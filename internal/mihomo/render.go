// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package mihomo renders the core's config from hyper's settings, nodes and rule lists.
//
// One rule-mode config with a loopback controller, a dedicated transparent listener (TProxy) or a TUN
// device, split DNS (foreign names over encrypted DNS through the proxy, mainland names
// and node hostnames on domestic resolvers), optional fake-ip, and device rules matched
// through per-MAC address lists. Group names stay those of the sing-box era: main-out is
// the node selector and every node is cfg-<id>-out.
package mihomo

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
)

// MainGroup is the node selector every proxied route points at.
const MainGroup = "main-out"

// TProxyListener names the transparent listener (IN-NAME in rules, firewall comments).
const TProxyListener = "hyper-tproxy"

// Proxy types this build of the core has (scripts/mihomo-trim.mjs KEEP_PROXIES): the
// Hyper service sends AnyTLS nodes only.
var Supported = []string{"anytls"}

// Time sync names resolve over plain DNS sent straight out, not over encrypted DNS through
// the proxy: with a badly wrong clock (no RTC), TLS fails and the clock could never be
// corrected.
var ntpNames = []string{
	"+.ntp.org", "*.ntp.org.cn", "*.time.edu.cn", "time.*.com", "time.*.gov", "time.*.edu.cn", "time.*.apple.com", "time-ios.apple.com",
	"time1.*.com", "time2.*.com", "time3.*.com", "time4.*.com", "time5.*.com", "time6.*.com", "time7.*.com",
	"ntp.*.com", "ntp1.*.com", "ntp2.*.com", "ntp3.*.com", "ntp4.*.com", "ntp5.*.com", "ntp6.*.com", "ntp7.*.com", "time1.cloud.tencent.com",
}

// Names that must keep real addresses under fake-ip, following OpenClash's filter:
// local and reserved names, time sync, connectivity checks, STUN and consoles,
// carrier one-tap login, VoWiFi, router setup pages, Windows domain controllers, Apple
// software update and iCloud Private Relay, whose Safari "Hide IP" otherwise fails
// Cloudflare challenges (OpenClash #4370).
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
})

// Mainland names whose mainland servers refuse traffic that leaves through a foreign exit,
// which breaks Google Play downloads and updates (OpenClash #5074). They are
// handled as foreign names: they resolve abroad, never take the QUIC exemption and, in
// smart mode, go through the proxy ahead of the mainland lists that also contain them.
var proxiedCN = []string{"+.googleapis.cn", "+.xn--ngstr-lra8j.com"}

const proxiedCNSet = "hyper-cn-proxied"

// As in OpenClash, QUIC that may be proxied is refused so browsers fall back
// to TCP, where sniffing and routing are reliable. Direct devices (their rules come first) and
// mainland names and addresses keep QUIC, as does other UDP on port 443 to the mainland
// (WireGuard, DTLS). The address check uses no-resolve: a connection that only names its
// host matches by name, without a lookup for the rule.
var quicRules = []string{
	"AND,((NETWORK,UDP),(DST-PORT,443),(RULE-SET," + proxiedCNSet + ")),REJECT",
	"AND,((NETWORK,UDP),(DST-PORT,443),(NOT,((RULE-SET," + geoSiteCN + "))),(NOT,((RULE-SET," + geoIPCN + ",no-resolve)))),REJECT",
}

var privateCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16",
	"100.64.0.0/10", "224.0.0.0/4", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"}

// Built-in mainland lists shipped in the package (core dir ./builtin/).
const (
	geoIPCN   = "geoip-cn"
	geoSiteCN = "geosite-cn"
)

// Input is everything a render depends on; Render does no I/O.
type Input struct {
	Settings Settings
	Nodes    []nodes.Node
	Rules    []ServerRule // smart mode only
	// Domain keywords from the UI lists (smart mode only).
	DirectList, ProxyList []string
	// Resolver is the WAN DNS server, for "wan" upstreams and bootstrapping DoH hosts.
	Resolver string
	Secret   string
	// Neighbors maps a device MAC to its current addresses (from the neighbour table).
	Neighbors map[string][]netip.Addr
	// Self holds the router's own addresses, exempted from listed_only.
	Self []netip.Addr
}

