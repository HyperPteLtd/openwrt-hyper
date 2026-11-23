// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"github.com/HyperPteLtd/openwrt-hyper/internal/update"
)

var (
	listEntry = lazyre.New(`^[A-Za-z0-9._-]{1,253}$`)
	apkPkg    = lazyre.New(update.Package + `-(\S+)`)
)

func (m *manager) registerSettings() {
	m.handlers["task_status"] = m.tasks.status
	m.handlers["status"] = m.status
	m.handlers["get_config"] = m.getConfig
	m.handlers["set_config"] = m.setConfig

	m.handlers["set_mode"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ Mode string }
		_ = jsonArgs(args, &a)
		routing, ok := modes[a.Mode]
		if !ok {
			return map[string]any{"result": false, "error": "illegal mode"}, nil
		}
		if err := m.edit(func(c *uciConfig) bool {
			s := c.Ensure("config", "hyper")
			s.Set("routing_mode", routing)
			s.Set("dns_only", map[bool]string{true: "1", false: "0"}[a.Mode == "dns"])
			return true
		}); err != nil {
			return nil, err
		}
		m.restartIfEnabled()
		return map[string]any{"result": true}, nil
	}

	m.handlers["rules_list"] = func(context.Context, json.RawMessage) (any, error) {
		c, _ := m.config()
		disabled := c.Section("rules").List("disabled")
		var index []subscription.IndexEntry
		readJSONFile(filepath.Join(m.paths.rules(), "index.json"), &index)
		rules := make([]map[string]any, 0, len(index))
		for _, r := range index {
			rules = append(rules, map[string]any{"key": r.Key, "policy": r.Policy, "server_enabled": r.Enabled, "count": r.Count,
				"source": r.Source, "stale": r.Stale, "enabled": r.Enabled && !contains(disabled, r.Key)})
		}
		return map[string]any{"rules": rules, "applies": uiMode(c) != "global"}, nil
	}
	m.handlers["set_rule_enabled"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct {
			Key     string
			Enabled bool
		}
		_ = jsonArgs(args, &a)
		if !rulekey.Valid(a.Key) {
			return map[string]any{"result": false, "error": "illegal key"}, nil
		}
		if err := m.edit(func(c *uciConfig) bool {
			s := c.Ensure("rules", "hyper")
			var keep []string
			for _, k := range s.List("disabled") {
				if k != a.Key {
					keep = append(keep, k)
				}
			}
			if !a.Enabled {
				keep = append(keep, a.Key)
			}
			s.SetList("disabled", keep)
			return true
		}); err != nil {
			return nil, err
		}
		m.restartIfEnabled()
		return map[string]any{"result": true}, nil
	}

	m.handlers["acllist_read"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ Type string }
		_ = jsonArgs(args, &a)
		if a.Type != "direct_list" && a.Type != "proxy_list" {
			return map[string]any{"content": nil, "error": "illegal type"}, nil
		}
		b, _ := os.ReadFile(m.paths.list(a.Type))
		return map[string]any{"content": string(b)}, nil
	}
	m.handlers["acllist_write"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ Type, Content string }
		_ = jsonArgs(args, &a)
		if a.Type != "direct_list" && a.Type != "proxy_list" {
			return map[string]any{"result": false, "error": "illegal type"}, nil
		}
		var lines []string
		for _, line := range strings.Split(strings.ReplaceAll(a.Content, "\r", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !listEntry().MatchString(line) {
				if len(line) > 64 {
					line = line[:64]
				}
				return map[string]any{"result": false, "error": "illegal entry: " + line}, nil
			}
			if !contains(lines, line) {
				lines = append(lines, line)
			}
		}
		body := ""
		if len(lines) > 0 {
			body = strings.Join(lines, "\n") + "\n"
		}
		path := m.paths.list(a.Type)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := atomicfile.WriteFile(path, []byte(body), 0o644); err != nil {
			return nil, err
		}
		m.restartIfEnabled()
		return map[string]any{"result": true, "count": len(lines)}, nil
	}

	m.handlers["lan_clients"] = m.lanClients
	m.handlers["set_access_control"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct {
			Mode       string
			DirectMACs []string `json:"direct_macs"`
		}
		_ = jsonArgs(args, &a)
		if a.Mode != "all" && a.Mode != "pick" {
			return map[string]any{"result": false, "error": "illegal mode"}, nil
		}
		var macs []string
		for _, s := range a.DirectMACs {
			if hw, err := net.ParseMAC(s); err == nil && len(hw) == 6 && !contains(macs, hw.String()) {
				macs = append(macs, hw.String())
			}
		}
		if err := m.edit(func(c *uciConfig) bool {
			s := c.Ensure("control", "hyper")
			if a.Mode == "all" {
				s.Set("lan_proxy_mode", "disabled")
				s.Delete("lan_direct_mac_addrs")
			} else {
				s.Set("lan_proxy_mode", "except_listed")
				s.SetList("lan_direct_mac_addrs", macs)
			}
			return true
		}); err != nil {
			return nil, err
		}
		m.restartIfEnabled()
		return map[string]any{"result": true}, nil
	}
}

