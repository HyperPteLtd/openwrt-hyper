// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// fakeUCI holds /etc/config/hyper as option -> value (lists space separated, sections -> type)
// and applies the batches the daemon writes.
func fakeUCI(t *testing.T, config map[string]string) map[string]string {
	t.Helper()
	get, batch := uciGet, uciBatch
	t.Cleanup(func() { uciGet, uciBatch = get, batch })
	uciGet = func(_ context.Context, key string) string { return config[key] }
	line := regexp.MustCompile(`^(set|add_list|delete|commit) ?([^=]*)(?:='?([^']*)'?)?$`)
	uciBatch = func(_ context.Context, script string) error {
		for _, l := range strings.Split(strings.TrimSpace(script), "\n") {
			m := line.FindStringSubmatch(l)
			if m == nil {
				t.Fatalf("unexpected uci line %q", l)
			}
			switch m[1] {
			case "set":
				config[m[2]] = m[3]
			case "add_list":
				config[m[2]] = strings.TrimSpace(config[m[2]] + " " + m[3])
			case "delete":
				for k := range config {
					if k == m[2] || strings.HasPrefix(k, m[2]+".") {
						delete(config, k)
					}
				}
			}
		}
		return nil
	}
	return config
}

// A router upgraded from 0.7 keeps its settings, its devices and lists, and its sign-in.
func TestMigrateLegacy(t *testing.T) {
	ctx := context.Background()
	config := fakeUCI(t, map[string]string{
		"hyper.account": "hyper", "hyper.account.device_id": "0d0c0b0a-0000-4000-8000-000000000001",
		"hyper.infra": "hyper", "hyper.infra.dns_redirect": "1", "hyper.infra.lan_network": "lan guest wan",
		"hyper.config": "hyper", "hyper.config.enabled": "1", "hyper.config.routing_mode": "global", "hyper.config.log_level": "warn",
		"hyper.control": "hyper", "hyper.control.proxy_router_self": "0", "hyper.control.lan_proxy_mode": "except_listed",
		"hyper.control.lan_direct_mac_addrs": "AA:BB:CC:DD:EE:01 not-a-mac",
		"hyper.subscription":                 "hyper", "hyper.subscription.auto_update": "1", "hyper.subscription.auto_update_time": "3",
		"hyper.update": "hyper", "hyper.update.auto_check": "1", "hyper.update.auto_update": "1",
		"hyper.rules": "hyper", "hyper.rules.disabled": "streaming streaming bad\"key",
	})
	etc := t.TempDir()
	legacyEtc = etc
	t.Cleanup(func() { legacyEtc = router.Root })
	os.MkdirAll(filepath.Join(etc, "resources"), 0o755)
	os.WriteFile(filepath.Join(etc, "resources", "direct_list.txt"), []byte("baidu\n\n# note\nqq.com\nbad entry\n"), 0o644)
	os.WriteFile(filepath.Join(etc, "resources", "proxy_list.txt"), []byte("google\n"), 0o644)
	os.WriteFile(filepath.Join(etc, "session"), []byte("signed in"), 0o600)
	os.WriteFile(filepath.Join(etc, "nodes.json"), []byte("[]"), 0o644)
	m := router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming)

	disabled, err := migrateLegacy(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(disabled, []string{"streaming"}) {
		t.Fatal("switched-off lists:", disabled)
	}
	for k, want := range map[string]string{"hyper.main": "router", "hyper.main.enabled": "1", "hyper.main.dns": "1",
		"hyper.main.method": "tproxy", "hyper.main.fail_open": "1", "hyper.main.update_hours": "6", "hyper.main.network": "lan guest",
		"hyper.main.update_at": "3", "hyper.main.router_self": "0", "hyper.main.auto_check": "1", "hyper.main.auto_update": "1",
		"hyper.main.log_level":    "warning",
		"hyper.account.device_id": "0d0c0b0a-0000-4000-8000-000000000001"} {
		if config[k] != want {
			t.Errorf("%s = %q, want %q", k, config[k], want)
		}
	}
	for _, s := range legacySections {
		if config["hyper."+s] != "" {
			t.Error("0.x section left:", s)
		}
	}
	i := m.Status().Intent
	if i.Mode != "global" || i.Policy.DefaultDirect || len(i.Policy.Devices) != 1 || i.Policy.Devices[0] != (router.DeviceRule{Address: "aa:bb:cc:dd:ee:01", Target: "@direct"}) {
		t.Fatalf("%+v", i)
	}
	if len(i.Policy.Rules) != 2 || i.Policy.Rules[0].Target != "@direct" || i.Policy.Rules[1].Target != subscription.HyperGroupID() ||
		!slices.Equal(i.Policy.Rules[0].Match["domain_keyword"].([]any), []any{"baidu", "qq.com"}) {
		t.Fatalf("%+v", i.Policy.Rules)
	}
	if _, err := os.Stat(filepath.Join(etc, "resources")); !os.IsNotExist(err) {
		t.Error("0.x lists left")
	}
	if _, err := os.Stat(filepath.Join(etc, "nodes.json")); !os.IsNotExist(err) {
		t.Error("0.x nodes left")
	}
	if _, err := os.Stat(filepath.Join(etc, "session")); err != nil {
		t.Error("the sign-in went:", err)
	}
	// Once: a second start changes nothing.
	revision := i.Revision
	if disabled, err := migrateLegacy(ctx, m); err != nil || disabled != nil || m.Status().Intent.Revision != revision {
		t.Fatal(disabled, err)
	}
}

func TestMigrateListedOnly(t *testing.T) {
	ctx := context.Background()
	fakeUCI(t, map[string]string{
		"hyper.config": "hyper", "hyper.config.enabled": "0", "hyper.config.routing_mode": "smart",
		"hyper.control": "hyper", "hyper.control.lan_proxy_mode": "listed_only",
		"hyper.control.lan_proxy_mac_addrs": "aa:bb:cc:dd:ee:02",
		"hyper.subscription":                "hyper", "hyper.subscription.auto_update": "0",
	})
	legacyEtc = t.TempDir()
	t.Cleanup(func() { legacyEtc = router.Root })
	m := router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming)
	if _, err := migrateLegacy(ctx, m); err != nil {
		t.Fatal(err)
	}
	i := m.Status().Intent
	if i.Mode != "smart" || !i.Policy.DefaultDirect || len(i.Policy.Devices) != 1 || i.Policy.Devices[0].Target != "@proxy" {
		t.Fatalf("%+v", i)
	}
	if got := uciGet(ctx, "hyper.main.update_hours"); got != "0" {
		t.Fatal("automatic sync left on:", got)
	}
	if got := uciGet(ctx, "hyper.main.network"); got != "lan" {
		t.Fatal(got)
	}
}

// A fresh router gets its identifier once; one it has stays.
func TestEnsureDeviceID(t *testing.T) {
	ctx := context.Background()
	config := fakeUCI(t, map[string]string{"hyper.main": "router"})
	if err := ensureDeviceID(ctx); err != nil {
		t.Fatal(err)
	}
	id := config["hyper.account.device_id"]
	if config["hyper.account"] != "account" || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatal(config)
	}
	if err := ensureDeviceID(ctx); err != nil || config["hyper.account.device_id"] != id {
		t.Fatal("identifier changed:", err, config)
	}
	// Nothing to carry over on a router that never ran 0.x.
	m := router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming)
	if disabled, err := migrateLegacy(ctx, m); err != nil || disabled != nil || m.Status().Intent.Revision != 0 {
		t.Fatal(disabled, err)
	}
}
