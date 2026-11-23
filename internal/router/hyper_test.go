// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package router

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/HyperPteLtd/openwrt-hyper/internal/hypersync"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// The Hyper profile compiles in every mode: its lists become file rule sets in catalog order,
// mainland names and addresses use the built-in sets, and the rest go to its selector.
func TestCompileHyperProfile(t *testing.T) {
	feed := &hypersync.Feed{
		Countries: []hypersync.Country{
			{Code: "JP", Sort: 9, Body: []byte("anytls://pw@jp.example.com:443?sni=jp.example.com#日本 01\n")},
			{Code: "US", Sort: 1, Body: []byte("vless://11111111-2222-3333-4444-555555555555@us.example.com:443?security=tls&sni=us.example.com&type=ws&path=%2Fws#美国 01\n")},
		},
		Catalog: &hypersync.Catalog{Lists: []hypersync.List{
			{CatalogEntry: hypersync.CatalogEntry{Key: "中国常用域名", Policy: "DIRECT", Enabled: true}, Parsed: &hypersync.RuleList{Suffix: []string{"baidu.com"}}},
			{CatalogEntry: hypersync.CatalogEntry{Key: "streaming", Policy: "PROXY", Enabled: true, Sort: 1}, Parsed: &hypersync.RuleList{Suffix: []string{"netflix.com"}, CIDR: []string{"1.2.3.0/24"}}},
		}},
	}
	p, err := subscription.HyperProfile(subscription.NewHyperSource(nil), feed, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := p.Groups[0].ID
	for _, mode := range []string{"smart", "global", "direct"} {
		i := initial()
		i.Source, i.Mode = p.Source, mode
		data, err := Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true}, Network{Method: "tproxy", Resolver: "1.1.1.1"}, strings.Repeat("x", 32))
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		var cfg struct {
			Proxies []struct {
				Name, Type string
			}
			Providers map[string]struct{ Behavior, Format, Path string } `yaml:"rule-providers"`
			Rules     []string
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Proxies) != 2 || cfg.Proxies[0].Type != "anytls" || cfg.Proxies[1].Type != "vless" {
			t.Fatalf("%s: proxies %+v", mode, cfg.Proxies)
		}
		if mode != "smart" {
			continue
		}
		var lists []string
		for _, r := range cfg.Rules {
			if strings.HasPrefix(r, "RULE-SET,list-") {
				lists = append(lists, r)
			}
		}
		if len(lists) != 3 || !strings.HasSuffix(lists[0], ",DIRECT") || !strings.HasSuffix(lists[1], ","+group) || !strings.HasSuffix(lists[2], ","+group+",no-resolve") {
			t.Fatalf("catalog rules %v (all %v)", lists, cfg.Rules)
		}
		for _, r := range lists {
			name := strings.Split(r, ",")[1]
			if pv, ok := cfg.Providers[name]; !ok || pv.Format != "text" || !strings.HasPrefix(pv.Path, "./rulesets/") {
				t.Fatalf("%s: provider %+v", name, pv)
			}
		}
		if !slices.Contains(cfg.Rules, "RULE-SET,"+dnsDomesticSet+",DIRECT") || cfg.Rules[len(cfg.Rules)-1] != "MATCH,"+group {
			t.Fatalf("mainland or default route missing: %v", cfg.Rules)
		}
	}
}
