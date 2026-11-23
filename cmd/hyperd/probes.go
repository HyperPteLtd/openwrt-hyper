package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

type probeResult struct {
	Node    string           `json:"node"`
	Latency *control.Latency `json:"latency,omitempty"`
	Error   string           `json:"error,omitempty"`
}

type probeSet struct {
	sync.Mutex
	cancel  context.CancelFunc
	seq     uint64
	source  string
	results map[string]map[string]probeResult
	slots   chan struct{}
}

func (p *probeSet) cancelLocked() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.seq++
}

func (p *probeSet) stop() {
	p.Lock()
	defer p.Unlock()
	p.cancelLocked()
}

func (p *probeSet) reset() {
	p.Lock()
	defer p.Unlock()
	p.cancelLocked()
	p.source = ""
	p.results = nil
}

func probeMethod(method string) (string, error) {
	switch method {
	case "", "url":
		return "url", nil
	case "tcp":
		return "tcp", nil
	default:
		return "", errors.New("unknown probe method")
	}
}

func (p *probeSet) snapshot(source, method string) (map[string]probeResult, error) {
	method, err := probeMethod(method)
	if err != nil {
		return nil, err
	}
	p.Lock()
	defer p.Unlock()
	out := map[string]probeResult{}
	if source == p.source {
		for k, v := range p.results[method] {
			out[k] = v
		}
	}
	return out, nil
}

func probeIDs(raw json.RawMessage) ([]string, error) {
	var in struct {
		Nodes []string `json:"nodes"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || len(in.Nodes) == 0 || len(in.Nodes) > 4096 {
		return nil, errors.New("choose between 1 and 4096 imported nodes")
	}
	seen := map[string]bool{}
	for _, id := range in.Nodes {
		if len(id) != 37 || !strings.HasPrefix(id, "node:") || seen[id] {
			return nil, errors.New("invalid or duplicate node")
		}
		if _, err := hex.DecodeString(id[5:]); err != nil {
			return nil, errors.New("invalid node identifier")
		}
		seen[id] = true
	}
	return in.Nodes, nil
}

// Unrecognized identifiers never become probe destinations or occupy the result cache.
func probeTargets(profile subscription.Profile, ids []string) (map[string]subscription.Node, error) {
	nodes := make(map[string]subscription.Node, len(profile.Nodes))
	for _, n := range profile.Nodes {
		nodes[n.ID] = n
	}
	for _, id := range ids {
		if _, ok := nodes[id]; !ok {
			return nil, errors.New("node no longer belongs to the active configuration")
		}
	}
	return nodes, nil
}

func measureTCP(ctx context.Context, node subscription.Node) probeResult {
	result := probeResult{Node: node.ID}
	network, port, ok := node.TCPProbeEndpoint()
	if !ok {
		result.Error = "not_applicable"
		return result
	}
	// Resolve before starting the clock. Dial a single address once so the
	// measurement is one TCP handshake, without DNS or protocol authentication.
	addr, err := netip.ParseAddr(node.Server)
	if err != nil {
		family := strings.Replace(network, "tcp", "ip", 1)
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, family, node.Server)
		if err != nil || len(addrs) == 0 {
			result.Error = "no confirmed measurement"
			return result
		}
		addr = addrs[0]
	}
	started := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(addr.String(), strconv.Itoa(port)))
	if err != nil {
		result.Error = "no confirmed measurement"
		return result
	}
	elapsed := time.Since(started)
	conn.Close()
	result.Latency = &control.Latency{Measured: time.Now().Unix(), Delay: uint64((elapsed + time.Millisecond - 1) / time.Millisecond)}
	return result
}

func (p *probeSet) begin(parent context.Context, source, method string, ids []string) (context.Context, context.CancelFunc, uint64) {
	p.Lock()
	p.cancelLocked()
	seq := p.seq
	if p.source != source || p.results == nil {
		p.source = source
		p.results = map[string]map[string]probeResult{}
	}
	if p.results[method] == nil {
		p.results[method] = make(map[string]probeResult)
	}
	for _, id := range ids {
		delete(p.results[method], id)
	}
	if p.slots == nil {
		p.slots = make(chan struct{}, 4)
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.Unlock()
	return ctx, cancel, seq
}

func (p *probeSet) run(ctx context.Context, seq uint64, method string, ids []string, measure func(context.Context, string) probeResult) error {
	queue := make(chan string)
	var wg sync.WaitGroup
	for range min(cap(p.slots), len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range queue {
				select {
				case p.slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				timeout, end := context.WithTimeout(ctx, 15*time.Second)
				r := measure(timeout, id)
				end()
				<-p.slots
				p.Lock()
				if p.seq == seq && ctx.Err() == nil {
					p.results[method][id] = r
				}
				p.Unlock()
			}
		}()
	}
dispatch:
	for _, id := range ids {
		select {
		case queue <- id:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(queue)
	wg.Wait()
	return ctx.Err()
}

func (s *app) probes(r request) (any, error) {
	st := s.m.Status()
	method, err := probeMethod(r.Method)
	if err != nil {
		return nil, err
	}
	if r.Source == "" || st.Intent.Source != r.Source || method == "url" && st.Phase != "running" {
		return nil, errors.New("active source changed or core stopped")
	}
	ids, err := probeIDs(r.Args)
	if err != nil {
		return nil, err
	}
	p := &s.tests
	ctx, cancel, seq := p.begin(s.ctx, r.Source, method, ids)
	job, err := s.runJob("", "latency/"+method, func(jobCtx context.Context) (json.RawMessage, error) {
		stop := context.AfterFunc(jobCtx, cancel)
		defer stop()
		defer cancel()
		defer func() {
			p.Lock()
			defer p.Unlock()
			if p.seq == seq {
				p.cancel = nil
			}
		}()
		current := s.m.Status()
		if current.Intent.Source != r.Source || method == "url" && current.Phase != "running" {
			return nil, errors.New("active source changed or core stopped")
		}
		profile, err := router.LoadProfile(current.Intent, subscription.OpenStore(router.Root+"/sources"))
		if err != nil {
			return nil, errors.New("subscription unavailable")
		}
		nodes, err := probeTargets(profile, ids)
		if err != nil {
			return nil, err
		}
		var caps subscription.CoreFeatures
		for _, id := range ids {
			if nodes[id].CapabilityKey() != "" {
				caps = subscription.CurrentCoreFeatures(ctx)
				break
			}
		}
		client := s.sys.Client()
		return nil, p.run(ctx, seq, method, ids, func(timeout context.Context, id string) probeResult {
			if caps.Availability(nodes[id].CapabilityKey()) == "unsupported" {
				return probeResult{Node: id, Error: "mihomo_protocol_unsupported"}
			}
			if nodes[id].Special {
				return probeResult{Node: id, Error: "not_applicable"}
			}
			if method == "tcp" {
				return measureTCP(timeout, nodes[id])
			}
			latency, err := client.Measure(timeout, id)
			if err != nil {
				return probeResult{Node: id, Error: "no confirmed measurement"}
			}
			return probeResult{Node: id, Latency: &latency}
		})
	})
	if err != nil {
		cancel()
	}
	return job, err
}