// Output is the config plus the files it references, relative to the core dir.
type Output struct {
	Config []byte
	Files  map[string][]byte
	// Devices lists the MACs whose address files the manager must keep current.
	Devices []string
}

// ErrNoNodes means there is nothing to route through; sync the subscription first.
var ErrNoNodes = errors.New("no_nodes")

type obj = map[string]any

func Render(in Input) (*Output, error) {
	s := in.Settings
	if s.Mode != ModeSmart && s.Mode != ModeGlobal {
		return nil, fmt.Errorf("unknown mode %q", s.Mode)
	}
	if s.Method != MethodTProxy && s.Method != MethodTUN {
		return nil, fmt.Errorf("unknown interception method %q", s.Method)
	}
	if len(in.Secret) < 16 {
		return nil, errors.New("missing control secret")
	}
	resolver, err := netip.ParseAddr(in.Resolver)
	if err != nil || resolver.IsLoopback() || resolver.IsUnspecified() {
		return nil, errors.New("missing WAN DNS resolver")
	}
	out := &Output{Files: map[string][]byte{}}
	smart := s.Mode == ModeSmart

	foreign, err := upstreams(s.ForeignDNS, resolver)
	if err != nil {
		return nil, err
	}
	domestic, err := upstreams(s.DomesticDNS, resolver)
	if err != nil {
		return nil, err
	}

	// Nodes, highest country sort first, stable within a country.
	var list []nodes.Node
	for _, n := range in.Nodes {
		if slices.Contains(Supported, n.Type()) && n.ID != "" {
			list = append(list, n)
		}
	}
	slices.SortStableFunc(list, func(a, b nodes.Node) int { return b.Sort - a.Sort })
	if len(list) == 0 {
		return nil, ErrNoNodes
	}
	proxies := make([]obj, 0, len(list))
	tags := make([]string, 0, len(list))
	selected := ""
	for _, n := range list {
		p := make(obj, len(n.Proxy)+1)
		for k, v := range n.Proxy {
			p[k] = v
		}
		p["name"] = n.Tag()
		proxies = append(proxies, p)
		tags = append(tags, n.Tag())
		if n.ID == s.MainNode {
			selected = n.Tag()
		}
	}
	if selected == "" {
		selected = tags[0]
	}
	groups := []obj{{"name": MainGroup, "type": "select", "proxies": tags, "default-selected": selected}}

	dns := obj{
		// No AAAA answers, as in OpenClash, even with IPv6 interception on: AAAA lookups for
		// proxied names would wait on the remote resolver and stall browsers; devices fall
		// back to IPv4, which always works. Native IPv6 traffic is still intercepted.
		"enable": true, "listen": fmt.Sprintf("127.0.0.1:%d", s.DNSPort), "ipv6": false,
		"enhanced-mode": "redir-host", "use-hosts": true, "use-system-hosts": false, "respect-rules": true,
		"cache-algorithm": "arc",
		// Bootstrap for DoH/DoT hostnames: the WAN resolver plus public domestic ones.
		"default-nameserver": dedupe([]string{resolver.String(), "223.5.5.5", "119.29.29.29"}),
		// Foreign names over encrypted DNS through the proxy; node hostnames and direct
		// traffic resolve on domestic servers so CDNs stay local.
		"nameserver": foreign, "proxy-server-nameserver": domestic, "direct-nameserver": domestic,
	}
	if s.FakeIP && !s.DNSOnly {
		dns["enhanced-mode"] = "fake-ip"
		dns["fake-ip-range"] = "198.18.0.1/16"
		dns["fake-ip-filter"] = fakeIPFilter
	}
	// nameserver-policy is tried in order (time sync, mainland names the proxy carries,
	// server lists in server order, then the mainland list); a map would be sorted.
	var plain []string
	for _, a := range []string{resolver.String(), "223.5.5.5", "119.29.29.29"} {
		plain = append(plain, "udp://"+netip.AddrPortFrom(netip.MustParseAddr(a), 53).String()+"#DIRECT")
	}
	policy := []policyEntry{{strings.Join(ntpNames, ","), dedupe(plain)}, {"rule-set:" + proxiedCNSet, foreign}}

	config := obj{
		"mode": "rule", "mixed-port": s.MixedPort, "allow-lan": false, "bind-address": "127.0.0.1",
		"ipv6": s.IPv6, "log-level": s.LogLevel, "routing-mark": s.Mark,
		"external-controller": fmt.Sprintf("127.0.0.1:%d", s.ControlPort), "secret": in.Secret,
		"profile":      obj{"store-selected": false, "store-fake-ip": s.FakeIP && !s.DNSOnly},
		"geodata-mode": false, "geo-auto-update": false, "find-process-mode": "off",
		// quic-go's GSO breaks Hysteria2 and TUIC on several router kernels.
		"experimental":   QUICExperimental,
		"tcp-concurrent": true, "unified-delay": true, "keep-alive-interval": 15, "keep-alive-idle": 600,
		"proxies": proxies, "proxy-groups": groups, "dns": dns,
		// The sniffed domain steers the rules, but the connection still goes to the address the
		// device chose, as OpenClash's sniffer has it. A device's own proxy app
		// dials its node by address with a TLS or QUIC name that is often a disguise (REALITY,
		// Hysteria2 and TUIC with a borrowed SNI); dialling that name would reach the disguise
		// site and break the device's tunnel. DNS protection already hands devices fake
		// addresses the core maps back to their names, so poisoned answers rarely get this far.
		"sniffer": obj{"enable": true, "override-destination": false, "parse-pure-ip": true, "force-dns-mapping": true,
			// Push, smart-home and remote-desktop services break when their sniffed names steer them.
			"skip-domain": []string{"+.push.apple.com", "Mijia Cloud", "+.mi.com", "+.xiaomi.net", "dlg.io.mi.com", "+.oray.com", "+.sunlogin.net"},
			"sniff": obj{"TLS": obj{"ports": []string{"443", "8443"}}, "HTTP": obj{"ports": []string{"80", "8080-8880"}},
				"QUIC": obj{"ports": []string{"443"}}}},
	}
	inType := ""
	if !s.DNSOnly {
		if s.Method == MethodTProxy {
			listen := "0.0.0.0"
			if s.IPv6 {
				listen = "::"
			}
			config["listeners"] = []obj{{"name": TProxyListener, "type": "tproxy", "listen": listen, "port": s.TProxyPort, "udp": true}}
			inType = "TPROXY"
		} else {
			tun := obj{"enable": true, "device": s.TunName, "stack": "system", "mtu": s.TunMTU,
				"inet4-address": []string{s.TunAddr4}, "auto-route": false, "auto-redirect": false,
				"auto-detect-interface": false, "dns-hijack": []string{}}
			if s.IPv6 {
				tun["inet6-address"] = []string{s.TunAddr6}
			}
			config["tun"] = tun
			inType = "TUN"
		}
	}

	providers := obj{}
	var rules []string
	for _, p := range privateCIDRs {
		rules = append(rules, "IP-CIDR,"+p+",DIRECT,no-resolve")
	}

	// Devices. except_listed: the listed MACs go direct. listed_only: everything that
	// came in through interception goes direct unless it is a listed MAC or the router.
	device := func(mac string) string {
		name := "hyper-device-" + strings.ReplaceAll(mac, ":", "")
		path := "devices/" + name + ".txt"
		providers[name] = obj{"type": "file", "format": "text", "behavior": "ipcidr", "path": "./" + path}
		out.Files[path] = addrList(in.Neighbors[mac])
		if !slices.Contains(out.Devices, mac) {
			out.Devices = append(out.Devices, mac)
		}
		return "RULE-SET," + name
	}
	switch s.LANMode {
	case LANExceptListed:
		for _, mac := range s.DirectMACs {
			rules = append(rules, device(mac)+",DIRECT,src")
		}
	case LANListedOnly:
		if inType != "" {
			var keep []string
			for _, mac := range s.ProxyMACs {
				keep = append(keep, "("+device(mac)+",src)")
			}
			if s.RouterSelf {
				providers["hyper-self"] = obj{"type": "file", "format": "text", "behavior": "ipcidr", "path": "./devices/self.txt"}
				out.Files["devices/self.txt"] = addrList(in.Self)
				keep = append(keep, "(RULE-SET,hyper-self,src)")
			}
			cond := "IN-TYPE," + inType
			if len(keep) > 0 {
				cond = "AND,((" + cond + "),(NOT,((OR,(" + strings.Join(keep, ",") + ")))))"
			}
			rules = append(rules, cond+",DIRECT")
		}
	}

	builtin := func(tag, behavior string) string {
		providers[tag] = obj{"type": "file", "format": "mrs", "behavior": behavior, "path": "./builtin/" + tag + ".mrs"}
		return "RULE-SET," + tag
	}
	providers[proxiedCNSet] = obj{"type": "inline", "behavior": "domain", "payload": proxiedCN}
	if !s.DNSOnly {
		builtin(geoSiteCN, "domain")
		builtin(geoIPCN, "ipcidr")
		rules = append(rules, quicRules...)
	}

	if smart {
		for _, c := range s.ProxyCIDRs {
			rules = append(rules, "IP-CIDR"+family(c)+","+c+","+MainGroup+",no-resolve")
		}
		for _, k := range in.DirectList {
			if validValue(k) {
				rules = append(rules, "DOMAIN-KEYWORD,"+k+",DIRECT")
			}
		}
		for _, k := range in.ProxyList {
			if validValue(k) {
				rules = append(rules, "DOMAIN-KEYWORD,"+k+","+MainGroup)
			}
		}
		// Google Play's mainland names go through the proxy just ahead of the first list that
		// would send them direct as mainland names; server lists before it still decide.
		play := "RULE-SET," + proxiedCNSet + "," + MainGroup
		placed := false
		for _, r := range in.Rules {
			target, server := MainGroup, foreign
			if r.Direct {
				target, server = "DIRECT", domestic
				if !placed {
					rules, placed = append(rules, play), true
				}
			}
			base := "hyper-rule-" + r.Key
			if body := domainList(r.Domain, r.Suffix); len(body) > 0 {
				tag := base + "-domain"
				providers[tag] = obj{"type": "file", "format": "text", "behavior": "domain", "path": "./rules/" + tag + ".txt"}
				out.Files["rules/"+tag+".txt"] = body
				rules = append(rules, "RULE-SET,"+tag+","+target)
				policy = append(policy, policyEntry{"rule-set:" + tag, server})
			}
			if body := classicalList(r.Keyword, r.Regex); len(body) > 0 {
				tag := base + "-keyword"
				providers[tag] = obj{"type": "file", "format": "text", "behavior": "classical", "path": "./rules/" + tag + ".txt"}
				out.Files["rules/"+tag+".txt"] = body
				rules = append(rules, "RULE-SET,"+tag+","+target)
			}
			if body := cidrList(r.CIDR); len(body) > 0 {
				tag := base + "-ip"
				providers[tag] = obj{"type": "file", "format": "text", "behavior": "ipcidr", "path": "./rules/" + tag + ".txt"}
				out.Files["rules/"+tag+".txt"] = body
				rules = append(rules, "RULE-SET,"+tag+","+target+",no-resolve")
			}
			if r.Direct && r.GeoIPCN {
				rules = append(rules, builtin(geoIPCN, "ipcidr")+",DIRECT")
			}
		}
		// Without server lists (signed out, never synced) keep mainland traffic direct.
		if len(in.Rules) == 0 {
			rules = append(rules, play, builtin(geoSiteCN, "domain")+",DIRECT", builtin(geoIPCN, "ipcidr")+",DIRECT")
		}
		builtin(geoSiteCN, "domain")
		policy = append(policy, policyEntry{"rule-set:" + geoSiteCN, domestic})
	}
	dns["nameserver-policy"] = policyNode(policy)
	config["rule-providers"] = providers
	config["rules"] = append(dedupe(rules), "MATCH,"+MainGroup)
	if out.Config, err = yaml.Marshal(config); err != nil {
		return nil, err
	}
	return out, nil
}

