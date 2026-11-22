// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package mihomo

import (
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
	"github.com/HyperPteLtd/openwrt-hyper/internal/uci"
)

// fixtureUCI loads tests/fixtures/uci-basic.json (the golden recorder's UCI state).
func fixtureUCI(t *testing.T, edit func(map[string]map[string]any)) *uci.Config {
	t.Helper()
	b, err := os.ReadFile("../../tests/fixtures/uci-basic.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]any
	if err = json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if h, ok := raw["hyper"]; ok && h[".type"] == nil {
		raw = map[string]map[string]any{}
		for k, v := range h {
			raw[k] = v.(map[string]any)
		}
	}
	if edit != nil {
		edit(raw)
	}
	c := &uci.Config{}
	for name, sec := range raw {
		s := &uci.Section{Name: name, Options: map[string]string{}, Lists: map[string][]string{}}
		for k, v := range sec {
			switch x := v.(type) {
			case string:
				if k == ".type" {
					s.Type = x
				} else {
					s.Options[k] = x
				}
			case []any:
				for _, e := range x {
					s.Lists[k] = append(s.Lists[k], e.(string))
				}
			}
		}
		c.Sections = append(c.Sections, s)
	}
	return c
}

func fixtureNodes(t *testing.T) []nodes.Node {
	t.Helper()
	b, err := os.ReadFile("../../tests/fixtures/links.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := nodes.Parse(b, nodes.ParseOptions{Country: "JP", Sort: 10, InsecureDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	// The golden fixture's main node.
	p.Nodes[1].ID = "pp_tokyo"
	sg, _ := nodes.Parse([]byte("anytls://pw@sg2.example.com:443?sni=sg2.example.com#SG"), nodes.ParseOptions{Country: "SG", Sort: 20})
	return append(p.Nodes, sg.Nodes...)
}

type rendered struct {
	out *Output
	cfg map[string]any
}

func (r rendered) rules() []string {
	var out []string
	for _, v := range r.cfg["rules"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

func (r rendered) dns() map[string]any { return r.cfg["dns"].(map[string]any) }

func render(t *testing.T, edit func(map[string]map[string]any), tweak func(*Input)) rendered {
	t.Helper()
	s, err := FromUCI(fixtureUCI(t, edit))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := LoadServerRules("../../tests/fixtures/rules", s.DisabledRules)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Settings: s, Nodes: fixtureNodes(t), Rules: rules, Resolver: "192.168.1.1",
		Secret: "0123456789abcdef0123", DirectList: []string{"bank"}, ProxyList: []string{"google", "bad,DIRECT"},
		Neighbors: map[string][]netip.Addr{"00:d9:d1:8b:33:e2": {netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("fe80::1%br-lan")}}}
	if tweak != nil {
		tweak(&in)
	}
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = yaml.Unmarshal(out.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	checkCore(t, out)
	return rendered{out, cfg}
}

func indexOf(t *testing.T, rules []string, rule string) int {
	t.Helper()
	i := slices.Index(rules, rule)
	if i < 0 {
		t.Fatalf("missing rule %q in\n%s", rule, strings.Join(rules, "\n"))
	}
	return i
}

func TestRenderSmart(t *testing.T) {
	r := render(t, nil, nil)
	groups := r.cfg["proxy-groups"].([]any)
	main := groups[0].(map[string]any)
	if len(groups) != 1 || main["name"] != "main-out" || main["type"] != "select" || main["default-selected"] != "cfg-pp_tokyo-out" {
		t.Fatal(groups)
	}
	members := main["proxies"].([]any)
	if len(members) != 7 || !strings.HasPrefix(members[0].(string), "cfg-pp_") || members[0] == "cfg-pp_tokyo-out" {
		t.Fatalf("members %v (SG sorts first)", members)
	}
	for _, p := range r.cfg["proxies"].([]any) {
		name := p.(map[string]any)["name"].(string)
		if !strings.HasPrefix(name, "cfg-") || !strings.HasSuffix(name, "-out") {
			t.Fatal(name)
		}
	}

	rules := r.rules()
	// Golden order: device bypass, Telegram ranges, UI lists, then server lists in server order.
	dev := indexOf(t, rules, "RULE-SET,hyper-device-00d9d18b33e2,DIRECT,src")
	tg := indexOf(t, rules, "IP-CIDR,91.108.4.0/22,main-out,no-resolve")
	direct := indexOf(t, rules, "DOMAIN-KEYWORD,bank,DIRECT")
	proxy := indexOf(t, rules, "DOMAIN-KEYWORD,google,main-out")
	cn := indexOf(t, rules, "RULE-SET,hyper-rule-cn-direct-domain,DIRECT")
	geo := indexOf(t, rules, "RULE-SET,geoip-cn,DIRECT")
	sd := indexOf(t, rules, "RULE-SET,hyper-rule-streaming-domain,main-out")
	sk := indexOf(t, rules, "RULE-SET,hyper-rule-streaming-keyword,main-out")
	si := indexOf(t, rules, "RULE-SET,hyper-rule-streaming-ip,main-out,no-resolve")
	if !(dev < tg && tg < direct && direct < proxy && proxy < cn && cn < geo && geo < sd && sd < sk && sk < si) {
		t.Fatalf("order\n%s", strings.Join(rules, "\n"))
	}
	// QUIC that may be proxied is refused ahead of every list, after the device rules;
	// Google Play's mainland names are proxied just ahead of the first DIRECT server list.
	play := indexOf(t, rules, "RULE-SET,hyper-cn-proxied,main-out")
	for i, q := range quicRules {
		if at := indexOf(t, rules, q); at != dev+1+i {
			t.Fatalf("QUIC rule %q at %d", q, at)
		}
	}
	if !(proxy < play && play == cn-1) {
		t.Fatalf("Play rule at %d\n%s", play, strings.Join(rules, "\n"))
	}
	if r.cfg["experimental"].(map[string]any)["quic-go-disable-gso"] != true {
		t.Fatal(r.cfg["experimental"])
	}
	if rules[len(rules)-1] != "MATCH,main-out" || slices.ContainsFunc(rules, func(s string) bool { return strings.Contains(s, "off") || strings.Contains(s, "bad") }) {
		t.Fatalf("rules\n%s", strings.Join(rules, "\n"))
	}
	if got := string(r.out.Files["rules/hyper-rule-cn-direct-domain.txt"]); got != "+.baidu.com\n+.qq.com\n" {
		t.Fatalf("%q", got)
	}
	if got := string(r.out.Files["rules/hyper-rule-streaming-keyword.txt"]); got != "DOMAIN-KEYWORD,nflx\n" {
		t.Fatalf("%q", got)
	}
	if got := string(r.out.Files["devices/hyper-device-00d9d18b33e2.txt"]); got != "192.168.1.20/32\nfe80::1/128\n" {
		t.Fatalf("%q", got)
	}

	dns := r.dns()
	// Fake-ip follows interception.
	if dns["listen"] != "127.0.0.1:5333" || dns["enhanced-mode"] != "fake-ip" || dns["fake-ip-range"] != "198.18.0.1/16" || dns["respect-rules"] != true {
		t.Fatal(dns)
	}
	if ns := dns["nameserver"].([]any); len(ns) != 1 || ns[0] != "https://1.1.1.1/dns-query" {
		t.Fatal(ns)
	}
	if ns := dns["proxy-server-nameserver"].([]any); ns[0] != "223.5.5.5" {
		t.Fatal(ns)
	}
	if boot := dns["default-nameserver"].([]any); boot[0] != "192.168.1.1" {
		t.Fatal(boot)
	}
	policy := dns["nameserver-policy"].(map[string]any)
	if policy["rule-set:hyper-rule-cn-direct-domain"].([]any)[0] != "223.5.5.5" ||
		policy["rule-set:hyper-rule-streaming-domain"].([]any)[0] != "https://1.1.1.1/dns-query" ||
		policy["rule-set:geosite-cn"].([]any)[0] != "223.5.5.5" ||
		policy["rule-set:hyper-cn-proxied"].([]any)[0] != "https://1.1.1.1/dns-query" ||
		policy[strings.Join(ntpNames, ",")].([]any)[0] != "udp://192.168.1.1:53#DIRECT" {
		t.Fatal(policy)
	}
	// The core tries the policy in order: time sync, Play, server lists in server order, mainland.
	raw, last := string(r.out.Config), -1
	for _, key := range []string{"+.ntp.org,*.ntp.org.cn", "rule-set:hyper-cn-proxied", "rule-set:hyper-rule-cn-direct-domain", "rule-set:hyper-rule-streaming-domain", "rule-set:geosite-cn:"} {
		at := strings.Index(raw[strings.Index(raw, "nameserver-policy:"):], key)
		if at <= last {
			t.Fatalf("nameserver-policy order at %q\n%s", key, raw)
		}
		last = at
	}
	l := r.cfg["listeners"].([]any)[0].(map[string]any)
	if l["type"] != "tproxy" || l["port"] != 5332 || l["listen"] != "0.0.0.0" || l["udp"] != true {
		t.Fatal(l)
	}
	if r.cfg["tun"] != nil || r.cfg["external-controller"] != "127.0.0.1:5334" || r.cfg["routing-mark"] != 100 || r.cfg["log-level"] != "warning" {
		t.Fatal(r.cfg)
	}
	if !slices.Equal(r.out.Devices, []string{"00:d9:d1:8b:33:e2"}) {
		t.Fatal(r.out.Devices)
	}
}

func TestRenderNoServerRules(t *testing.T) {
	r := render(t, nil, func(in *Input) { in.Rules = nil })
	rules := r.rules()
	if indexOf(t, rules, "RULE-SET,geosite-cn,DIRECT") > indexOf(t, rules, "RULE-SET,geoip-cn,DIRECT") {
		t.Fatal(rules)
	}
}

func TestRenderTUN(t *testing.T) {
	r := render(t, func(u map[string]map[string]any) { u["config"]["proxy_mode"] = "tun" },
		func(in *Input) { in.Settings.IPv6 = true }) // the router has an IPv6 uplink
	tun := r.cfg["tun"].(map[string]any)
	if tun["device"] != "hypertun0" || tun["auto-route"] != false || tun["stack"] != "system" || tun["inet6-address"].([]any)[0] != "fdfe:dcba:9876::1/126" {
		t.Fatal(tun)
	}
	if r.cfg["listeners"] != nil || r.cfg["ipv6"] != true {
		t.Fatal(r.cfg["listeners"])
	}
}

func TestRenderDNSOnly(t *testing.T) {
	r := render(t, func(u map[string]map[string]any) {
		u["config"]["dns_only"] = "1"
	}, nil)
	if r.cfg["listeners"] != nil || r.cfg["tun"] != nil {
		t.Fatal("dns-only intercepts nothing")
	}
	if r.dns()["enhanced-mode"] != "redir-host" {
		t.Fatal("fake-ip needs interception")
	}
	if slices.ContainsFunc(r.rules(), func(rule string) bool { return strings.Contains(rule, "DST-PORT,443") }) {
		t.Fatal("QUIC rejected in DNS-only mode")
	}
	indexOf(t, r.rules(), "RULE-SET,hyper-rule-cn-direct-domain,DIRECT")
}

func TestRenderGlobal(t *testing.T) {
	r := render(t, func(u map[string]map[string]any) { u["config"]["routing_mode"] = "global" }, nil)
	rules := r.rules()
	for _, rule := range rules {
		if slices.Contains(quicRules, rule) {
			continue
		}
		if strings.Contains(rule, "hyper-rule-") || strings.Contains(rule, "KEYWORD") || strings.Contains(rule, "geo") || strings.Contains(rule, "91.108") || strings.Contains(rule, "cn-proxied") {
			t.Fatalf("smart rule in global mode: %s", rule)
		}
	}
	// Foreign QUIC falls back to TCP in global mode too; direct devices keep it.
	if indexOf(t, rules, "RULE-SET,hyper-device-00d9d18b33e2,DIRECT,src") > indexOf(t, rules, quicRules[1]) {
		t.Fatal(rules)
	}
	// Global resolves everything through the proxy, apart from time sync and the Play names.
	if policy := r.dns()["nameserver-policy"].(map[string]any); len(policy) != 2 || policy["rule-set:geosite-cn"] != nil {
		t.Fatal(policy)
	}
}

func TestRenderFakeIPAndListedOnly(t *testing.T) {
	r := render(t, func(u map[string]map[string]any) {
		u["control"]["lan_proxy_mode"] = "listed_only"
		u["control"]["lan_proxy_mac_addrs"] = []any{"AA:BB:CC:DD:EE:01"}
	}, func(in *Input) { in.Self = []netip.Addr{netip.MustParseAddr("192.168.1.1")} })
	if r.dns()["enhanced-mode"] != "fake-ip" || r.dns()["fake-ip-range"] != "198.18.0.1/16" {
		t.Fatal(r.dns())
	}
	indexOf(t, r.rules(), "AND,((IN-TYPE,TPROXY),(NOT,((OR,((RULE-SET,hyper-device-aabbccddee01,src),(RULE-SET,hyper-self,src)))))),DIRECT")
	if string(r.out.Files["devices/self.txt"]) != "192.168.1.1/32\n" || r.out.Files["devices/hyper-device-aabbccddee01.txt"] == nil {
		t.Fatal(r.out.Files)
	}
}

func TestRenderMainNodeFallback(t *testing.T) {
	r := render(t, func(u map[string]map[string]any) { u["config"]["main_node"] = "gone" }, nil)
	main := r.cfg["proxy-groups"].([]any)[0].(map[string]any)
	if main["default-selected"] != main["proxies"].([]any)[0] {
		t.Fatal(main)
	}
}

func TestRenderErrors(t *testing.T) {
	s, _ := FromUCI(fixtureUCI(t, nil))
	base := Input{Settings: s, Nodes: fixtureNodes(t), Resolver: "192.168.1.1", Secret: "0123456789abcdef0123"}
	for name, edit := range map[string]func(*Input){
		"no nodes":    func(in *Input) { in.Nodes = nil },
		"unsupported": func(in *Input) { in.Nodes = []nodes.Node{{ID: "x", Proxy: map[string]any{"type": "naive"}}} },
		"secret":      func(in *Input) { in.Secret = "short" },
		"resolver":    func(in *Input) { in.Resolver = "127.0.0.1" },
		"dns":         func(in *Input) { in.Settings.ForeignDNS = []string{"https://x/\nrules:"} },
	} {
		in := base
		edit(&in)
		if _, err := Render(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFromUCI(t *testing.T) {
	s, err := FromUCI(fixtureUCI(t, func(u map[string]map[string]any) {
		u["config"]["log_level"] = "trace"
		delete(u["infra"], "tun_name")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != ModeSmart || s.Method != MethodTProxy || s.LogLevel != "debug" || s.TunName != "hypertun0" || !s.RouterSelf {
		t.Fatal(s)
	}
	for _, bad := range []func(map[string]map[string]any){
		func(u map[string]map[string]any) { u["config"]["routing_mode"] = "custom" },
		func(u map[string]map[string]any) { u["config"]["proxy_mode"] = "redirect_tproxy" },
		func(u map[string]map[string]any) { u["config"]["routing_mode"] = "bypass_mainland_china" },
		func(u map[string]map[string]any) { u["control"]["lan_direct_mac_addrs"] = []any{"nope"} },
		func(u map[string]map[string]any) { u["infra"]["tproxy_port"] = "70000" },
		func(u map[string]map[string]any) { u["infra"]["tun_name"] = "bad name" },
	} {
		if _, err := FromUCI(fixtureUCI(t, bad)); err == nil {
			t.Error("accepted bad UCI")
		}
	}
}

// checkCore runs `mihomo -t` on the config when HYPER_TEST_MIHOMO names a core binary.
func checkCore(t *testing.T, out *Output) {
	t.Helper()
	bin := os.Getenv("HYPER_TEST_MIHOMO")
	if bin == "" {
		return
	}
	dir := t.TempDir()
	if dump := os.Getenv("HYPER_TEST_DUMP"); dump != "" {
		dir = filepath.Join(dump, t.Name())
		os.MkdirAll(dir, 0o755)
	}
	for name, body := range out.Files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if mrs := os.Getenv("HYPER_TEST_BUILTIN"); mrs != "" {
		os.Symlink(mrs, filepath.Join(dir, "builtin"))
	}
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, out.Config, 0o600)
	if b, err := exec.Command(bin, "-t", "-d", dir, "-f", cfg).CombinedOutput(); err != nil {
		t.Fatalf("mihomo -t: %v\n%s\n%s", err, b, out.Config)
	}
}

// The sniffed domain steers rules but is never dialled (a device's own proxy app reaches its
// node); the core answers no AAAA records.
func TestRenderSnifferAndDNS(t *testing.T) {
	r := render(t, nil, func(in *Input) { in.Settings.IPv6 = true })
	sn := r.cfg["sniffer"].(map[string]any)
	// No forced sniffing: one node selector, so naming streaming hosts would change nothing.
	if sn["override-destination"] != false || sn["parse-pure-ip"] != true || sn["force-domain"] != nil ||
		sn["sniff"].(map[string]any)["HTTP"].(map[string]any)["ports"].([]any)[1] != "8080-8880" {
		t.Fatal(sn)
	}
	if r.dns()["ipv6"] != false || r.cfg["ipv6"] != true {
		t.Fatal("AAAA answered or IPv6 dialling off")
	}
	filter := r.dns()["fake-ip-filter"].([]any)
	if !slices.Contains(filter, any("+.pub.3gppnetwork.org")) || !slices.Contains(filter, any("*.cmpassport.com")) {
		t.Fatal("fake-ip filter misses VoWiFi / carrier login")
	}
	if !slices.Contains(filter, any("mask.icloud.com")) || !slices.Contains(filter, any("time.*.apple.com")) {
		t.Fatal("fake-ip filter misses iCloud Private Relay / time sync")
	}
	if r.cfg["keep-alive-idle"] != 600 || r.cfg["keep-alive-interval"] != 15 {
		t.Fatal(r.cfg["keep-alive-idle"])
	}
}
