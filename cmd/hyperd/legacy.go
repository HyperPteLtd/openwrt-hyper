// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// A router upgraded from 0.x keeps its /etc/config/hyper (a conffile), still in 0.x's
// sections: config, infra, control, subscription, update and rules. Its first start here
// carries them over once, to hyper.main and the intent, and its own files under /etc/hyper
// (the custom keyword lists, the node and rule caches) go. The account section and its
// device_id stay, as does the session: the router stays signed in.

// uciGet and uciBatch are the router's UCI; tests replace them.
var (
	uciGet = func(ctx context.Context, key string) string {
		out, _ := exec.CommandContext(ctx, "uci", "-q", "get", key).Output()
		return strings.TrimSpace(string(out))
	}
	uciBatch = func(ctx context.Context, script string) error {
		cmd := exec.CommandContext(ctx, "uci", "-q", "batch")
		cmd.Stdin = strings.NewReader(script)
		return cmd.Run()
	}
	legacyEtc = router.Root
)

var legacySections = []string{"config", "infra", "control", "subscription", "update", "rules"}

var (
	legacyNetwork = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	legacyEntry   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

// ensureDeviceID gives the router the identifier it signs in with, once.
func ensureDeviceID(ctx context.Context) error {
	if uciGet(ctx, "hyper.account.device_id") != "" {
		return nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	script := ""
	if uciGet(ctx, "hyper.account") == "" {
		script = "set hyper.account=account\n"
	}
	return uciBatch(ctx, script+"set hyper.account.device_id='"+id+"'\ncommit hyper\n")
}

// migrateLegacy carries 0.x's settings over and returns the server lists it had switched
// off, for the account's source (adopt). Nothing happens on a router without them.
func migrateLegacy(ctx context.Context, m *router.Manager) (disabled []string, err error) {
	legacy := false
	for _, s := range legacySections {
		if uciGet(ctx, "hyper."+s) != "" {
			legacy = true
		}
	}
	if !legacy {
		return nil, nil
	}
	var b strings.Builder
	if uciGet(ctx, "hyper.main") == "" {
		bit := func(on bool) string {
			if on {
				return "1"
			}
			return "0"
		}
		var networks []string
		for _, n := range strings.Fields(uciGet(ctx, "hyper.infra.lan_network")) {
			if legacyNetwork.MatchString(n) && n != "wan" && n != "wan6" && n != "loopback" && !slices.Contains(networks, n) && len(networks) < 16 {
				networks = append(networks, n)
			}
		}
		if len(networks) == 0 {
			networks = []string{"lan"}
		}
		hours := "6"
		if uciGet(ctx, "hyper.subscription.auto_update") == "0" {
			hours = "0"
		}
		b.WriteString("set hyper.main=router\n")
		b.WriteString("set hyper.main.enabled='" + bit(uciGet(ctx, "hyper.config.enabled") == "1") + "'\n")
		b.WriteString("set hyper.main.method='tproxy'\n")
		b.WriteString("set hyper.main.dns='" + bit(uciGet(ctx, "hyper.infra.dns_redirect") != "0") + "'\n")
		b.WriteString("set hyper.main.fail_open='1'\n")
		b.WriteString("set hyper.main.update_hours='" + hours + "'\n")
		// 0.x read the account daily at its sync hour (04:00 unless chosen).
		at := router.UpdateHour(uciGet(ctx, "hyper.subscription.auto_update_time"))
		if at < 0 {
			at = 4
		}
		b.WriteString("set hyper.main.update_at='" + strconv.Itoa(at) + "'\n")
		b.WriteString("set hyper.main.router_self='" + bit(uciGet(ctx, "hyper.control.proxy_router_self") != "0") + "'\n")
		b.WriteString("set hyper.main.dns_only='" + bit(uciGet(ctx, "hyper.config.dns_only") == "1") + "'\n")
		b.WriteString("set hyper.main.auto_check='" + bit(uciGet(ctx, "hyper.update.auto_check") != "0") + "'\n")
		b.WriteString("set hyper.main.auto_update='" + bit(uciGet(ctx, "hyper.update.auto_update") == "1") + "'\n")
		level := uciGet(ctx, "hyper.config.log_level")
		if level == "warn" {
			level = "warning"
		}
		if slices.Contains(router.LogLevels, level) {
			b.WriteString("set hyper.main.log_level='" + level + "'\n")
		}
		for _, n := range networks {
			b.WriteString("add_list hyper.main.network='" + n + "'\n")
		}
		policy, mode := legacyPolicy(ctx)
		for _, k := range strings.Fields(uciGet(ctx, "hyper.rules.disabled")) {
			if rulekey.Valid(k) && !slices.Contains(disabled, k) {
				disabled = append(disabled, k)
			}
		}
		if _, err = m.Change(nil, func(i *router.Intent) error {
			i.Mode = mode
			i.Policy.DefaultDirect = policy.DefaultDirect
			i.Policy.Devices = append(i.Policy.Devices, policy.Devices...)
			for _, r := range policy.Rules {
				if !slices.ContainsFunc(i.Policy.Rules, func(o router.CustomRule) bool { return o.ID == r.ID }) {
					i.Policy.Rules = append(i.Policy.Rules, r)
				}
			}
			return router.ValidatePolicy(i.Policy)
		}); err != nil {
			return nil, err
		}
	}
	for _, s := range legacySections {
		if uciGet(ctx, "hyper."+s) != "" {
			b.WriteString("delete hyper." + s + "\n")
		}
	}
	b.WriteString("commit hyper\n")
	if err = uciBatch(ctx, b.String()); err != nil {
		return nil, err
	}
	for _, name := range []string{"resources", "rules", "rule-set", "nodes.json"} {
		_ = os.RemoveAll(filepath.Join(legacyEtc, name))
	}
	return disabled, nil
}

// legacyPolicy is 0.x's routing mode, LAN access control and custom keyword lists.
func legacyPolicy(ctx context.Context) (router.Policy, string) {
	var p router.Policy
	mode := "smart"
	if uciGet(ctx, "hyper.config.routing_mode") == "global" {
		mode = "global"
	}
	macs := func(option string) []string {
		var out []string
		for _, v := range strings.Fields(uciGet(ctx, "hyper.control."+option)) {
			if mac, err := net.ParseMAC(v); err == nil && len(mac) == 6 && mac[0]&1 == 0 && !slices.Contains(out, mac.String()) {
				out = append(out, mac.String())
			}
		}
		return out
	}
	switch uciGet(ctx, "hyper.control.lan_proxy_mode") {
	case "except_listed": // the listed devices go direct
		for _, mac := range macs("lan_direct_mac_addrs") {
			p.Devices = append(p.Devices, router.DeviceRule{Address: mac, Target: "@direct"})
		}
	case "listed_only": // only the listed devices are proxied
		p.DefaultDirect = true
		for _, mac := range macs("lan_proxy_mac_addrs") {
			p.Devices = append(p.Devices, router.DeviceRule{Address: mac, Target: "@proxy"})
		}
	}
	// 0.x's lists held domain keywords, one a line.
	for _, list := range []struct{ file, id, note, target string }{
		{"direct_list.txt", "legacy-direct", "直连关键词（0.x）", "@direct"},
		{"proxy_list.txt", "legacy-proxy", "代理关键词（0.x）", subscription.HyperGroupID()},
	} {
		data, err := os.ReadFile(filepath.Join(legacyEtc, "resources", list.file))
		if errors.Is(err, os.ErrNotExist) || err != nil {
			continue
		}
		var keywords []any
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if legacyEntry.MatchString(line) && !slices.Contains(keywords, any(line)) && len(keywords) < 4096 {
				keywords = append(keywords, line)
			}
		}
		if len(keywords) > 0 {
			p.Rules = append(p.Rules, router.CustomRule{ID: list.id, Note: list.note,
				Rule: subscription.Rule{Match: map[string]any{"domain_keyword": keywords}, Target: list.target}})
		}
	}
	return p, mode
}