// QUICExperimental turns off quic-go GSO, for every config the core loads (node probes too).
var QUICExperimental = obj{"quic-go-disable-gso": true}

type policyEntry struct {
	match   string
	servers []string
}

// policyNode keeps nameserver-policy in the order the core tries it.
func policyNode(entries []policyEntry) *yaml.Node {
	str := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	node := &yaml.Node{Kind: yaml.MappingNode}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.match] || len(e.servers) == 0 {
			continue
		}
		seen[e.match] = true
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, s := range e.servers {
			list.Content = append(list.Content, str(s))
		}
		node.Content = append(node.Content, str(e.match), list)
	}
	return node
}

var upstreamRe = lazyre.New(`^(?:(?:udp|tcp|tls|https|quic|h3)://)?[A-Za-z0-9.:\[\]_-]+(?::\d+)?(?:/[A-Za-z0-9._~/-]*)?(?:#[A-Za-z0-9._-]+)?$`)

// upstreams validates DNS server addresses and replaces "wan" with the WAN resolver.
func upstreams(list []string, wan netip.Addr) ([]string, error) {
	var out []string
	for _, u := range list {
		if u == "wan" {
			u = wan.String()
		}
		if !upstreamRe().MatchString(u) {
			return nil, fmt.Errorf("invalid DNS server %q", u)
		}
		out = append(out, u)
	}
	return dedupe(out), nil
}

