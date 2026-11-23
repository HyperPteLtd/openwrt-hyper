package router

import (
	"errors"
	"fmt"
	"go.yaml.in/yaml/v3"
	"maps"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

const Global = "@global"
const Local = "@default"
const TProxyPort = 19762
const MixedPort = 19763
const DNSPort = 19764

type Network struct {
	Family6  bool
	DNS      bool
	Method   string
	Resolver string
	LAN      map[string][]netip.Prefix
	// FakeIP follows DNS protection where the router's own connections to fake addresses are
	// captured too (fw4, RouterSelf); elsewhere the core answers real addresses.
	FakeIP bool
	// RouterSelf takes the router's own connections over as well (hyper.main.router_self).
	RouterSelf bool
	// LogLevel is the core's; empty means warning.
	LogLevel string
	// FailOpen and ChinaDirect are read by the network helper when it applies the takeover;
	// Compile reads ChinaDirect for the fake-ip filter. ChinaDirect needs nftables sets, so it
	// is off on fw3; chinaShortcut adds the mode.
	FailOpen, ChinaDirect bool
	// SideRouter (旁路由): the IPv4 default route leaves through a managed LAN; the network
	// helper then masquerades the flows it forwards direct.
	SideRouter bool
	// Empty lists mean the built-in upstreams.
	Domestic, Foreign []string
}
type DeviceRule struct {
	Address string `json:"address"`
	Target  string `json:"target"`
}
type CustomRule struct {
	Note    string `json:"note,omitempty"`
	ID      string `json:"id"`
	Enabled *bool  `json:"enabled,omitempty"`
	subscription.Rule
}
type Policy struct {
	AutoFailover      *bool        `json:"auto_failover,omitempty"` // unset means on
	DefaultDirect     bool         `json:"default_direct,omitempty"`
	RecordConnections *bool        `json:"record_connections,omitempty"`
	Groups            []LocalGroup `json:"groups,omitempty"`
	Telegram          *Telegram    `json:"telegram,omitempty"`
	Devices           []DeviceRule `json:"devices,omitempty"`
	Rules             []CustomRule `json:"rules,omitempty"`
}

// Failover reports whether a lost node switches to another one in its group; it is on unless turned off.
func (p Policy) Failover() bool { return p.AutoFailover == nil || *p.AutoFailover }

type obj = map[string]any

func stringOption(m obj, k string) string { s, _ := m[k].(string); return s }

// Native Mihomo names are the stable per-source IDs, independent of display names.
func coreTarget(s string) string {
	switch s {
	case subscription.Direct:
		return "DIRECT"
	case subscription.Reject:
		return "REJECT"
	default:
		return s
	}
}

// Issue is a configuration problem the kernel helper or the compiler names by its object.
type Issue struct {
	Kind   string `json:"kind"`
	Object string `json:"object"`
	Reason string `json:"reason"`
}

func (i Issue) Error() string {
	if i.Object == "" {
		return i.Reason
	}
	return i.Object + ": " + i.Reason
}

// incompatible reports a node whose protocol this core release does not support.
func incompatible(n subscription.Node, c kernel.TargetCapabilities) bool {
	return c.Control == kernel.Controller && subscription.CapabilityAvailability(n.CapabilityKey(), &c.Naive, c.ProtocolSupport) == "unsupported"
}

// unchain adds to unsupported the nodes that connect through a proxy the core will not have:
// one missing from the profile or unsupported here, or a group without a usable member.
// Groups skip them like any unsupported member.
func unchain(p subscription.Profile, unsupported map[string]bool) {
	known := map[string]bool{subscription.Direct: true, subscription.Reject: true}
	for _, n := range p.Nodes {
		known[n.ID] = !n.Special
	}
	for _, g := range p.Groups {
		known[g.ID] = true
	}
	down := maps.Clone(unsupported)
	usable := func(id string) bool { return known[id] && !down[id] }
	for changed := true; changed; {
		changed = false
		for _, n := range p.Nodes {
			if via := n.Dialer(); via != "" && !down[n.ID] && !usable(via) {
				down[n.ID], unsupported[n.ID], changed = true, true, true
			}
		}
		for _, g := range p.Groups {
			if !down[g.ID] && !slices.ContainsFunc(g.Members, usable) {
				down[g.ID], changed = true, true
			}
		}
	}
}

// chainGroups lists, in profile order, the groups that usable chained nodes connect through
// and the groups inside them: modes that compile no subscription groups still need these.
func chainGroups(p subscription.Profile, unsupported map[string]bool) []subscription.Group {
	byID := map[string]subscription.Group{}
	for _, g := range p.Groups {
		byID[g.ID] = g
	}
	need := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if g, ok := byID[id]; ok && !need[id] {
			need[id] = true
			for _, m := range g.Members {
				visit(m)
			}
		}
	}
	for _, n := range p.Nodes {
		if !n.Special && !unsupported[n.ID] {
			visit(n.Dialer())
		}
	}
	var out []subscription.Group
	for _, g := range p.Groups {
		if need[g.ID] {
			out = append(out, g)
		}
	}
	return out
}

