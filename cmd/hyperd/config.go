// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mihomo"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
	"github.com/HyperPteLtd/openwrt-hyper/internal/uci"
)

// config reads /etc/config/hyper; a missing file is an empty config.
func (m *manager) config() (*uci.Config, error) {
	c, err := uci.Load(m.paths.UCI)
	if errors.Is(err, os.ErrNotExist) {
		return &uci.Config{}, nil
	}
	return c, err
}

// edit changes the config under the write lock and saves it when f returns true.
func (m *manager) edit(f func(c *uci.Config) bool) error {
	m.uciMu.Lock()
	defer m.uciMu.Unlock()
	c, err := m.config()
	if err != nil {
		return err
	}
	if !f(c) {
		return nil
	}
	return c.Save(m.paths.UCI)
}

func (m *manager) settings() (mihomo.Settings, error) {
	c, err := m.config()
	if err != nil {
		return mihomo.Settings{}, err
	}
	return mihomo.FromUCI(c)
}

// runtimeSettings are the settings a start applies: IPv6 interception whenever the core
// can reach the IPv6 internet (there is no switch). Its sockets pick no source address, so source-specific
// defaults ("default from <prefix>", common with PPPoE) do not count; without such a route
// the core would dial unreachable IPv6 addresses. The core and the firewall both use this.
func (m *manager) runtimeSettings(ctx context.Context) (mihomo.Settings, error) {
	s, err := m.settings()
	if err != nil {
		return s, err
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = m.sys.Run(c, "ip", "-6", "route", "get", "2400:3200::1")
	s.IPv6 = err == nil
	return s, nil
}

// tproxyProbe is the network helper's capture rule in a table of its own, for nft -c.
const tproxyProbe = `table inet hyper_probe {
	chain pre {
		type filter hook prerouting priority mangle; policy accept;
		meta l4proto { tcp, udp } meta mark set 0x1 tproxy to :1 accept
	}
}
`

// captureMethod picks how the LAN's traffic reaches the core, automatically like IPv6
// (there is no setting): TProxy where this firewall accepts the helper's tproxy rule
// (checked with nft -c, which changes nothing), otherwise TUN where the kernel has it.
// With neither, TProxy, so the start's network step reports what is missing.
// HYPERD_METHOD forces one in tests, as HYPERD_CORE_GROUP does the group.
func (m *manager) captureMethod(ctx context.Context) string {
	if v := os.Getenv("HYPERD_METHOD"); v == mihomo.MethodTProxy || v == mihomo.MethodTUN {
		return v
	}
	probe := filepath.Join(m.paths.RunDir, "tproxy-probe.nft")
	if err := os.WriteFile(probe, []byte(tproxyProbe), 0o600); err == nil {
		defer os.Remove(probe)
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := m.sys.Run(c, "nft", "-c", "-f", probe); err == nil {
			return mihomo.MethodTProxy
		}
	}
	if m.sys.Exists("/dev/net/tun") {
		return mihomo.MethodTUN
	}
	return mihomo.MethodTProxy
}

func (m *manager) enabled() bool {
	c, _ := m.config()
	return c.Get("config", "enabled") == "1"
}

// Settings the UI may change: section.option -> validator.
var settingsTable = map[string]func() *regexp.Regexp{
	"control.proxy_router_self":     lazyre.New(`^[01]$`),
	"config.log_level":              lazyre.New(`^(trace|debug|info|warn|error)$`),
	"config.dns_server":             lazyre.New(`^[A-Za-z0-9.:/\[\]_-]{1,255}$`),
	"config.china_dns_server":       lazyre.New(`^[A-Za-z0-9.:/\[\]_-]{1,255}$`),
	"subscription.auto_update":      lazyre.New(`^[01]$`),
	"subscription.auto_update_time": lazyre.New(`^([0-9]|1[0-9]|2[0-3])$`),
	"update.auto_check":             lazyre.New(`^[01]$`),
	"update.auto_update":            lazyre.New(`^[01]$`),
}

// UI mode -> routing_mode; "dns" keeps the smart split but intercepts nothing.
var modes = map[string]string{"smart": mihomo.ModeSmart, "global": mihomo.ModeGlobal, "dns": mihomo.ModeSmart}

func uiMode(c *uci.Config) string {
	if c.Get("config", "dns_only") == "1" {
		return "dns"
	}
	if c.Get("config", "routing_mode") == mihomo.ModeGlobal {
		return "global"
	}
	return "smart"
}

func readJSONFile(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

// nullable turns "" into JSON null, as uci.get returned for unset options.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nodeCache keeps the parsed nodes file: the UI polls status every few seconds and a
// few hundred nodes take milliseconds and a megabyte of allocations to parse (far more on
// a MIPS router). The store is shared and read-only.
type nodeCache struct {
	mu    sync.Mutex
	key   [4]int64 // inode, mtime, size, ctime: a sync renames a new file into place
	store *nodes.Store
}

func (m *manager) nodes() (*nodes.Store, error) {
	path := m.paths.nodes()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		if os.IsNotExist(err) {
			return nodes.Load(path) // empty store
		}
		return nil, err
	}
	key := [4]int64{int64(st.Ino), st.Mtim.Nano(), st.Size, st.Ctim.Nano()}
	m.nodeCache.mu.Lock()
	defer m.nodeCache.mu.Unlock()
	if m.nodeCache.store != nil && m.nodeCache.key == key {
		return m.nodeCache.store, nil
	}
	s, err := nodes.Load(path)
	if err != nil {
		return nil, err
	}
	m.nodeCache.key, m.nodeCache.store = key, s
	return s, nil
}
