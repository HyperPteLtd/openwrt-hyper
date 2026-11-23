package subscription

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openweir/subconv"
	"go.yaml.in/yaml/v3"
)

func TestImportedRuleFilesStillLoad(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source-local"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source-local", "list.txt"), []byte("DOMAIN-SUFFIX,imported.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "local", Name: "test", Kind: "file"}); err != nil {
		t.Fatal(err)
	}
	body := "proxies: [{name: A, type: http, server: a.example, port: 80}]\nrule-providers: {mine: {type: file, behavior: classical, path: ./sets/list.txt}}\nrules: ['RULE-SET,mine,A', 'MATCH,DIRECT']\n"
	c, err := (Fetcher{ruleImportRoot: root}).Refresh(t.Context(), store, "local", []byte(body), Policy{})
	if err != nil || c.Decision != "ready" || len(c.Profile.BinarySets) != 1 || len(c.Profile.Rules) != 1 || string(c.Profile.BinarySets[0].Data) != "DOMAIN-SUFFIX,imported.example\n" {
		t.Fatal(err, c.Decision, c.Notices)
	}
}

const identityVLESS = `{name: VLESS, type: vless, server: v.example, port: 443, uuid: 01234567-89ab-cdef-0123-456789abcdef, flow: xtls-rprx-vision, tls: true, servername: www.example.com, client-fingerprint: chrome, reality-opts: {public-key: jUuJmNLSY1o2H_l4FaS66HB3XVAXqNJhoFrlQ1JyBTw, short-id: abcd}, udp: true}`
const identitySS = `{name: SS, type: ss, server: s.example, port: 8388, cipher: 2022-blake3-aes-128-gcm, password: MDEyMzQ1Njc4OWFiY2RlZg==, udp: true, plugin: obfs, plugin-opts: {mode: tls, host: cdn.example}}`

// rawID is the ID the router gave a valid Clash proxy before subconv converted Clash too: it
// digests the proxy as written, without its name.
func rawID(t *testing.T, source, proxy string) string {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(proxy), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "name")
	return ref("node", []any{source, proxyProtocol(m), m})
}