var digestName = regexp.MustCompile("^[a-f0-9]{64}$")

const dnsDomesticSet = "hyper-dns-cn"
const domesticIPSet = "hyper-ip-cn"
const realIPSet = "hyper-real-ip"
const proxiedCNSet = "hyper-cn-proxied"
const routedSet = "hyper-routed"

// profileSet moves a subscription's rule set out of the hyper- names, which belong to the
// built-in and device sets, so that no subscription can replace or collide with them.
func profileSet(tag string) string {
	if strings.HasPrefix(tag, "hyper-") {
		return "hyper-sub-" + tag
	}
	return tag
}

var ruleSetRef = regexp.MustCompile(`(?i)(RULE-SET\s*,\s*)([^,()]+)`)

// mainlandSets maps the tags GEOSITE,CN and GEOIP,CN rules name in a profile to the built-in
// sets of the same lists. The core loads every rule provider on its own, so a list under two
// names would be read and kept twice.
var mainlandSets = map[string]string{subscription.DomesticDomainSet: dnsDomesticSet, subscription.DomesticIPSet: domesticIPSet}

// routedByName lists what a rule matches by name alone: patterns for a domain set, and
// fake-ip filter rules for the other name matches the filter accepts. Conditions on
// addresses, ports or logical combinations are left out. coreSet names a rule set in the core.
func routedByName(r subscription.Rule, behavior map[string]string, coreSet func(string) string) (names, filters []string) {
	add := func(kind, value string) {
		switch kind {
		case "DOMAIN":
			names = append(names, value)
		case "DOMAIN-SUFFIX":
			names = append(names, "+."+value)
		case "DOMAIN-KEYWORD", "DOMAIN-REGEX", "DOMAIN-WILDCARD", "GEOSITE":
			filters = append(filters, kind+","+value+",fake-ip")
		case "RULE-SET":
			// The filter refuses address sets; classical ones match by their name rules.
			if b := behavior[value]; b == "domain" || b == "classical" {
				filters = append(filters, "RULE-SET,"+coreSet(value)+",fake-ip")
			}
		}
	}
	if line, ok := r.Match["clash"].(string); ok {
		if r.Match["src"] != true {
			kind, value, _ := strings.Cut(line, ",")
			add(strings.ToUpper(strings.TrimSpace(kind)), strings.TrimSpace(value))
		}
		return names, filters
	}
	for _, f := range [][2]string{{"domain", "DOMAIN"}, {"domain_suffix", "DOMAIN-SUFFIX"}, {"domain_keyword", "DOMAIN-KEYWORD"}, {"domain_regex", "DOMAIN-REGEX"}, {"rule_set", "RULE-SET"}} {
		values, _ := matchValues(r.Match[f[0]])
		for _, v := range values {
			add(f[1], v)
		}
	}
	return names, filters
}

// unique drops repeats and keeps the first order, so the configuration stays the same.
func unique(list []string) []string {
	seen := map[string]bool{}
	return slices.DeleteFunc(list, func(s string) bool {
		if seen[s] {
			return true
		}
		seen[s] = true
		return false
	})
}

