// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/core"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mihomo"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
	"go.yaml.in/yaml/v3"
)

var (
	nodeName = lazyre.New(`^[A-Za-z0-9_]+$`)
	probeID  = lazyre.New(`^[A-Za-z0-9-]{1,64}$`)
)

const probeURL = "https://www.gstatic.com/generate_204"

func (m *manager) registerNodes() {
	m.handlers["nodes"] = func(context.Context, json.RawMessage) (any, error) {
		store, err := m.nodes()
		if err != nil {
			return nil, err
		}
		list := make([]nodes.Summary, 0, len(store.Nodes))
		for _, n := range store.Nodes {
			list = append(list, n.Summary())
		}
		c, _ := m.config()
		return map[string]any{"nodes": list, "main_node": nullable(c.Get("config", "main_node"))}, nil
	}
	m.handlers["select_node"] = m.selectNode
	m.handlers["latency"] = m.probes.start
	m.handlers["latency_status"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ ID string }
		_ = jsonArgs(args, &a)
		if !probeID().MatchString(a.ID) {
			return map[string]any{"result": false, "error": "invalid_job"}, nil
		}
		return m.probes.status(a.ID), nil
	}
	m.handlers["latency_cancel"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ ID string }
		_ = jsonArgs(args, &a)
		m.probes.cancel(a.ID)
		return map[string]any{"result": true}, nil
	}
}

// selectNode persists the choice, then switches main-out live (no restart). If the live
// switch fails the service restarts with the new default.
func (m *manager) selectNode(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct{ Node string }
	_ = jsonArgs(args, &a)
	store, err := m.nodes()
	if err != nil {
		return nil, err
	}
	if _, ok := store.Find(a.Node); !nodeName().MatchString(a.Node) || !ok {
		return map[string]any{"result": false, "error": "unknown node"}, nil
	}
	if err := m.edit(func(c *uciConfig) bool { c.Ensure("config", "hyper").Set("main_node", a.Node); return true }); err != nil {
		return nil, err
	}
	m.probes.cancel("")
	go func() {
		api := m.svc.controller()
		if api == nil {
			return // a stopped service keeps the choice for its next start
		}
		c, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		if err := api.Select(c, mihomo.MainGroup, nodes.Tag(a.Node)); err != nil {
			cfg, _ := m.config()
			if cfg.Get("config", "main_node") == a.Node {
				m.svc.request("restart")
			}
			return
		}
		// Open connections stay on the old node otherwise.
		if err := api.CloseThrough(c, mihomo.MainGroup); err != nil {
			m.log.Errorf("CORE", "Closing connections through the previous node failed: %v", err)
		}
	}()
	return map[string]any{"result": true, "accepted": true}, nil
}

// probes measures node latency. With the service running it asks the live core; otherwise
// it starts an isolated core with only the nodes and a loopback controller.
type probes struct {
	m   *manager
	mu  sync.Mutex
	job *probeJob
}

type probeJob struct {
	id        string
	nodes     []string
	started   time.Time
	state     string // queued | running | done | failed | cancelled
	errText   string
	latency   map[string]*int64
	cancelled bool
	stop      context.CancelFunc
}

func (p *probes) start(_ context.Context, args json.RawMessage) (any, error) {
	var a struct{ Nodes []string }
	_ = jsonArgs(args, &a)
	store, err := p.m.nodes()
	if err != nil {
		return nil, err
	}
	var names []string
	if len(a.Nodes) == 0 {
		for _, n := range store.Nodes {
			names = append(names, n.ID)
		}
	} else {
		for _, n := range a.Nodes {
			if _, ok := store.Find(n); ok && nodeName().MatchString(n) && !contains(names, n) {
				names = append(names, n)
			}
		}
	}
	if len(names) > 64 {
		names = names[:64]
	}
	if len(names) == 0 {
		return map[string]any{"result": false, "error": "no_nodes"}, nil
	}
	p.cancel("")
	ctx, cancel := context.WithTimeout(p.m.ctx, 120*time.Second)
	job := &probeJob{id: uuid(), nodes: names, started: p.m.sys.Now(), state: "queued", latency: map[string]*int64{}, stop: cancel}
	p.mu.Lock()
	p.job = job
	p.mu.Unlock()
	go p.run(ctx, job, store)
	return p.status(job.id), nil
}

func (p *probes) cancel(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != nil && (id == "" || p.job.id == id) && !p.job.cancelled {
		p.job.cancelled = true
		p.job.stop()
	}
}

func (p *probes) status(id string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	j := p.job
	if j == nil || j.id != id || j.cancelled {
		return map[string]any{"result": true, "id": id, "state": "cancelled", "latency": map[string]any{}, "completed": 0, "total": 0}
	}
	lat := map[string]*int64{}
	for k, v := range j.latency {
		lat[k] = v
	}
	out := map[string]any{"result": true, "id": id, "state": j.state, "error": j.errText, "latency": lat, "completed": len(lat), "total": len(j.nodes)}
	if j.state != "done" && j.state != "failed" && p.m.sys.Now().Sub(j.started) > 120*time.Second {
		out["state"], out["error"] = "failed", "probe_timeout"
	}
	return out
}