// IDs the router gave these nodes and groups before subconv converted Clash subscriptions too:
// saved choices, local groups and device rules refer to them.
func TestClashIdentitiesSurviveTheConverter(t *testing.T) {
	p, err := Parse("golden", []byte(`proxies:
  - `+identityVLESS+`
  - {name: VMess, type: vmess, server: m.example, port: 443, uuid: 01234567-89ab-cdef-0123-456789abcdef, alterId: 0, cipher: auto, tls: true, network: ws, ws-opts: {path: /ws, headers: {Host: m.example}}}
  - `+identitySS+`
  - {name: Trojan, type: trojan, server: t.example, port: 443, password: secret, sni: t.example, network: grpc, grpc-opts: {grpc-service-name: svc}}
  - {name: HY2, type: hysteria2, server: h.example, port: 443, password: secret, ports: 20000-30000, obfs: salamander, obfs-password: mask, sni: h.example}
  - {name: TUIC, type: tuic, server: q.example, port: 443, uuid: 01234567-89ab-cdef-0123-456789abcdef, password: secret, congestion-controller: bbr, alpn: [h3]}
  - {name: Snell, type: snell, server: n.example, port: 443, psk: strong-password, version: 4, obfs-opts: {mode: tls, host: n.example}}
  - {name: AnyTLS, type: anytls, server: a.example, port: 443, password: secret, sni: a.example, skip-cert-verify: false}
proxy-groups:
  - {name: Proxy, type: select, proxies: [VLESS, VMess, SS, Trojan, HY2, TUIC, Snell, AnyTLS, DIRECT]}
  - {name: Auto, type: url-test, url: 'https://www.gstatic.com/generate_204', interval: 300, tolerance: 50, proxies: [VLESS, VMess]}
rules:
  - DOMAIN-SUFFIX,example.org,Auto
  - GEOIP,CN,DIRECT
  - MATCH,Proxy
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"VLESS": rawID(t, "golden", identityVLESS), "VMess": "node:7c8d1a9c3b5d5d8d4fe1decb975c0d1e",
		"SS": rawID(t, "golden", identitySS), "Trojan": "node:8088edc6bf47abbe49733855ec9908d7",
		"HY2": "node:7a4dd61daf21c9f00119aedf2d0baeb5", "TUIC": "node:077e98fa271fb6059e54f6a270e88f27",
		"Snell": "node:2966cb2690cfc6623aef711488cb986e", "AnyTLS": "node:669236bdbd0a655440e76eb3957f0004",
		"Proxy": "group:d55ad108424a1644112b0c64457491eb", "Auto": "group:3df319d51d8e726a0f8baaaddc33cab2",
	}
	got := map[string]string{}
	for _, n := range p.Nodes {
		got[n.Name] = n.ID
	}
	for _, g := range p.Groups {
		got[g.Name] = g.ID
	}
	for name, id := range want {
		if got[name] != id {
			t.Errorf("%s: %s, was %s", name, got[name], id)
		}
	}
	if len(p.Notices) != 0 || p.Format != "clash" || len(p.Rules) != 2 || p.Final != want["Proxy"] {
		t.Fatal(p.Notices, p.Format, p.Rules)
	}
}

func TestFormatsKeepTheirNames(t *testing.T) {
	for raw, format := range map[string]string{
		basic: "uri",
		base64.StdEncoding.EncodeToString([]byte(basic)): "uri",
		primary: "sing-box",
		"[Proxy]\nEdge = http, edge.example, 8080":                                                      "surge",
		"[server_local]\nhttp=edge.example:8080, tag=Edge":                                              "quantumultx",
		`{"servers":[{"server":"s.example","server_port":8388,"method":"aes-128-gcm","password":"x"}]}`: "sip008",
	} {
		if p := mustParse(t, raw); p.Format != format {
			t.Errorf("%q: %s", raw, p.Format)
		}
	}
}

func TestWhatIsNoSubscriptionFailsClearly(t *testing.T) {
	for raw, want := range map[string]error{
		" \n": errEmptySubscription,
		"<!DOCTYPE html><html><body>Login</body></html>": errWebPage,
		`{"msg":"token expired"}`:                        errMessage,
		"\x01\x02\x03 binary garbage \x04":               errUnknownFormat,
	} {
		if _, err := Parse("s", []byte(raw)); !errors.Is(err, want) {
			t.Errorf("%q: %v", raw, err)
		}
	}
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "page", Name: "test", Kind: "file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "page", []byte("<html><title>Just a moment</title></html>"), Policy{}); !errors.Is(err, errWebPage) {
		t.Fatal(err)
	}
	if st, _ := store.Status("page"); st.LastFailure != "subscription_web_page" {
		t.Fatal(st.LastFailure)
	}
}

func TestConverterWarningsKeepTheirWeight(t *testing.T) {
	for _, tc := range []struct {
		warning subconv.Warning
		code    string
		danger  bool
	}{
		// Only a dropped rejection needs confirming; its rule set's rules are reported one by one.
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "USER-AGENT,app*,Proxy: rule type USER-AGENT"}, "subconv_unsupported_rule", false},
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "USER-AGENT,ads*,REJECT: rule type USER-AGENT"}, "subconv_unsupported_rule", true},
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "IP-CIDR6,2001:db8::/32,REJECT-DROP,no-resolve: x"}, "subconv_unsupported_rule", true},
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "rule 2 to REJECT: rule set ads was left out"}, "subconv_unsupported_rule", true},
		// Other clients' rejections: Surge's REJECT-TINYGIF, Quantumult X's reject-200 and the like.
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "URL-REGEX,^https://ad,REJECT-TINYGIF: rule type URL-REGEX"}, "subconv_unsupported_rule", true},
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "user-agent, ads*, reject-200: rule type USER-AGENT"}, "subconv_unsupported_rule", true},
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "rule 4 to REJECT-NO-DROP: rule set ads was left out"}, "subconv_unsupported_rule", true},
		{subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "rule 3 to Proxy: rule set ads was left out"}, "subconv_unsupported_rule", false},
		{subconv.Warning{Code: subconv.WarnUnsupportedRuleSet, Detail: "ads: a sing-box rule set mihomo cannot read"}, "subconv_unsupported_rule_set", false},
		{subconv.Warning{Code: "invalid_credential", Detail: "Edge: a password but no psk"}, "nonstandard_snell_credential", false},
		{subconv.Warning{Code: "invalid_credential", Detail: "Edge: no psk"}, "invalid_credential", false},
		{subconv.Warning{Code: "invalid_credential", Detail: "Edge: no uuid"}, "node_missing_credential", false},
		{subconv.Warning{Code: subconv.WarnRenamed, Detail: "Edge: renamed Edge 2"}, "subconv_renamed", false},
	} {
		n := warningNotice(tc.warning)
		if n.Code != tc.code || n.Dangerous != tc.danger || strings.Contains(n.At, "psk") {
			t.Errorf("%v: %+v", tc.warning, n)
		}
	}
	// Only dropped rejections weigh on the routing; whatever else subconv reports informs.
	p := mustParse(t, basic)
	for _, code := range []string{"invalid_proxy", "ignored_option", "renamed", "unsupported_provider", "local_provider", "unsupported_group", "unknown_reference", "cyclic_reference", "empty_group", "unsupported_proxy", "unsupported_option", "a_future_code"} {
		p.Notices = append(p.Notices, warningNotice(subconv.Warning{Code: code, Detail: "x"}))
	}
	if c := Assess(p, nil, Policy{}); c.Decision != "ready" {
		t.Fatal(c.Decision)
	}
	p.Notices = append(p.Notices, warningNotice(subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "SUB-RULE,(NETWORK,UDP),udp: rule type SUB-RULE"}))
	if c := Assess(p, nil, Policy{AllowLocalFallback: true}); c.Decision != "ready" {
		t.Fatal("a dropped rule that rejected nothing stopped the refresh", c.Decision)
	}
	p.Notices = append(p.Notices, warningNotice(subconv.Warning{Code: subconv.WarnUnsupportedRule, Detail: "GEOSITE,category-ads,REJECT: unknown"}))
	if c := Assess(p, nil, Policy{AllowLocalFallback: true}); c.Decision != "confirmation_required" {
		t.Fatal("a dropped rejection went unconfirmed")
	}
}

func TestClashFieldsTheConverterLeavesOutAreReported(t *testing.T) {
	p, err := Parse("s", []byte(`tun: {enable: true}
external-controller: 0.0.0.0:9090
script:
  code: |
    def main(ctx, metadata): return "DIRECT"
geox-url:
  geosite: https://example.org/my/geosite.dat
proxies:
  - {name: Edge, type: http, server: edge.example, port: 8080}
rules:
  - GEOSITE,google,Edge
  - GEOIP,JP,Edge
  - MATCH,DIRECT
`))
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, n := range p.Notices {
		codes = append(codes, fmt.Sprint(n.Code, " ", n.At, " ", n.Dangerous))
	}
	for _, want := range []string{"mihomo_router_field_ignored tun false", "mihomo_router_field_ignored external-controller false", "script script true", "rule_provider_custom_geo GEOSITE google false"} {
		if !slices.Contains(codes, want) {
			t.Fatal(codes)
		}
	}
	if len(p.Rules) != 1 || p.Rules[0].Match["clash"] != "GEOIP,JP" {
		t.Fatal("default geodata rule lost", p.Rules)
	}
}

func TestProvidersOfAnyClientAreConverted(t *testing.T) {
	const prefix = "/api/v1/app/proxy/subscriptions/rulesets/"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prefix + "surge/list":
			fmt.Fprint(w, "# Surge\nDOMAIN-SUFFIX,surge.example\nUSER-AGENT,Instagram*\nIP-CIDR,192.0.2.0/24,no-resolve\n")
		case prefix + "singbox/json":
			fmt.Fprint(w, `{"version":2,"rules":[{"domain_suffix":["sing.example"]}]}`)
		case prefix + "empty/list":
			fmt.Fprint(w, "# nothing\n")
		case prefix + "nodes/links":
			fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte("trojan://secret@t.example:443#Linked\n")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := mustConvert(t, fmt.Sprintf(`proxy-providers:
  linked: {type: http, url: '%[1]s%[2]snodes/links'}
proxy-groups:
  - {name: Proxy, type: select, use: [linked]}
rule-providers:
  surge: {type: http, behavior: classical, format: text, url: '%[1]s%[2]ssurge/list'}
  sing: {type: http, behavior: domain, url: '%[1]s%[2]ssingbox/json'}
  empty: {type: http, behavior: domain, format: text, url: '%[1]s%[2]sempty/list'}
rules:
  - RULE-SET,surge,Proxy
  - RULE-SET,sing,Proxy
  - RULE-SET,empty,REJECT
  - MATCH,DIRECT
`, srv.URL, prefix))
	sets, notices, err := (Fetcher{Client: srv.Client()}).expandSourceProviders(t.Context(), c.doc, srv.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseVerified("s", c, sets, notices...)
	if err != nil || len(p.Nodes) != 1 || p.Nodes[0].Name != "Linked" || len(p.Groups[0].Members) != 1 {
		t.Fatal(err, p.Nodes, p.Notices)
	}
	byTag := map[string]BinarySet{}
	for _, s := range p.BinarySets {
		byTag[s.Tag] = s
	}
	if s := byTag["surge"]; s.Format != "text" || s.Entries != 2 || string(s.Data) != "DOMAIN-SUFFIX,surge.example\nIP-CIDR,192.0.2.0/24,no-resolve\n" {
		t.Fatalf("Surge list: %+v %q", s, s.Data)
	}
	if s := byTag["sing"]; s.Format != "text" || s.Behavior != "domain" || string(s.Data) != "+.sing.example\n" {
		t.Fatalf("sing-box source: %+v %q", s, s.Data)
	}
	// An empty set matches nothing, as its provider meant: it and its rule stay, without a word.
	if s, ok := byTag["empty"]; !ok || s.Entries != 0 || len(s.Data) != 0 || len(p.Rules) != 3 || len(p.Notices) != 0 {
		t.Fatal(byTag["empty"], p.Rules, p.Notices)
	}
}
