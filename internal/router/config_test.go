package router

import (
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"go.yaml.in/yaml/v3"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type profiles map[string]subscription.Profile

func (s profiles) Load(id string) (subscription.Profile, error) {
	p, ok := s[id]
	if !ok {
		return p, errors.New("revoked")
	}
	return p, nil
}
func baseProfile(t *testing.T, source string) subscription.Profile {
	t.Helper()
	p, e := subscription.Parse(source, []byte("http://127.0.0.1:3210#same\nhttp://127.0.0.1:3211#second"))
	if e != nil {
		t.Fatal(e)
	}
	p.Local = true
	return p
}
func configForTest(t *testing.T, i Intent, p subscription.Profile, n Network) map[string]any {
	t.Helper()
	cap := kernel.TargetCapabilities{Control: "mihomo-rest-v1", TProxy: true, TUN: true}
	b, e := Compile(i, p, cap, n, strings.Repeat("s", 32))
	if e != nil {
		t.Fatal(e)
	}
	var c map[string]any
	if e = yaml.Unmarshal(b, &c); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestSourceIdentityModesAndOrderedPolicy(t *testing.T) {
	p, q := baseProfile(t, "alpha"), baseProfile(t, "beta")
	i := initial()
	i.Source = p.Source
	g := LocalGroup{ID: "local:video", Name: "Video", Members: []NodeRef{{q.Source, q.Nodes[0].ID}}}
	i.Policy.Groups = []LocalGroup{g}
	i.Policy.Devices = []DeviceRule{{"192.168.10.2/32", "@direct"}}
	no := false
	i.Policy.Rules = []CustomRule{{ID: "off", Enabled: &no, Rule: subscription.Rule{Match: obj{"domain": "off.example"}, Target: "@reject"}}, {ID: "on", Rule: subscription.Rule{Match: obj{"domain": "on.example"}, Target: g.ID}}}
	i.Choices = map[string]map[string]string{p.Source: {Global: p.Nodes[1].ID, Local: p.Nodes[0].ID}}
	if ValidatePolicy(i.Policy) != nil {
		t.Fatal("valid policy rejected")
	}
	if _, e := LoadProfile(i, profiles{p.Source: p}); e == nil {
		t.Fatal("revoked cross-source reference accepted")
	}
	mismatched := i
	mismatched.Policy.Groups = []LocalGroup{{ID: g.ID, Name: g.Name, Members: []NodeRef{
		{q.Source, q.Nodes[0].ID}, {p.Source, q.Nodes[0].ID},
	}}}
	if _, e := LoadProfile(mismatched, profiles{p.Source: p, q.Source: q}); e == nil {
		t.Fatal("foreign node accepted as a member of the primary source")
	}
	merged, e := LoadProfile(i, profiles{p.Source: p, q.Source: q})
	if e != nil {
		t.Fatal(e)
	}
	n := Network{Method: "tproxy", Resolver: "192.0.2.53", Family6: true}
	smart := configForTest(t, i, merged, n)
	b, _ := yaml.Marshal(smart)
	if strings.Contains(string(b), "off.example") || !strings.Contains(string(b), "on.example") {
		t.Fatal("disabled rule still active")
	}
	tags := map[string]bool{}
	for _, v := range smart["proxy-groups"].([]any) {
		x := v.(map[string]any)
		tag := x["name"].(string)
		tags[tag] = true
		if tag == Local {
			for _, id := range x["proxies"].([]any) {
				if id == q.Nodes[0].ID {
					t.Fatal("other subscription entered default group")
				}
			}
		}
	}
	if tags[Global] || !tags[g.ID] {
		t.Fatal("wrong smart groups")
	}
	i.Mode = "global"
	global := configForTest(t, i, merged, n)
	for _, v := range global["proxy-groups"].([]any) {
		x := v.(map[string]any)
		if x["name"] == g.ID {
			t.Fatal("smart group in global mode")
		}
		if x["name"] == Global && x["default-selected"] != p.Nodes[1].ID {
			t.Fatal("global selection lost")
		}
	}
	if global["rules"].([]any)[len(global["rules"].([]any))-1] != "MATCH,"+Global {
		t.Fatal("global default incorrect")
	}
}
func TestTelegramLANScope(t *testing.T) {
	p := baseProfile(t, "p")
	mt := subscription.Node{ID: "node:mtfixture", Protocol: "mtproxy", Special: true, Server: "relay.example", Port: 443}
	p.Nodes = append(p.Nodes, mt)
	i := initial()
	i.Mode = "global"
	i.Source = "p"
	i.Policy.Telegram = &Telegram{Node: NodeRef{"p", mt.ID}, Interface: "br-lan", Address: netip.MustParseAddr("192.168.3.1"), Port: 23456, Allow: []netip.Prefix{netip.MustParsePrefix("192.168.3.0/24")}}
	n := Network{Method: "tproxy", Resolver: "192.0.2.53", LAN: map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.3.1/24")}}}
	c := configForTest(t, i, p, n)
	for _, v := range c["proxy-groups"].([]any) {
		x := v.(map[string]any)
		if x["name"] == Global && slices.Contains(x["proxies"].([]any), any(mt.ID)) {
			t.Fatal("Telegram became a general exit")
		}
	}
	rules := c["rules"].([]any)
	if rules[0] != "AND,((IN-NAME,telegram-lan),(SRC-IP-CIDR,192.168.3.0/24)),DIRECT" || rules[1] != "IN-NAME,telegram-lan,REJECT" {
		t.Fatal("Telegram allowlist missing")
	}
	i.Policy.Telegram.Address = netip.MustParseAddr("203.0.113.1")
	if _, _, e := telegram(*i.Policy.Telegram, p, n); e == nil {
		t.Fatal("WAN address accepted")
	}
}

func TestProbeConfigurationPreservesTargetsAndPolicy(t *testing.T) {
	p := baseProfile(t, "primary")
	unsupported, err := subscription.Parse(p.Source, []byte("nowhere://0102030405060708090a0b0c0d0e0f10@198.18.0.33:443?net=udp&morph=1#NW3"))
	if err != nil || len(unsupported.Nodes) != 1 {
		t.Fatal(err)
	}
	node := unsupported.Nodes[0]
	p.Nodes = append(p.Nodes, node)
	members := []string{p.Nodes[0].ID, node.ID}
	p.Local = false
	const group = "group:0123456789abcdef0123456789abcdef"
	p.Groups = []subscription.Group{{ID: group, Kind: "selector", Members: members}}
	p.Final = group
	p.Rules = []subscription.Rule{{Match: obj{"domain_suffix": "test.invalid"}, Target: group}}
	i := initial()
	i.Source = p.Source
	i.Choices = map[string]map[string]string{p.Source: {group: node.ID}}
	i.Policy.Devices = []DeviceRule{{"192.168.3.4/32", group}}

	network := Network{Method: "tproxy", Resolver: "192.0.2.53", LAN: map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.3.1/24")}}}
	data, err := Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true}, network, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []struct{ Name, Type string }
		Groups  []struct {
			Name    string
			Default string `yaml:"default-selected"`
			Proxies []string
		} `yaml:"proxy-groups"`
		Rules []string
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(cfg.Proxies, func(p struct{ Name, Type string }) bool { return p.Name == node.ID && p.Type == "nowhere" }) {
		t.Fatal("unchecked node omitted")
	}
	if len(cfg.Groups) != 1 || cfg.Groups[0].Name != group || !slices.Equal(cfg.Groups[0].Proxies, members) || cfg.Groups[0].Default != node.ID {
		t.Fatal("lost selector or saved choice", cfg.Groups)
	}
	if !slices.Contains(cfg.Rules, "SRC-IP-CIDR,192.168.3.4/32,"+group) || !slices.Contains(cfg.Rules, "DOMAIN-SUFFIX,test.invalid,"+group) {
		t.Fatal("lost device or domain routing policy", cfg.Rules)
	}
	declared := kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true, ProtocolSupport: map[string]bool{"nowhere": false}}
	_, err = Compile(i, p, declared, network, strings.Repeat("x", 32))
	var issue Issue
	if !errors.As(err, &issue) || issue.Kind != "group" || issue.Object != group || !strings.Contains(issue.Reason, "saved member unavailable") {
		t.Fatal("declared capability mismatch was not identified", err)
	}
}

func TestRejectSelectableInImportedPolicyGroup(t *testing.T) {
	p, err := subscription.Parse("fixture", []byte(`{"proxies":[{"type":"http","name":"Edge","server":"example.org","port":80}],"proxy-groups":[{"name":"Ads","type":"select","proxies":["REJECT","Edge","DIRECT"]},{"name":"Auto","type":"url-test","interval":3600,"proxies":["Edge"]}],"rules":["DOMAIN,ad.example,Ads","MATCH,DIRECT"]}`))
	if err != nil || len(p.Notices) != 0 {
		t.Fatal(err, p.Notices)
	}
	i := initial()
	i.Source = p.Source
	data, err := Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true}, Network{Method: "tproxy", Resolver: "1.1.1.1"}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Groups []struct {
			Name, Type string
			Proxies    []string
			Interval   int
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Groups) != 2 || !slices.Equal(cfg.Groups[0].Proxies, []string{"REJECT", p.Nodes[0].ID, "DIRECT"}) || cfg.Groups[1].Type != "url-test" || cfg.Groups[1].Interval != 3600 {
		t.Fatal("lost native group selection or interval", cfg.Groups)
	}
}

func TestSubscriptionSetsCannotTakeBuiltInNames(t *testing.T) {
	p := baseProfile(t, "fixture")
	p.Local = false
	const group = "group:main"
	p.Groups = []subscription.Group{{ID: group, Kind: "selector", Members: []string{p.Nodes[0].ID}}}
	p.Final = group
	p.BinarySets = []subscription.BinarySet{
		{Tag: domesticIPSet, Format: "mrs", Behavior: "domain", SHA256: strings.Repeat("c", 64)},
		{Tag: dnsDomesticSet, Format: "mrs", Behavior: "domain", SHA256: strings.Repeat("d", 64)},
	}
	p.Rules = []subscription.Rule{
		{Match: obj{"clash": "RULE-SET," + domesticIPSet}, RuleSet: domesticIPSet, Target: group},
		{Match: obj{"clash": "AND,((RULE-SET," + dnsDomesticSet + "),(NETWORK,TCP))"}, Target: group},
	}
	i := initial()
	i.Source = p.Source
	c := configForTest(t, i, p, Network{Method: "tproxy", Resolver: "192.0.2.53"})
	providers := c["rule-providers"].(map[string]any)
	own := func(name string) string { return providers[name].(map[string]any)["path"].(string) }
	if own(domesticIPSet) != "./builtin/geoip-cn.mrs" || own(dnsDomesticSet) != "./builtin/geosite-cn.mrs" ||
		own("hyper-sub-"+domesticIPSet) != "./rulesets/"+p.BinarySets[0].Filename() || own("hyper-sub-"+dnsDomesticSet) != "./rulesets/"+p.BinarySets[1].Filename() {
		t.Fatal(providers)
	}
	rules := c["rules"].([]any)
	if !slices.Contains(rules, any("RULE-SET,hyper-sub-"+domesticIPSet+","+group)) || !slices.Contains(rules, any("AND,((RULE-SET,hyper-sub-"+dnsDomesticSet+"),(NETWORK,TCP)),"+group)) {
		t.Fatal(rules)
	}
}

func TestDeclaredDomesticRuleSetDoesNotDuplicateBundledDefinition(t *testing.T) {
	p := baseProfile(t, "fixture")
	p.BinarySets = []subscription.BinarySet{{Tag: subscription.DomesticIPSet, Format: "mrs", Behavior: "ipcidr", SHA256: strings.Repeat("b", 64)}}
	p.Rules = []subscription.Rule{{Match: map[string]any{"rule_set": subscription.DomesticIPSet}, RuleSet: subscription.DomesticIPSet, Target: subscription.Direct}}
	p.Local = false
	p.Final = subscription.Direct
	i := initial()
	i.Source = p.Source
	data, err := Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true}, Network{Method: "tproxy", Resolver: "1.1.1.1"}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers map[string]struct{ Path, Format, Behavior string } `yaml:"rule-providers"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	def := cfg.Providers[subscription.DomesticIPSet]
	delete(cfg.Providers, dnsDomesticSet)
	delete(cfg.Providers, domesticIPSet)
	delete(cfg.Providers, proxiedCNSet)
	if len(cfg.Providers) != 1 || def.Path != "./rulesets/"+p.BinarySets[0].Filename() || def.Format != "mrs" || def.Behavior != "ipcidr" {
		t.Fatal(cfg.Providers)
	}
}

func TestDevicePolicyAndUnsupportedNaive(t *testing.T) {
	p := baseProfile(t, "fixture")
	p.Local = false
	group := subscription.Group{ID: "group:main", Kind: "selector", Members: []string{p.Nodes[0].ID, "node:naive"}}
	p.Groups = []subscription.Group{group}
	p.Final = group.ID
	p.Nodes = append(p.Nodes, subscription.Node{ID: "node:naive", Protocol: "naive", Outbound: obj{"type": "naive", "server": "127.0.0.1", "port": 3040}})
	i := initial()
	i.Source = p.Source
	i.Policy.DefaultDirect = true
	i.Policy.Devices = []DeviceRule{{Address: "10.0.0.2", Target: "@proxy"}, {Address: "02:01:01:02:03:04", Target: subscription.Reject}}
	n := Network{Method: "tun", DNS: true, Family6: true, Resolver: "2001:db8::53"}
	c := kernel.TargetCapabilities{Control: kernel.Controller, TUN: true, Naive: false}
	data, err := Compile(i, p, c, n, strings.Repeat("q", 32))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []struct{ Name string }
		Groups  []struct{ Proxies []string } `yaml:"proxy-groups"`
		Tun     struct {
			Enable    bool
			Device    string
			AutoRoute bool `yaml:"auto-route"`
		}
		Providers map[string]obj `yaml:"rule-providers"`
		Rules     []string
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Tun.Enable || cfg.Tun.Device != "owtun0" || cfg.Tun.AutoRoute || len(cfg.Proxies) != 2 || !slices.Equal(cfg.Groups[0].Proxies, []string{p.Nodes[0].ID}) {
		t.Fatal("unexpected TUN settings or incompatible node")
	}
	name := macProviderName("02:01:01:02:03:04")
	if cfg.Providers[name]["path"] != "./devices/"+name+".txt" || !slices.Contains(cfg.Rules, "RULE-SET,"+name+",REJECT,src") || !slices.Contains(cfg.Rules, "AND,((IN-TYPE,TUN),(NOT,((SRC-IP-CIDR,10.0.0.2/32)))),DIRECT") {
		t.Fatal(cfg.Rules)
	}
	i.Choices = map[string]map[string]string{p.Source: {group.ID: "node:naive"}}
	if _, err := Compile(i, p, c, n, strings.Repeat("q", 32)); err == nil {
		t.Fatal("silent change to saved unsupported member")
	}
}

func TestCandidateCannotReplaceLiveDeviceAddresses(t *testing.T) {
	i := initial()
	i.Mode = "global"
	i.Policy.Devices = []DeviceRule{{"aa:bb:cc:dd:ee:01", subscription.Direct}, {"aa:bb:cc:dd:ee:02", "@proxy"}}
	macs := policyMACs(i)
	if !slices.Equal(macs, []string{"aa:bb:cc:dd:ee:01"}) {
		t.Fatal(macs)
	}
	dir := t.TempDir()
	if err := ensureDeviceFiles(dir, macs); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, macProviderName(macs[0])+".txt")
	data := []byte("192.0.2.11/32\n")
	if err := writeDeviceProvider(file, data); err != nil {
		t.Fatal(err)
	}
	if err := ensureDeviceFiles(dir, macs); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != string(data) {
		t.Fatal("candidate changed the active DHCP policy", err)
	}
}

func TestDNSSplitsDomesticAndProxiedUpstreams(t *testing.T) {
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	c := configForTest(t, i, p, Network{Method: "tproxy", Resolver: "192.0.2.53", LAN: map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.3.1/24")}}})
	dns := c["dns"].(map[string]any)
	if dns["respect-rules"] != true || dns["nameserver-policy"].(map[string]any)["rule-set:"+dnsDomesticSet] == nil || c["rule-providers"].(map[string]any)[dnsDomesticSet] == nil {
		t.Fatal(dns)
	}
	if sn := c["sniffer"].(map[string]any); sn["parse-pure-ip"] != true || !slices.Contains(c["rules"].([]any), any(quicBlock)) {
		t.Fatal("sniffer must parse bare addresses, or QUIC block missing")
	}
	// UDP 443 to mainland addresses is not refused, and the address check never resolves a name.
	cnIP := c["rule-providers"].(map[string]any)[domesticIPSet].(map[string]any)
	if quicBlock != "AND,((NETWORK,UDP),(DST-PORT,443),(NOT,((RULE-SET,hyper-dns-cn))),(NOT,((RULE-SET,hyper-ip-cn,no-resolve)))),REJECT" ||
		cnIP["behavior"] != "ipcidr" || cnIP["path"] != "./builtin/geoip-cn.mrs" {
		t.Fatal("QUIC block refuses mainland addresses", quicBlock, cnIP)
	}
	if c["experimental"].(map[string]any)["quic-go-disable-gso"] != true {
		t.Fatal("quic-go GSO left on")
	}
	// A checked address the router's own programs send through the mixed port stays pinned.
	if skip := c["sniffer"].(map[string]any)["skip-src-address"]; !slices.Equal(skip.([]any), []any{"127.0.0.0/8", "::1/128"}) {
		t.Fatal("loopback clients are sniffed", skip)
	}
	off, on := false, true
	a, b := Preferences{BypassCN: &on}, Preferences{BypassCN: &off}
	if a.SameNetwork(b) || !a.SameNetwork(Preferences{}) {
		t.Fatal("China IP switch must restart only when it changes")
	}
	if (Preferences{RouterSelf: &on}).SameNetwork(Preferences{RouterSelf: &off}) || (Preferences{LogLevel: "info"}).SameNetwork(Preferences{LogLevel: "debug"}) ||
		!(Preferences{LogLevel: "info"}).SameNetwork(Preferences{}) {
		t.Fatal("the router's own traffic and the log level apply when they change")
	}
	lan := map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.3.1/24")}}
	if c := configForTest(t, i, p, Network{Method: "tproxy", LogLevel: "debug", LAN: lan}); c["log-level"] != "debug" {
		t.Fatal("log level", c["log-level"])
	}
	if c := configForTest(t, i, p, Network{Method: "tproxy", LAN: lan}); c["log-level"] != "warning" {
		t.Fatal("default log level", c["log-level"])
	}
	base := Preferences{Method: "tproxy", Networks: []string{"lan"}}
	for _, bad := range []Preferences{{UpdateAt: ptr(24)}, {UpdateAt: ptr(-2)}, {LogLevel: "loud"}} {
		bad.Method, bad.Networks = base.Method, base.Networks
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if UpdateHour("4") != 4 || UpdateHour("") != -1 || UpdateHour("04") != -1 || UpdateHour("24") != -1 {
		t.Fatal("refresh hour")
	}
}

func ptr[T any](v T) *T { return &v }

func TestFakeIPFollowsDNSProtection(t *testing.T) {
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	n := Network{Method: "tproxy", FakeIP: true, Resolver: "192.0.2.53", LAN: map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.3.1/24")}}}
	if c := configForTest(t, i, p, n); c["dns"].(map[string]any)["enhanced-mode"] != "redir-host" {
		t.Fatal("fake-ip without DNS protection")
	}
	n.DNS = true
	c := configForTest(t, i, p, n)
	dns := c["dns"].(map[string]any)
	if dns["enhanced-mode"] != "fake-ip" {
		t.Fatal("fake-ip missing")
	}
	for _, name := range []string{"mask.icloud.com", "mask-h2.icloud.com", "mask.apple-dns.net"} {
		if !slices.Contains(dns["fake-ip-filter"].([]any), any(name)) {
			t.Fatal("iCloud Private Relay gets a fake address:", name)
		}
	}
}

func TestDNSUpstreamValidation(t *testing.T) {
	for _, v := range []string{"https://dns.google/dns-query", "tls://1.1.1.1", "223.5.5.5", "2001:db8::1"} {
		if !validUpstream(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"", "https://x/'y", "a b", "ftp://x"} {
		if validUpstream(v) {
			t.Fatal(v)
		}
	}
	if got := upstreams("bad'x tls://9.9.9.9", DomesticDNS); !slices.Equal(got, []string{"tls://9.9.9.9"}) {
		t.Fatal(got)
	}
}

func TestChinaShortcutGivesMainlandNamesRealAddresses(t *testing.T) {
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	n := Network{Method: "tproxy", DNS: true, FakeIP: true, ChinaDirect: true, Resolver: "192.0.2.53"}
	c := configForTest(t, i, p, n)
	dns := c["dns"].(map[string]any)
	filter := []any{"RULE-SET," + realIPSet + ",real-ip", "RULE-SET," + proxiedCNSet + ",fake-ip", "RULE-SET," + dnsDomesticSet + ",real-ip", "MATCH,fake-ip"}
	fixed := c["rule-providers"].(map[string]any)[realIPSet].(map[string]any)
	if dns["fake-ip-filter-mode"] != "rule" || !slices.Equal(dns["fake-ip-filter"].([]any), filter) ||
		fixed["type"] != "inline" || fixed["behavior"] != "domain" || len(fixed["payload"].([]any)) != len(fakeIPFilter) {
		t.Fatal(dns["fake-ip-filter-mode"], dns["fake-ip-filter"], fixed)
	}
	// Without the shortcut mainland names keep fake addresses: global mode, or the switch off.
	i.Mode = "global"
	global := configForTest(t, i, p, n)
	i.Mode = "smart"
	n.ChinaDirect = false
	for _, c := range []map[string]any{global, configForTest(t, i, p, n)} {
		dns := c["dns"].(map[string]any)
		if dns["fake-ip-filter-mode"] != nil || len(dns["fake-ip-filter"].([]any)) != len(fakeIPFilter) || c["rule-providers"].(map[string]any)[realIPSet] != nil {
			t.Fatal(dns["fake-ip-filter-mode"], dns["fake-ip-filter"])
		}
	}
}

func TestChinaShortcutKeepsRuledNamesFake(t *testing.T) {
	p := baseProfile(t, "p")
	p.Local = false
	const hk = "group:hk"
	p.Groups = []subscription.Group{{ID: hk, Kind: "selector", Members: []string{p.Nodes[0].ID, subscription.Direct}}}
	p.Final = hk
	p.BinarySets = []subscription.BinarySet{
		{Tag: "media", Format: "mrs", Behavior: "domain", SHA256: strings.Repeat("a", 64)},
		{Tag: "ips", Format: "mrs", Behavior: "ipcidr", SHA256: strings.Repeat("b", 64)},
		{Tag: "hyper-mix", Format: "text", Behavior: "classical", SHA256: strings.Repeat("c", 64)},
	}
	rule := func(clash, target string) subscription.Rule {
		r := subscription.Rule{Match: obj{"clash": clash}, Target: target}
		if set, ok := strings.CutPrefix(clash, "RULE-SET,"); ok {
			r.RuleSet = set
		}
		return r
	}
	p.Rules = []subscription.Rule{
		rule("DOMAIN-SUFFIX,bilibili.com", hk), rule("DOMAIN,a.example.cn", hk), rule("DOMAIN-KEYWORD,bili", hk), rule("DOMAIN-SUFFIX,direct.cn", subscription.Direct),
		rule("RULE-SET,media", hk), rule("RULE-SET,ips", hk), rule("RULE-SET,hyper-mix", subscription.Reject), rule("GEOSITE,bilibili", hk),
		rule("AND,((DOMAIN,x.cn),(NETWORK,TCP))", hk), rule("DOMAIN-SUFFIX,bilibili.com", subscription.Reject),
		// The mainland lists sent to a group (ACL4SSR's 全球直连) are what the shortcut takes over.
		rule("GEOSITE,CN", hk), rule("GEOSITE,geolocation-cn", hk),
	}
	i := initial()
	i.Source = p.Source
	i.Policy.Rules = []CustomRule{
		{ID: "a", Rule: subscription.Rule{Match: obj{"domain_suffix": []any{"douyin.com", "bilibili.com"}}, Target: hk}},
		{ID: "b", Rule: subscription.Rule{Match: obj{"domain": "z.cn", "port": 443}, Target: subscription.Reject}},
		{ID: "c", Rule: subscription.Rule{Match: obj{"domain": "d.cn"}, Target: subscription.Direct}},
	}
	n := Network{Method: "tproxy", DNS: true, FakeIP: true, ChinaDirect: true, Resolver: "192.0.2.53"}
	c := configForTest(t, i, p, n)
	routed := c["rule-providers"].(map[string]any)[routedSet].(map[string]any)
	if !slices.Equal(routed["payload"].([]any), []any{"+.douyin.com", "+.bilibili.com", "z.cn", "a.example.cn"}) || routed["type"] != "inline" || routed["behavior"] != "domain" {
		t.Fatal(routed)
	}
	filter := []any{"RULE-SET," + realIPSet + ",real-ip", "RULE-SET," + proxiedCNSet + ",fake-ip", "RULE-SET," + routedSet + ",fake-ip",
		"DOMAIN-KEYWORD,bili,fake-ip", "RULE-SET,media,fake-ip", "RULE-SET,hyper-sub-hyper-mix,fake-ip", "GEOSITE,bilibili,fake-ip",
		"RULE-SET," + dnsDomesticSet + ",real-ip", "MATCH,fake-ip"}
	if got := c["dns"].(map[string]any)["fake-ip-filter"].([]any); !slices.Equal(got, filter) {
		t.Fatal(got)
	}
	// Without the shortcut nothing is kept back.
	i.Mode = "global"
	if c = configForTest(t, i, p, n); c["rule-providers"].(map[string]any)[routedSet] != nil {
		t.Fatal("names kept fake without the shortcut")
	}
}

// dnsPolicyEntries reads nameserver-policy in document order, the order the core tries it.
func dnsPolicyEntries(t *testing.T, data []byte) (keys []string, servers [][]string) {
	t.Helper()
	var cfg struct {
		DNS struct {
			Policy yaml.Node `yaml:"nameserver-policy"`
		} `yaml:"dns"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for j := 0; j+1 < len(cfg.DNS.Policy.Content); j += 2 {
		var list []string
		if err := cfg.DNS.Policy.Content[j+1].Decode(&list); err != nil {
			t.Fatal(err)
		}
		keys, servers = append(keys, cfg.DNS.Policy.Content[j].Value), append(servers, list)
	}
	return keys, servers
}