func (m *manager) status(ctx context.Context, _ json.RawMessage) (any, error) {
	c, err := m.config()
	if err != nil {
		return nil, err
	}
	sess := m.sessions.Load()
	main := c.Get("config", "main_node")
	if main == "nil" {
		main = ""
	}
	var label any
	if main != "" {
		if store, err := m.nodes(); err == nil {
			if n, ok := store.Find(main); ok {
				label = n.Label
			}
		}
	}
	out := map[string]any{
		"enabled": c.Get("config", "enabled") == "1", "running": false, "memory_kb": nil, "uptime": nil,
		"operation": m.svc.operation(), "main_node": nullable(main), "main_node_label": label, "mode": uiMode(c),
		"signed_in": sess != nil, "email": nil, "api_base": m.client.Resolver.Current(), "sync": m.syncState(),
		"health": m.svc.healthState(), "core_version": m.coreVersion(ctx), "version": m.packageVersion(ctx),
	}
	if sess != nil {
		out["email"] = sess.Email
	}
	if st, ok := m.svc.state(); ok && st.Running {
		out["running"] = true
		out["memory_kb"] = procMemoryKB(st.PID)
		out["uptime"] = int64(m.sys.Now().Sub(st.Since).Seconds())
	}
	var check struct {
		Time   *int64  `json:"time"`
		Latest *string `json:"latest"`
	}
	readJSONFile(filepath.Join(m.paths.Update, "check.json"), &check)
	version, _ := out["version"].(string)
	var st any
	if s := update.ReadState(m.paths.Update); s != nil {
		st = s
	}
	out["update"] = map[string]any{
		"auto_check": c.Get("update", "auto_check") != "0", "auto_update": c.Get("update", "auto_update") == "1",
		"checked": check.Time, "latest": check.Latest,
		"available": check.Latest != nil && *check.Latest != "" && update.Compare(*check.Latest, version) > 0, "state": st,
	}
	return out, nil
}

// cached keeps a value until a file's mtime/size changes (versions change only on install).
type cached struct {
	stamp string
	value any
}

var versionCache = map[string]*cached{}

func (m *manager) cachedBy(key, file string, compute func() any) any {
	stamp := ""
	if st, err := os.Stat(file); err == nil {
		stamp = fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
	}
	m.uciMu.Lock()
	c := versionCache[key]
	m.uciMu.Unlock()
	if c != nil && c.stamp == stamp {
		return c.value
	}
	v := compute()
	m.uciMu.Lock()
	versionCache[key] = &cached{stamp, v}
	m.uciMu.Unlock()
	return v
}

func (m *manager) coreVersion(ctx context.Context) any {
	return m.cachedBy("core", m.paths.CoreBin, func() any {
		out, err := m.sys.Run(ctx, m.paths.CoreBin, "-v")
		if err != nil {
			return nil
		}
		line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return nullable(strings.TrimSpace(line))
	})
}