// validValue rejects anything that would break out of a rule line.
func validValue(s string) bool {
	return s != "" && len(s) <= 253 && !strings.ContainsAny(s, ", \t\r\n\x00()")
}

var domainRe = lazyre.New(`^[A-Za-z0-9*_-]+(?:\.[A-Za-z0-9*_-]+)*\.?$`)

func domainList(exact, suffix []string) []byte {
	var b strings.Builder
	for _, d := range exact {
		if domainRe().MatchString(d) {
			b.WriteString(strings.ToLower(strings.TrimSuffix(d, ".")) + "\n")
		}
	}
	for _, d := range suffix {
		d = strings.TrimPrefix(d, ".")
		if domainRe().MatchString(d) {
			b.WriteString("+." + strings.ToLower(strings.TrimSuffix(d, ".")) + "\n")
		}
	}
	return []byte(b.String())
}

func classicalList(keyword, regex []string) []byte {
	var b strings.Builder
	for _, k := range keyword {
		if validValue(k) {
			b.WriteString("DOMAIN-KEYWORD," + k + "\n")
		}
	}
	for _, r := range regex {
		if _, err := regexp.Compile(r); err == nil && r != "" && !strings.ContainsAny(r, ",\r\n\x00") {
			b.WriteString("DOMAIN-REGEX," + r + "\n")
		}
	}
	return []byte(b.String())
}

func cidrList(list []string) []byte {
	var b strings.Builder
	for _, c := range list {
		if p, err := parsePrefix(c); err == nil {
			b.WriteString(p.String() + "\n")
		}
	}
	return []byte(b.String())
}

func addrList(list []netip.Addr) []byte {
	var b strings.Builder
	for _, a := range list {
		if a.IsValid() {
			a = a.Unmap().WithZone("")
			b.WriteString(netip.PrefixFrom(a, a.BitLen()).String() + "\n")
		}
	}
	return []byte(b.String())
}

func family(prefix string) string {
	if strings.Contains(prefix, ":") {
		return "6"
	}
	return ""
}

func dedupe(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := list[:0:0]
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// DeviceFile is the address list path of a MAC, relative to the core dir.
func DeviceFile(mac string) string {
	m, err := net.ParseMAC(mac)
	if err != nil {
		return ""
	}
	return "devices/hyper-device-" + strings.ReplaceAll(m.String(), ":", "") + ".txt"
}

// ProviderName is the rule provider of a MAC, for the controller's provider refresh API.
func ProviderName(mac string) string {
	return strings.TrimSuffix(strings.TrimPrefix(DeviceFile(mac), "devices/"), ".txt")
}