func TestTimeSyncNamesUsePlainDNS(t *testing.T) {
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	data, err := Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true}, Network{Method: "tproxy", DNS: true, FakeIP: true, Resolver: "2001:db8::53"}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	// Time sync names come before the mainland set, which may list them, and skip the proxy.
	keys, servers := dnsPolicyEntries(t, data)
	last := len(keys) - 1
	if keys[0] != strings.Join(ntpNames, ",") || keys[last] != "rule-set:"+dnsDomesticSet ||
		!slices.Equal(servers[0], []string{"udp://[2001:db8::53]:53#DIRECT", "udp://223.5.5.5:53#DIRECT", "udp://119.29.29.29:53#DIRECT"}) || !slices.Equal(servers[last], DomesticDNS) {
		t.Fatal(keys, servers)
	}
	for _, name := range ntpNames {
		if !slices.Contains(fakeIPFilter, name) {
			t.Fatal("time sync name gets a fake address:", name)
		}
	}
	// Without a WAN resolver the public ones remain.
	b, _ := yaml.Marshal(obj{"dns": obj{"nameserver-policy": dnsPolicy("", DomesticDNS, ForeignDNS)}})
	if _, servers = dnsPolicyEntries(t, b); !slices.Equal(servers[0], []string{"udp://223.5.5.5:53#DIRECT", "udp://119.29.29.29:53#DIRECT"}) {
		t.Fatal(servers)
	}
}