// mainlandRule reports whether a profile rule matches by the mainland lists: the built-in
// sets (mainlandSets), a set or GEOSITE category named as a cn one, or GEOIP,CN.
func mainlandRule(r subscription.Rule) bool {
	if _, ok := mainlandSets[r.RuleSet]; ok {
		return true
	}
	line, _ := r.Match["clash"].(string)
	kind, payload, _ := strings.Cut(line, ",")
	payload = strings.ToLower(strings.TrimSpace(payload))
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "GEOIP":
		return payload == "cn"
	case "GEOSITE", "RULE-SET":
		// cn, geosite-cn, geolocation-cn, category-games@cn; not geolocation-!cn.
		return slices.Contains(strings.FieldsFunc(payload, func(c rune) bool { return c == '-' || c == '_' || c == '@' }), "cn")
	}
	return false
}

// Mainland names whose mainland servers refuse traffic that leaves through a foreign exit,
// which breaks Google Play downloads and updates (OpenClash #5074). They come from OpenClash's
// default China route pass list and are handled as foreign names: they resolve abroad, keep
// fake addresses under the China shortcut and, in smart mode, take the default route.
var proxiedCN = []string{"+.googleapis.cn", "+.xn--ngstr-lra8j.com"}

// Mainland names and addresses keep QUIC. The address check uses no-resolve: a connection
// that only names its host matches by name, without a lookup for the rule.
// quicProxied refuses QUIC to mainland names the proxy must carry; records.js labels it.
var quicProxied = "AND,((NETWORK,UDP),(DST-PORT,443),(RULE-SET," + proxiedCNSet + ")),REJECT"

var quicBlock = "AND,((NETWORK,UDP),(DST-PORT,443),(NOT,((RULE-SET," + dnsDomesticSet + "))),(NOT,((RULE-SET," + domesticIPSet + ",no-resolve)))),REJECT"

// Time sync names resolve over plain DNS sent straight out, not over encrypted DNS through
// the proxy: with a badly wrong clock, TLS fails and the clock could never be corrected.
var ntpNames = []string{
	"+.ntp.org", "*.ntp.org.cn", "*.time.edu.cn", "time.*.com", "time.*.gov", "time.*.edu.cn", "time.*.apple.com", "time-ios.apple.com",
	"time1.*.com", "time2.*.com", "time3.*.com", "time4.*.com", "time5.*.com", "time6.*.com", "time7.*.com",
	"ntp.*.com", "ntp1.*.com", "ntp2.*.com", "ntp3.*.com", "ntp4.*.com", "ntp5.*.com", "ntp6.*.com", "ntp7.*.com", "time1.cloud.tencent.com",
}

// Names that must keep real addresses, following OpenClash's filter: local and reserved
// names, time sync, connectivity checks, STUN and consoles, carrier one-tap login,
// VoWiFi, router setup pages, Windows domain controllers, Apple software update and iCloud
// Private Relay, whose Safari "Hide IP" otherwise fails Cloudflare challenges (OpenClash #4370).
var fakeIPFilter = slices.Concat([]string{
	"*.lan", "*.local", "*.localdomain", "*.localhost", "*.home.arpa", "*.example", "*.invalid", "*.test", "localhost.ptlink",
	"localhost.ptlogin2.qq.com", "localhost.sec.qq.com", "localhost.*.weixin.qq.com",
}, ntpNames, []string{
	"+.msftconnecttest.com", "+.msftncsi.com", "connectivitycheck.gstatic.com", "captive.apple.com", "detectportal.firefox.com", "network-test.debian.org",
	"stun.*.*", "stun.*.*.*", "+.stun.*.*", "+.stun.*.*.*", "+.stun.*.*.*.*", "stun.l.google.com", "global.stun.twilio.com", "global.turn.twilio.com",
	"+.srv.nintendo.net", "+.cdn.nintendo.net", "+.stun.playstation.net", "xbox.*.microsoft.com", "xbox.*.*.microsoft.com", "*.*.xboxlive.com", "xnotify.xboxlive.com",
	"+.battle.net", "+.battlenet.com.cn", "+.uu.163.com",
	"*.cmpassport.com", "id6.me", "open.e.189.cn", "opencloud.wostore.cn", "id.mail.wo.cn", "mdn.open.wo.cn", "hmrz.wo.cn", "nishub1.10010.com", "enrichgw.10010.com", "*.wosms.cn",
	"+.pub.3gppnetwork.org",
	"*.router.asus.com", "*.linksys.com", "*.linksyssmartwifi.com", "heartbeat.belkin.com",
	"PDC._msDCS.*.*", "DC._msDCS.*.*", "GC._msDCS.*.*",
	"mesu.apple.com", "swscan.apple.com", "swquery.apple.com", "swdownload.apple.com", "swcdn.apple.com", "swdist.apple.com",
	"mask.icloud.com", "mask-h2.icloud.com", "mask.apple-dns.net",
	"local.adguard.org", "injections.adguard.org", "resolver1.opendns.com",
	"probe.hyper.invalid",
})