func (m *manager) packageVersion(ctx context.Context) any {
	ctl := "/usr/lib/opkg/info/luci-app-hyper.control"
	if b, err := os.ReadFile(ctl); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "Version:"); ok {
				return update.Normalize(strings.TrimSpace(v))
			}
		}
	}
	return m.cachedBy("package", "/lib/apk/db/installed", func() any {
		if !m.sys.Exists("/usr/bin/apk") {
			return nil
		}
		out, _ := m.sys.Run(ctx, "/usr/bin/apk", "list", "-I", update.Package)
		if mt := apkPkg().FindSubmatch(out); mt != nil {
			return update.Normalize(string(mt[1]))
		}
		return nil
	})
}

func (m *manager) getConfig(context.Context, json.RawMessage) (any, error) {
	c, err := m.config()
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	for key := range settingsTable {
		sec, opt, _ := strings.Cut(key, ".")
		values[key] = nullable(c.Get(sec, opt))
	}
	return map[string]any{"config": values, "api_base": m.client.Resolver.Current()}, nil
}

func (m *manager) setConfig(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Values map[string]any
	}
	if jsonArgs(args, &a) != nil || a.Values == nil {
		return map[string]any{"result": false, "error": "illegal values"}, nil
	}
	vals := map[string]string{}
	for key, raw := range a.Values {
		re := settingsTable[key]
		v := jsonScalar(raw)
		if re == nil || !re().MatchString(v) {
			return map[string]any{"result": false, "error": "illegal value for " + key}, nil
		}
		vals[key] = v
	}
	restart, updateChanged := false, false
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if err := m.edit(func(c *uciConfig) bool {
		changed := false
		for _, key := range keys {
			sec, opt, _ := strings.Cut(key, ".")
			if c.Get(sec, opt) == vals[key] {
				continue
			}
			changed = true
			if sec == "update" {
				updateChanged = true
			} else if sec != "subscription" {
				restart = true
			}
			c.Ensure(sec, "hyper").Set(opt, vals[key])
		}
		return changed
	}); err != nil {
		return nil, err
	}
	if restart {
		m.restartIfEnabled()
	}
	if updateChanged {
		go m.autoUpdate(m.ctx, true)
	}
	return map[string]any{"result": true}, nil
}

func (m *manager) lanClients(ctx context.Context, _ json.RawMessage) (any, error) {
	type client struct {
		MAC    string  `json:"mac"`
		IP     string  `json:"ip"`
		Name   *string `json:"name"`
		Online bool    `json:"online,omitempty"`
	}
	byMAC := map[string]*client{}
	var order []string
	add := func(mac string) *client {
		if c := byMAC[mac]; c != nil {
			return c
		}
		c := &client{MAC: mac}
		byMAC[mac] = c
		order = append(order, mac)
		return c
	}
	if b, err := os.ReadFile(m.paths.Leases); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			hw, err := net.ParseMAC(f[1])
			if err != nil {
				continue
			}
			c := add(hw.String())
			c.IP = f[2]
			if f[3] != "*" {
				name := f[3]
				c.Name = &name
			}
		}
	}
	for mac, addrs := range m.neighbors(ctx) {
		for _, a := range addrs {
			if a.Is4() {
				c := add(mac)
				if c.IP == "" {
					c.IP = a.String()
				}
				c.Online = true
				break
			}
		}
	}
	clients := make([]*client, 0, len(order))
	for _, mac := range order {
		if byMAC[mac].IP != "" {
			clients = append(clients, byMAC[mac])
		}
	}
	c, _ := m.config()
	mode := c.Get("control", "lan_proxy_mode")
	if mode == "" {
		mode = "disabled"
	}
	direct := c.Section("control").List("lan_direct_mac_addrs")
	if direct == nil {
		direct = []string{}
	}
	return map[string]any{"clients": clients, "lan_proxy_mode": mode, "direct_macs": direct}, nil
}