func TestGooglePlayNamesTakeTheProxy(t *testing.T) {
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	i.Policy.Rules = []CustomRule{{ID: "mine", Rule: subscription.Rule{Match: obj{"domain": "play.googleapis.cn"}, Target: subscription.Direct}}}
	n := Network{Method: "tproxy", DNS: true, FakeIP: true, ChinaDirect: true, Resolver: "192.0.2.53", Foreign: []string{"tls://9.9.9.9"}}
	cap := kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true}
	data, err := Compile(i, p, cap, n, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	// They resolve abroad, ahead of the mainland set that may list them.
	keys, servers := dnsPolicyEntries(t, data)
	if !slices.Equal(keys, []string{strings.Join(ntpNames, ","), "rule-set:" + proxiedCNSet, "rule-set:" + dnsDomesticSet}) || !slices.Equal(servers[1], n.Foreign) {
		t.Fatal(keys, servers)
	}
	var cfg struct {
		Providers map[string]obj `yaml:"rule-providers"`
		Rules     []string
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	set := cfg.Providers[proxiedCNSet]
	if set["type"] != "inline" || set["behavior"] != "domain" || !slices.Equal(set["payload"].([]any), []any{"+.googleapis.cn", "+.xn--ngstr-lra8j.com"}) {
		t.Fatal(set)
	}
	// Their QUIC is refused like foreign QUIC, and they take the default route after the
	// user's rules but ahead of the mainland ones.
	quic := slices.Index(cfg.Rules, "AND,((NETWORK,UDP),(DST-PORT,443),(RULE-SET,"+proxiedCNSet+")),REJECT")
	user, route := slices.Index(cfg.Rules, "DOMAIN,play.googleapis.cn,DIRECT"), slices.Index(cfg.Rules, "RULE-SET,"+proxiedCNSet+","+Local)
	if quic < 0 || quic > slices.Index(cfg.Rules, quicBlock) || user < 0 || route < user || route > slices.Index(cfg.Rules, "RULE-SET,"+dnsDomesticSet+",DIRECT") {
		t.Fatal(cfg.Rules)
	}
	// Global mode proxies everything anyway, and a direct default leaves them to the profile.
	i.Mode = "global"
	global := configForTest(t, i, p, n)
	p.Local, p.Final = false, subscription.Direct
	i.Mode = "smart"
	for _, c := range []map[string]any{global, configForTest(t, i, p, n)} {
		for _, r := range c["rules"].([]any) {
			if strings.HasPrefix(r.(string), "RULE-SET,"+proxiedCNSet+",") {
				t.Fatal(r)
			}
		}
	}
}

func TestGooglePlayRuleKeepsTheProfilesOwnRules(t *testing.T) {
	p := baseProfile(t, "p")
	p.Local = false
	const proxy, google = "group:proxy", "group:google"
	p.Groups = []subscription.Group{{ID: proxy, Kind: "selector", Members: []string{p.Nodes[0].ID}}, {ID: google, Kind: "selector", Members: []string{p.Nodes[1].ID}}}
	p.Final = proxy
	p.Rules = []subscription.Rule{
		{Match: obj{"clash": "GEOSITE,google"}, Target: google},
		{Match: obj{"clash": "RULE-SET," + subscription.DomesticDomainSet}, RuleSet: subscription.DomesticDomainSet, Target: subscription.Direct},
		{Match: obj{"clash": "DOMAIN-SUFFIX,example.cn"}, Target: subscription.Direct},
	}
	i := initial()
	i.Source = p.Source
	n := Network{Method: "tproxy", Resolver: "192.0.2.53"}
	play := "RULE-SET," + proxiedCNSet + "," + proxy
	// After the profile's Google group, ahead of its mainland list.
	rules := configForTest(t, i, p, n)["rules"].([]any)
	if at := slices.Index(rules, any(play)); at < 1 || rules[at-1] != "GEOSITE,google,"+google || rules[at+1] != "RULE-SET,"+dnsDomesticSet+",DIRECT" {
		t.Fatal(rules)
	}
	// Without mainland rules, just ahead of the default route.
	p.Rules = p.Rules[:1]
	if rules = configForTest(t, i, p, n)["rules"].([]any); rules[len(rules)-2] != play {
		t.Fatal(rules)
	}
	for line, want := range map[string]bool{"GEOSITE,geolocation-cn": true, "GEOSITE,category-games@cn": true, "GEOSITE,geolocation-!cn": false, "GEOIP,CN": true, "GEOIP,US": false,
		"RULE-SET,geoip-cn": true, "RULE-SET,cncidr": false, "DOMAIN-SUFFIX,cn": false} {
		if mainlandRule(subscription.Rule{Match: obj{"clash": line}}) != want {
			t.Fatal(line)
		}
	}
}

// With DNS protection a device's own proxy app reaches its node: the sniffed name steers rules but
// is not dialled. Without it devices may hold poisoned addresses, so the core dials the name.
func TestSnifferDialsTheNameOnlyWithoutDNSProtection(t *testing.T) {
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	lan := map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.3.1/24")}}
	for _, dns := range []bool{true, false} {
		c := configForTest(t, i, p, Network{Method: "tproxy", Resolver: "192.0.2.53", DNS: dns, LAN: lan})
		if got := c["sniffer"].(map[string]any)["override-destination"]; got != !dns {
			t.Fatalf("DNS protection %v: override-destination %v", dns, got)
		}
	}
}