func (p *probes) set(j *probeJob, f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !j.cancelled {
		f()
	}
}

func (p *probes) run(ctx context.Context, j *probeJob, store *nodes.Store) {
	defer j.stop()
	p.set(j, func() { j.state = "running" })
	api := p.m.svc.controller()
	stop := func() {}
	if api == nil || !p.m.svc.isRunning() {
		var err error
		api, stop, err = p.isolated(ctx, j, store)
		if err != nil {
			p.set(j, func() { j.state, j.errText = "failed", err.Error() })
			return
		}
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, name := range j.nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(name string) {
			defer func() { <-sem; wg.Done() }()
			ms, err := api.Delay(ctx, nodes.Tag(name), probeURL, 3*time.Second)
			var v *int64
			if err == nil {
				n := int64(max(ms, 1))
				v = &n
			}
			if ctx.Err() == nil {
				p.set(j, func() { j.latency[name] = v })
			}
		}(name)
	}
	wg.Wait()
	timedOut := ctx.Err() != nil
	// The isolated core and its files are gone before the result is published.
	stop()
	if timedOut {
		p.set(j, func() { j.state, j.errText = "failed", "probe_timeout" })
		return
	}
	p.set(j, func() { j.state = "done" })
}

// isolated starts a probe-only core: the selected nodes, direct DNS, no listeners.
func (p *probes) isolated(ctx context.Context, j *probeJob, store *nodes.Store) (*control.Client, func(), error) {
	s, err := p.m.settings()
	if err != nil {
		return nil, nil, fmt.Errorf("probe_config_failed")
	}
	dir := filepath.Join(p.m.paths.RunDir, "probe")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	var proxies []map[string]any
	for _, name := range j.nodes {
		n, _ := store.Find(name)
		if !contains(mihomo.Supported, n.Type()) {
			continue
		}
		px := map[string]any{}
		for k, v := range n.Proxy {
			px[k] = v
		}
		px["name"] = n.Tag()
		proxies = append(proxies, px)
	}
	resolver := p.m.wanResolver(ctx, false)
	if _, err := netip.ParseAddr(resolver); err != nil {
		resolver = "223.5.5.5"
	}
	port := 20000 + int(p.m.sys.Now().UnixNano()%30000)
	secret := randomSecret()
	cfg := map[string]any{"mode": "direct", "log-level": "error", "routing-mark": s.Mark, "allow-lan": false,
		"external-controller": fmt.Sprintf("127.0.0.1:%d", port), "secret": secret, "proxies": proxies,
		"dns":     map[string]any{"enable": true, "nameserver": []string{resolver}, "proxy-server-nameserver": []string{resolver}},
		"profile": map[string]any{"store-selected": false}, "geodata-mode": false, "geo-auto-update": false,
		"experimental": mihomo.QUICExperimental}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, nil, err
	}
	gid, err := coreGroup()
	if err != nil {
		return nil, nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	sup := &core.Supervisor{Core: core.Core{Bin: p.m.paths.CoreBin, Dir: dir, Config: path, Env: coreEnv(), Group: gid}}
	done := make(chan struct{})
	go func() { _ = sup.Run(pctx); close(done) }()
	stop := func() { cancel(); <-done; os.RemoveAll(dir) }
	api := control.New(fmt.Sprintf("127.0.0.1:%d", port), secret)
	for i := 0; i < 50; i++ {
		c, cc := context.WithTimeout(ctx, time.Second)
		_, err := api.Version(c)
		cc()
		if err == nil {
			return api, stop, nil
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return nil, nil, fmt.Errorf("probe_start_failed")
}

// deviceLists keeps the per-MAC address files current while the core runs, so device
// rules follow DHCP changes without a restart.
type deviceLists struct {
	m    *manager
	mu   sync.Mutex
	macs []string
	last map[string]string
}

func (d *deviceLists) reset(macs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.macs, d.last = macs, map[string]string{}
}

func (d *deviceLists) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		d.refresh(ctx)
		trimLog(d.m.paths.Log, 512<<10)
		trimLog(d.m.paths.CoreLog, 512<<10)
	}
}

func (d *deviceLists) refresh(ctx context.Context) {
	api := d.m.svc.controller()
	d.mu.Lock()
	macs := append([]string(nil), d.macs...)
	d.mu.Unlock()
	if api == nil || len(macs) == 0 {
		return
	}
	neigh := d.m.neighbors(ctx)
	for _, mac := range macs {
		var b strings.Builder
		for _, a := range neigh[mac] {
			b.WriteString(netip.PrefixFrom(a, a.BitLen()).String() + "\n")
		}
		body := b.String()
		d.mu.Lock()
		same := d.last[mac] == body
		d.last[mac] = body
		d.mu.Unlock()
		path := filepath.Join(d.m.paths.CoreDir, mihomo.DeviceFile(mac))
		if old, err := os.ReadFile(path); same || (err == nil && string(old) == body) {
			continue
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = api.RefreshRules(c, mihomo.ProviderName(mac))
		cancel()
	}
}
