package router

import (
	"slices"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"go.yaml.in/yaml/v3"
)

func TestChainsCompileByIDInEveryMode(t *testing.T) {
	p, err := subscription.Parse("chains", []byte(`proxies:
 - {name: A, type: http, server: a.example, port: 80}
 - {name: B, type: http, server: b.example, port: 80}
 - {name: NW, type: nowhere, server: nw.example, port: 443, password: `+strings.Repeat("a", 32)+`}
 - {name: Exit, type: http, server: exit.example, port: 80, dialer-proxy: Front}
 - {name: Hop, type: http, server: hop.example, port: 80, dialer-proxy: NW}
 - {name: Deep, type: http, server: deep.example, port: 80, dialer-proxy: Hop}
proxy-groups:
 - {name: Front, type: select, proxies: [A, B]}
 - {name: Main, type: select, proxies: [Exit, Deep, A]}
rules:
 - MATCH,Main`))
	if err != nil || len(p.Notices) != 0 {
		t.Fatal(err, p.Notices)
	}
	id := map[string]string{}
	for _, n := range p.Nodes {
		id[n.Name] = n.ID
	}
	for _, g := range p.Groups {
		id[g.Name] = g.ID
	}
	// NW is unsupported on this release, so the chains through it cannot run either.
	caps := kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true, ProtocolSupport: map[string]bool{"nowhere": false}}
	compile := func(i Intent, p subscription.Profile) (map[string]map[string]any, map[string][]string) {
		t.Helper()
		data, err := Compile(i, p, caps, Network{Method: "tproxy", Resolver: "192.0.2.53"}, strings.Repeat("x", 32))
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Proxies []map[string]any
			Groups  []struct {
				Name    string
				Proxies []string
			} `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		proxies, groups := map[string]map[string]any{}, map[string][]string{}
		for _, x := range cfg.Proxies {
			proxies[x["name"].(string)] = x
		}
		for _, g := range cfg.Groups {
			groups[g.Name] = g.Proxies
		}
		return proxies, groups
	}
	i := initial()
	i.Source = p.Source
	proxies, groups := compile(i, p)
	if proxies[id["Exit"]]["dialer-proxy"] != id["Front"] || proxies[id["Hop"]] != nil || proxies[id["Deep"]] != nil || proxies[id["NW"]] != nil {
		t.Fatal("chains compiled wrongly", proxies)
	}
	if !slices.Equal(groups[id["Main"]], []string{id["Exit"], id["A"]}) || !slices.Equal(groups[id["Front"]], []string{id["A"], id["B"]}) {
		t.Fatal("groups kept a broken chain", groups)
	}
	i.Mode = "global"
	proxies, groups = compile(i, p)
	if proxies[id["Exit"]]["dialer-proxy"] != id["Front"] || groups[id["Front"]] == nil || groups[id["Main"]] != nil || !slices.Equal(groups[Global], []string{id["A"], id["B"], id["Exit"]}) {
		t.Fatal("global mode lost the group a chain connects through", groups)
	}
	// A local fallback keeps no subscription groups: a chain through one cannot run.
	i.Mode = "smart"
	p.Local, p.Groups, p.Rules, p.Final = true, nil, nil, ""
	proxies, groups = compile(i, p)
	if proxies[id["Exit"]] != nil || !slices.Equal(groups[Local], []string{id["A"], id["B"]}) {
		t.Fatal("local fallback compiled a chain without its group", groups)
	}
}