// dnsPolicy lists nameserver-policy entries in the order the core tries them, which a map
// would lose to sorting. Time sync names come first and query plain resolvers through
// DIRECT, which also keeps them off respect-rules and the proxy. Mainland names the proxy
// must carry resolve abroad, ahead of the mainland set that may list them.
func dnsPolicy(resolver string, domestic, foreign []string) *yaml.Node {
	var plain []string
	for _, s := range []string{resolver, "223.5.5.5", "119.29.29.29"} {
		if a, err := netip.ParseAddr(s); err == nil && !a.IsLoopback() && !a.IsUnspecified() {
			plain = append(plain, "udp://"+netip.AddrPortFrom(a, 53).String()+"#DIRECT")
		}
	}
	str := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	policy := &yaml.Node{Kind: yaml.MappingNode}
	for _, e := range []struct {
		match   string
		servers []string
	}{{strings.Join(ntpNames, ","), plain}, {"rule-set:" + proxiedCNSet, foreign}, {"rule-set:" + dnsDomesticSet, domestic}} {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, s := range e.servers {
			list.Content = append(list.Content, str(s))
		}
		policy.Content = append(policy.Content, str(e.match), list)
	}
	return policy
}

func Compile(i Intent, p subscription.Profile, c kernel.TargetCapabilities, n Network, secret string) ([]byte, error) {
	if p.SchemaVersion != subscription.SchemaVersion || i.Source == "" || i.Source != p.Source {
		return nil, errors.New("no authorized primary source")
	}
	if c.Control != kernel.Controller {
		return nil, errors.New("unsupported core control interface")
	}
	if len(secret) < 16 {
		return nil, errors.New("missing control secret")
	}
	if !validMode(i.Mode) {
		return nil, errors.New("unknown routing mode")
	}
	if n.Method != "tproxy" && n.Method != "tun" && n.Method != "none" {
		return nil, errors.New("unknown interception method")
	}
	if n.Method == "tun" && !c.TUN || n.Method == "tproxy" && !c.TProxy {
		return nil, errors.New("selected release lacks the configured interception method")
	}
	domesticDNS, foreignDNS := n.Domestic, n.Foreign
	if len(domesticDNS) == 0 {
		domesticDNS = DomesticDNS
	}
	if len(foreignDNS) == 0 {
		foreignDNS = ForeignDNS
	}
	// The WAN resolver is optional (see networkOptions): the public pair alone bootstraps the core.
	defaultDNS := []string{"223.5.5.5", "119.29.29.29"}
	if resolver, err := netip.ParseAddr(n.Resolver); err == nil && !resolver.IsLoopback() && !resolver.IsUnspecified() {
		defaultDNS = append([]string{resolver.String()}, defaultDNS...)
	}
	config := obj{"mode": "rule", "mixed-port": MixedPort, "allow-lan": false, "external-controller": "127.0.0.1:19761", "secret": secret, "ipv6": n.Family6,
		"log-level": logLevel(n.LogLevel), "profile": obj{"store-selected": false, "store-fake-ip": false}, "geodata-mode": true, "geo-auto-update": false,
		"hosts": obj{"probe.hyper.invalid": "192.0.2.1"}, "dns": obj{"enable": true, "listen": "", "enhanced-mode": "redir-host", "use-hosts": true,
			"use-system-hosts": false, "ipv6": false, "default-nameserver": defaultDNS,
			// As in OpenClash, the core answers no AAAA records even with IPv6 proxying on: under
			// fake-ip, AAAA lookups for proxied names otherwise wait on the remote resolver and
			// stall browsers, and devices fall back to IPv4 that always works. Native IPv6
			// traffic is still taken over and the core still dials IPv6 destinations.
			// Foreign names resolve over encrypted DNS through the proxy; mainland
			// names and node hostnames use domestic resolvers so CDNs stay local.
			"nameserver": foreignDNS, "proxy-server-nameserver": domesticDNS, "direct-nameserver": domesticDNS, "respect-rules": true,
			"nameserver-policy": dnsPolicy(n.Resolver, domesticDNS, foreignDNS), "fake-ip-range": "172.30.254.1/30"},
		"find-process-mode": "off", "tcp-concurrent": true, "unified-delay": true, "keep-alive-interval": 15, "keep-alive-idle": 600}
	// With DNS protection the sniffed domain only steers the rules and the connection still goes
	// to the address the device chose, as OpenClash's sniffer has it (override-destination
	// false). A device's own proxy app dials its node by address with a TLS or QUIC name that is
	// often a disguise (REALITY, Hysteria2 and TUIC with a borrowed SNI); dialling that name
	// would reach the disguise site and break the device's tunnel, while DNS protection already
	// hands devices fake addresses the core maps back to their names. Without it devices resolve
	// through the ISP, blocked names come back poisoned and only the sniffed name reaches the
	// real site, so the core dials that name instead.
	config["sniffer"] = obj{"enable": true, "override-destination": !n.DNS, "parse-pure-ip": true, "force-dns-mapping": true,
		// Push and smart-home services break when their destination is rewritten.
		"skip-domain": []string{"+.push.apple.com", "Mijia Cloud", "+.mi.com", "+.xiaomi.net", "dlg.io.mi.com", "+.oray.com", "+.sunlogin.net"},
		// The router's own programs on the mixed port name their destination or pin a checked
		// address (the subscription fallback); their sniffed names must not steer the rules.
		"skip-src-address": []string{"127.0.0.0/8", "::1/128"},
		"sniff":            obj{"TLS": obj{"ports": []string{"443", "8443"}}, "HTTP": obj{"ports": []string{"80", "8080-8880"}}, "QUIC": obj{"ports": []string{"443"}}}}
	// quic-go's UDP segmentation offload breaks Hysteria2 and TUIC nodes on OpenWrt 24.10
	// kernels (apernet/hysteria#1233); OpenClash turns it off as well.
	config["experimental"] = obj{"quic-go-disable-gso": true}
	if n.DNS {
		dns := config["dns"].(obj)
		dns["listen"] = "127.0.0.1:19764"
		if n.FakeIP {
			dns["enhanced-mode"] = "fake-ip"
			dns["fake-ip-range"] = "198.18.0.1/16"
			dns["fake-ip-filter"] = fakeIPFilter
			config["profile"].(obj)["store-fake-ip"] = true
		}
	}
	// Built-in listeners share a bind address. Keep the diagnostic SOCKS/HTTP
	// socket loopback-only, and bind only the dedicated transparent socket to LAN.
	var listeners []obj
	if n.Method == "tproxy" {
		host := "0.0.0.0"
		if n.Family6 {
			host = "::"
		}
		listeners = append(listeners, obj{"name": "lan-tproxy", "type": "tproxy", "listen": host, "port": TProxyPort, "udp": true})
	} else if n.Method == "tun" {
		tun := obj{"enable": true, "device": "owtun0", "stack": "system", "auto-route": false, "auto-redirect": false, "auto-detect-interface": false, "dns-hijack": []string{}, "inet6-address": []string{}}
		if n.Family6 {
			tun["inet6-address"] = []string{"fd7e:5745:4952::1/126"}
		}
		config["tun"] = tun
	}
	proxies := []obj{}
	available := map[string]bool{subscription.Direct: true, subscription.Reject: true}
	targets := maps.Clone(available)
	foreign := map[string]bool{}
	for _, ref := range i.Policy.refs() {
		if ref.Source != i.Source {
			foreign[ref.ID] = true
		}
	}
	unsupported := map[string]bool{}
	for _, node := range p.Nodes {
		if !node.Special && incompatible(node, c) {
			unsupported[node.ID] = true
		}
	}
	unchain(p, unsupported)
	var nodes []string
	for _, node := range p.Nodes {
		if node.Special || unsupported[node.ID] {
			continue
		}
		if node.ID == "" || available[node.ID] || node.Outbound == nil {
			return nil, errors.New("invalid or duplicate node identity")
		}
		proxy := maps.Clone(node.Outbound)
		proxy["name"] = node.ID
		proxies = append(proxies, proxy)
		available[node.ID] = true
		if !foreign[node.ID] {
			nodes = append(nodes, node.ID)
		}
	}
	if len(nodes) == 0 {
		return nil, errors.New("no compatible general-purpose nodes on this release")
	}
	config["proxies"] = proxies
	groups := []obj{}
	addGroup := func(id, kind string, members []string, opts obj) error {
		native := map[string]string{"selector": "select", "urltest": "url-test", "fallback": "fallback", "loadbalance": "load-balance", "load-balance": "load-balance"}[kind]
		if native == "" {
			return fmt.Errorf("unsupported group type %s", kind)
		}
		usable := []string{}
		for _, member := range members {
			if unsupported[member] {
				continue
			}
			if !available[member] {
				return fmt.Errorf("member unavailable in %s", id)
			}
			usable = append(usable, member)
		}
		if len(usable) == 0 {
			return Issue{"group", id, "no compatible members on this device"}
		}
		g := obj{"type": native, "name": id}
		for _, k := range []string{"url", "interval", "tolerance", "lazy", "timeout", "max-failed-times", "expected-status", "strategy", "disable-udp", "hidden", "icon"} {
			if v, ok := opts[k]; ok {
				g[k] = v
			}
		}
		if raw, ok := g["interval"].(string); ok {
			if d, e := time.ParseDuration(raw); e == nil && d > 0 && d%time.Second == 0 {
				g["interval"] = int64(d / time.Second)
			}
		}
		if native == "select" {
			saved := i.Choices[i.Source][id]
			if saved == "" {
				saved = stringOption(opts, "default")
			}
			if saved != "" {
				pos := slices.Index(usable, saved)
				if pos < 0 {
					return Issue{"group", id, "saved member unavailable on this device"}
				}
				g["default-selected"] = coreTarget(saved)
			}
		}
		for j, v := range usable {
			usable[j] = coreTarget(v)
		}
		g["proxies"] = usable
		groups = append(groups, g)
		targets[id] = true
		available[id] = true
		return nil
	}
	end := p.Final
	compiled := p.Groups
	if i.Mode == "global" || p.Local {
		end = Local
		if i.Mode == "global" {
			end = Global
		}
		if err := addGroup(end, "selector", nodes, nil); err != nil {
			return nil, err
		}
		// Chained nodes still connect through their groups.
		compiled = chainGroups(p, unsupported)
	}
	for _, g := range compiled {
		if available[g.ID] || g.ID == "" {
			return nil, errors.New("duplicate group identity")
		}
		available[g.ID] = true
	}
	for _, g := range compiled {
		if err := addGroup(g.ID, g.Kind, g.Members, g.Options); err != nil {
			return nil, err
		}
	}
	if end == "" && len(p.Groups) > 0 {
		end = p.Groups[0].ID
	}
	if i.Mode == "smart" {
		for _, g := range i.Policy.Groups {
			if available[g.ID] {
				return nil, errors.New("duplicate local group identity")
			}
			var members []string
			for _, ref := range g.Members {
				members = append(members, ref.ID)
			}
			if err := addGroup(g.ID, "selector", members, nil); err != nil {
				return nil, err
			}
		}
	}
	config["proxy-groups"] = groups
	var rules []string
	if t := i.Policy.Telegram; t != nil {
		relay, extra, err := telegram(*t, p, n)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, relay)
		rules = append(rules, extra...)
	}
	if len(listeners) > 0 {
		config["listeners"] = listeners
	}
	for _, prefix := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "::1/128", "fc00::/7", "fe80::/10"} {
		rules = append(rules, "IP-CIDR,"+prefix+",DIRECT,no-resolve")
	}
	providers := map[string]obj{}
	var exceptions []string
	for _, d := range i.Policy.Devices {
		if i.Mode == "global" && d.Target != subscription.Direct {
			continue
		}
		match, err := deviceMatch(d.Address)
		if err != nil {
			return nil, err
		}
		var cond, src string
		if mac, ok := match["source_mac_address"].(string); ok {
			name := macProviderName(mac)
			providers[name] = obj{"type": "file", "format": "text", "behavior": "ipcidr", "path": "./devices/" + name + ".txt"}
			cond = "RULE-SET," + name
			src = ",src"
		} else {
			cond = "SRC-IP-CIDR," + stringOption(match, "source_ip_cidr")
		}
		if d.Target == "@proxy" {
			exceptions = append(exceptions, "NOT,(("+cond+src+"))")
			continue
		}
		if !targets[d.Target] {
			return nil, fmt.Errorf("unavailable device target %s", d.Target)
		}
		rules = append(rules, cond+","+coreTarget(d.Target)+src)
	}
	if i.Mode == "smart" && i.Policy.DefaultDirect && n.Method != "none" {
		conditions := append([]string{"IN-TYPE," + strings.ToUpper(n.Method)}, exceptions...)
		rules = append(rules, logical("AND", conditions)+",DIRECT")
	}
	// As in OpenClash, QUIC that may be proxied is refused so browsers fall back to TCP, where
	// sniffing and routing are reliable. Direct devices and mainland destinations keep QUIC,
	// and so does other UDP on port 443 to the mainland (WireGuard, DTLS). Mainland names the
	// proxy must carry count as foreign.
	rules = append(rules, quicProxied, quicBlock)
	// The only providers of the mainland lists: DNS policy, the QUIC rule, the fake-ip filter and
	// every mainland routing rule (mainlandSets) use these two.
	providers[dnsDomesticSet] = obj{"type": "file", "format": "mrs", "behavior": "domain", "path": chinaList(cnlist.Domains)}
	providers[domesticIPSet] = obj{"type": "file", "format": "mrs", "behavior": "ipcidr", "path": chinaList(cnlist.Addresses)}
	providers[proxiedCNSet] = obj{"type": "inline", "behavior": "domain", "payload": proxiedCN}
	// With the China shortcut the firewall sends mainland addresses straight out. As with
	// OpenClash's China route, mainland names then get real addresses from the domestic
	// resolvers, so their traffic also leaves in the kernel instead of through the core.
	// Names the proxy must carry, and names that rules other than the mainland lists send
	// anywhere but DIRECT (a group may switch between direct and a proxy at any time), keep fake
	// addresses so the core still routes them. Device routes cannot be kept this way: the filter sees only the name, not
	// which device asks, so a device sent to a proxy still reaches mainland names directly.
	shortcut := n.DNS && n.FakeIP && chinaShortcut(i, n)
	var routedNames, routedRules []string
	if i.Mode == "smart" {
		// A rule names a profile's set by its tag; moved holds the sets the core knows by another name.
		moved := map[string]string{}
		behavior := map[string]string{subscription.DomesticDomainSet: "domain", subscription.DomesticIPSet: "ipcidr"}
		for _, set := range p.BinarySets {
			if !digestName.MatchString(set.SHA256) || filepath.Base(set.Filename()) != set.Filename() {
				return nil, errors.New("invalid rule provider digest")
			}
			format := set.FileFormat()
			if !slices.Contains([]string{"yaml", "mrs", "text"}, format) {
				return nil, errors.New("unsupported rule provider format")
			}
			name := profileSet(set.Tag)
			if _, exists := providers[name]; exists {
				return nil, errors.New("rule provider name collision")
			}
			if name != set.Tag {
				moved[set.Tag] = name
			}
			behavior[set.Tag] = set.Behavior
			providers[name] = obj{"type": "file", "format": format, "behavior": set.Behavior, "path": "./rulesets/" + set.Filename()}
		}
		// GEOSITE,CN and GEOIP,CN rules use the built-in sets, as a local profile's mainland routes
		// do, so each mainland list loads once. A set the profile declares under that tag is its own.
		for tag, name := range mainlandSets {
			if _, declared := providers[tag]; !declared {
				moved[tag] = name
			}
		}
		coreSet := func(tag string) string {
			if name, ok := moved[tag]; ok {
				return name
			}
			return tag
		}
		addRule := func(r subscription.Rule) error {
			if !targets[r.Target] {
				return fmt.Errorf("unavailable rule target %s", r.Target)
			}
			// The mainland lists themselves are what the shortcut switch takes over, even when a
			// profile sends them to a group (ACL4SSR-style "全球直连"); other rules keep their names.
			if shortcut && r.Target != subscription.Direct && !mainlandRule(r) {
				names, filters := routedByName(r, behavior, coreSet)
				routedNames, routedRules = append(routedNames, names...), append(routedRules, filters...)
			}
			line, err := nativeRule(r.Match, coreTarget(r.Target))
			// Only a line that names a rule set can follow a moved one; skipping the regex for the
			// rest keeps compiling subscriptions of many thousand rules cheap on slow routers.
			if err == nil && strings.Contains(strings.ToUpper(line), "RULE-SET") {
				// References follow the sets that moved, inside logical rules too.
				line = ruleSetRef.ReplaceAllStringFunc(line, func(ref string) string {
					m := ruleSetRef.FindStringSubmatch(ref)
					if name, ok := moved[strings.TrimSpace(m[2])]; ok {
						return m[1] + name
					}
					return ref
				})
			}
			if err == nil {
				rules = append(rules, line)
			}
			return err
		}
		for _, r := range i.Policy.Rules {
			if r.Enabled != nil && !*r.Enabled {
				continue
			}
			if err := addRule(r.Rule); err != nil {
				return nil, err
			}
		}
		// Mainland names the proxy must carry take the default route like foreign names, just
		// ahead of the first profile rule that would send them out as mainland names, or of the
		// default route; the profile's own rules before that still decide. A direct default
		// leaves them to the profile's rules.
		play := ""
		if end != subscription.Direct && end != subscription.Reject && targets[end] {
			play = "RULE-SET," + proxiedCNSet + "," + coreTarget(end)
		}
		placePlay := func() {
			if play != "" {
				rules, play = append(rules, play), ""
			}
		}
		if p.Local {
			placePlay()
			rules = append(rules, "RULE-SET,"+dnsDomesticSet+",DIRECT", "RULE-SET,"+domesticIPSet+",DIRECT")
		} else {
			for _, r := range p.Rules {
				if mainlandRule(r) {
					placePlay()
				}
				if err := addRule(r); err != nil {
					return nil, err
				}
			}
		}
		placePlay()
	}
	if shortcut {
		providers[realIPSet] = obj{"type": "inline", "behavior": "domain", "payload": fakeIPFilter}
		filter := []string{"RULE-SET," + realIPSet + ",real-ip", "RULE-SET," + proxiedCNSet + ",fake-ip"}
		if routedNames = unique(routedNames); len(routedNames) > 0 {
			providers[routedSet] = obj{"type": "inline", "behavior": "domain", "payload": routedNames}
			filter = append(filter, "RULE-SET,"+routedSet+",fake-ip")
		}
		dns := config["dns"].(obj)
		dns["fake-ip-filter-mode"] = "rule"
		dns["fake-ip-filter"] = append(append(filter, unique(routedRules)...), "RULE-SET,"+dnsDomesticSet+",real-ip", "MATCH,fake-ip")
	}
	if !targets[end] {
		return nil, errors.New("default route has no available group")
	}
	config["rule-providers"] = providers
	config["rules"] = append(rules, "MATCH,"+coreTarget(end))
	return yaml.Marshal(config)
}
