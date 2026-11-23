package router

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// Reload closes open connections exactly when routingKey changes.
func TestSwitchingRoutingRulesResetsConnectionsButRefreshesDoNot(t *testing.T) {
	i := Intent{Mode: "smart", Source: "s"}
	node := func(server string) subscription.Node {
		return subscription.Node{ID: "node:" + server, Name: "HK 01", Protocol: "trojan", Server: server, Port: 443}
	}
	group := []subscription.Group{{ID: "group:proxy", Name: "Proxy", Kind: "selector", Members: []string{"node:a.example"}}}
	own := subscription.Profile{Source: "s", Format: "clash", Nodes: []subscription.Node{node("a.example")}, Groups: group,
		Rules: []subscription.Rule{{Match: map[string]any{"domain_suffix": []any{"example.com"}}, Target: "group:proxy"}}, Final: "group:proxy"}
	local := own
	local.Local, local.Rules, local.Groups, local.Final = true, nil, nil, ""

	// Same nodes, different rules in effect.
	for _, c := range []struct {
		name     string
		from, to subscription.Profile
	}{{"own to local", own, local}, {"local to own", local, own}} {
		if routingKey(i, c.from) == routingKey(i, c.to) {
			t.Errorf("%s kept open connections on the old route", c.name)
		}
	}

	// Same rules in effect, nodes, plan and counters refreshed.
	refresh := func(p subscription.Profile) subscription.Profile {
		p.Nodes = []subscription.Node{node("b.example"), node("c.example")}
		if !p.Local {
			p.Groups = []subscription.Group{{ID: "group:proxy", Name: "Proxy", Kind: "selector", Members: []string{"node:b.example", "node:c.example"}}}
		}
		p.Notices = []subscription.Notice{{Code: "unsupported_node"}}
		p.Title = "1 GB left"
		return p
	}
	for name, p := range map[string]subscription.Profile{"own": own, "local": local} {
		if routingKey(i, p) != routingKey(i, refresh(p)) {
			t.Errorf("refreshing a subscription on its %s rules closed open connections", name)
		}
	}
}

func TestGlobalModeTakesOverMainlandAddressesToo(t *testing.T) {
	smart, global := Intent{Mode: "smart"}, Intent{Mode: "global"}
	n := Network{Method: "tproxy", ChinaDirect: true}
	if !chinaShortcut(smart, n) || chinaShortcut(global, n) {
		t.Fatal("the China IP shortcut belongs to smart mode only")
	}
	// Switching modes with the shortcut on re-applies the firewall; without it, the core reloads in place.
	if networkKey(smart, n) == networkKey(global, n) {
		t.Fatal("mode switch kept the shortcut")
	}
	n.ChinaDirect = false
	if networkKey(smart, n) != networkKey(global, n) {
		t.Fatal("mode switch without the shortcut forces a restart")
	}
	failOpen := n
	failOpen.FailOpen = true
	if networkKey(smart, n) == networkKey(smart, failOpen) {
		t.Fatal("fault fallback change kept the old takeover")
	}
}

// The network helper is told the family, the China shortcut, the router's role and the core's
// group: it exempts the group the manager launches the core in and resolves none of its own.
func TestApplyPassesTheCoreGroupToTheNetworkHelper(t *testing.T) {
	smart, global := Intent{Mode: "smart"}, Intent{Mode: "global"}
	for _, c := range []struct {
		i    Intent
		n    Network
		gid  int
		want string
	}{
		{smart, Network{}, 65534, "apply ipv4 main-router gid=65534"},
		{smart, Network{Family6: true, ChinaDirect: true}, 65534, "apply ipv6 china main-router gid=65534"},
		{global, Network{ChinaDirect: true, SideRouter: true}, 4242, "apply ipv4 side-router gid=4242"},
		{smart, Network{Family6: true, SideRouter: true}, 99, "apply ipv6 side-router gid=99"},
	} {
		if got := strings.Join(applyArgs(c.i, c.n, c.gid), " "); got != c.want {
			t.Errorf("%+v %+v %d: %q, want %q", c.i, c.n, c.gid, got, c.want)
		}
	}
}

func TestRotatingIPv6PrefixKeepsAnIPv4OnlyTakeover(t *testing.T) {
	lan := func(v6 string) map[string][]netip.Prefix {
		return map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.1.1/24"), netip.MustParsePrefix(v6)}}
	}
	i := Intent{Mode: "smart"}
	before, after := Network{Method: "tproxy", LAN: lan("240e:1::1/64")}, Network{Method: "tproxy", LAN: lan("240e:2::1/64")}
	if networkKey(i, before) != networkKey(i, after) {
		t.Fatal("a new delegated prefix restarted an IPv4-only takeover")
	}
	before.Family6, after.Family6 = true, true
	if networkKey(i, before) == networkKey(i, after) {
		t.Fatal("a new prefix kept the IPv6 takeover on the old one")
	}
	if len(before.LAN["br-lan"]) != 2 {
		t.Fatal("the key changed the network's own prefixes")
	}
}
