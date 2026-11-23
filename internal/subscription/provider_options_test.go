package subscription

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestProxyProviderOptionsApplyWithinRouterSafety(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "abc" || r.Header.Get("User-Agent") != "clash.meta" || r.Header.Get("Proxy-Authorization") != "" || r.Host == "evil.example" {
			t.Error("provider headers", r.Header, r.Host)
		}
		fmt.Fprint(w, `proxies:
 - {name: HK 1, type: http, server: hk1.example, port: 80}
 - {name: HK 2, type: ss, server: hk2.example, port: 80, cipher: aes-128-gcm, password: x}
 - {name: JP 1, type: http, server: jp1.example, port: 80}
 - {name: HK Expire, type: http, server: info.example, port: 80}
 - {name: HK 1, type: http, server: dup.example, port: 80}
`)
	}))
	defer srv.Close()
	var m map[string]any
	doc := fmt.Sprintf(`{"proxies":[{"name":"Front","type":"http","server":"front.example","port":80}],
"proxy-providers":{
 "air":{"type":"http","url":%q,"interval":3600,"proxy":"Front","dialer-proxy":"Front",
  "header":{"User-Agent":["clash.meta"],"X-Token":["abc"],"Host":["evil.example"],"Proxy-Authorization":["x"]},
  "filter":"JP`+"`"+`HK","exclude-filter":"Expire","exclude-type":"ss|vmess",
  "override":{"udp":true,"interface-name":"eth1","routing-mark":7,"additional-prefix":"[air] ","proxy-name":[{"pattern":" (\\d)","target":"-$1"}],"override-expr":[".udp = false"]}},
 "inline":{"type":"inline","filter":"B`+"`"+`A","exclude-filter":"(?!x)","payload":[
  {"name":"A","type":"http","server":"a.example","port":80},{"name":"B","type":"http","server":"b.example","port":80}]}},
"proxy-groups":[{"name":"Air","type":"select","use":["air"]},{"name":"Inline","type":"select","use":["inline"]}],
"rules":["MATCH,Air"]}`, srv.URL+"/nodes")
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	s := providers{f: Fetcher{Client: srv.Client()}, ctx: t.Context(), cache: map[string][]byte{}}
	if err := s.clash(m); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range list(m["proxies"]) {
		n := object(x)
		names = append(names, field(n, "name"))
		if n["interface-name"] != nil || n["routing-mark"] != nil {
			t.Fatal("unsafe override applied", n)
		}
		if field(n, "server") == "jp1.example" && (n["udp"] != true || n["dialer-proxy"] != "Front") {
			t.Fatal("override not applied", n)
		}
	}
	// Each filter picks in turn and names are overridden after filtering. subconv gives a
	// downloaded duplicate a name of its own, where mihomo would drop it.
	if !slices.Equal(names, []string{"Front", "[air] JP-1", "[air] HK-1", "[air] HK-1-2", "B", "A"}) {
		t.Fatal(names)
	}
	want := []Notice{
		{Code: "provider_option_ignored", At: "air · header"},
		{Code: "provider_option_ignored", At: "air · proxy"},
		{Code: "provider_option_unsafe", At: "air · override interface-name"},
		{Code: "provider_option_ignored", At: "air · override override-expr"},
		{Code: "provider_option_unsafe", At: "air · override routing-mark"},
		{Code: "provider_option_ignored", At: "inline · exclude-filter"},
		{Code: "subconv_renamed", At: "HK 1: renamed HK 1 2"},
	}
	if !slices.Equal(s.notices, want) {
		t.Fatal(s.notices)
	}
	p, err := parseVerified("source", conversion{doc: m}, nil)
	if err != nil {
		t.Fatal(err, p.Notices)
	}
	front := p.Nodes[0].ID
	if len(p.Nodes) != 6 || p.Nodes[1].Dialer() != front || len(p.Groups[0].Members) != 3 || len(p.Notices) != 0 {
		t.Fatal(p.Nodes, p.Groups, p.Notices)
	}
	p.Notices = s.notices
	if c := Assess(p, nil, Policy{}); c.Decision != "ready" || c.Profile.Local {
		t.Fatal("ignored provider options cost the routing", c.Decision)
	}
}

func TestProviderNodesPassTheSameSafetyPolicy(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"proxies":[],"proxy-providers":{"p":{"type":"inline","payload":[
{"name":"Marked","type":"http","server":"m.example","port":80,"routing-mark":7},
{"name":"Plugin","type":"ss","server":"s.example","port":80,"cipher":"aes-128-gcm","password":"x","plugin-opts":{"key-path":"/etc/shadow"}},
{"name":"Fine","type":"http","server":"f.example","port":80}]}},
"proxy-groups":[{"name":"P","type":"select","use":["p"]}],"rules":["MATCH,P"]}`), &m); err != nil {
		t.Fatal(err)
	}
	s := providers{ctx: t.Context(), cache: map[string][]byte{}}
	if err := s.clash(m); err != nil {
		t.Fatal(err)
	}
	p, err := parseVerified("source", conversion{doc: m}, nil)
	if err != nil || len(p.Nodes) != 1 || p.Nodes[0].Name != "Fine" || !slices.Contains(p.Notices, Notice{Code: "unsupported_node", At: "Marked"}) || !slices.Contains(p.Notices, Notice{Code: "unsupported_node", At: "Plugin"}) {
		t.Fatal(err, p.Nodes, p.Notices)
	}
}

func TestProvidersTheRouterCannotExpandStayUnexpanded(t *testing.T) {
	s := providers{}
	for _, v := range []map[string]any{
		{"type": "file", "path": "./nodes.yaml"},
		{"type": "http", "url": "https://p.example/nodes", "age-secret-key": "AGE-SECRET-KEY-1"},
		{"type": "http"},
	} {
		if _, ok := s.proxyProvider("p", v); ok {
			t.Fatal("expanded", v)
		}
	}
	if h := s.providerHeader("p", map[string]any{"Bad Name": []any{"x"}, "X-Ok": "1", "X-Long": []any{string(make([]byte, 2000))}, "Transfer-Encoding": "chunked"}); len(h) != 1 || h.Get("X-Ok") != "1" {
		t.Fatal(h)
	}
	if p, _ := s.proxyProvider("p", map[string]any{"type": "http", "url": "https://p.example/nodes", "size-limit": 4096}); p.input.limit != 4096 {
		t.Fatal("size limit ignored")
	}
}

func TestRefreshReportsProviderOptionNotices(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plan" {
			// The subscription's own origin may serve its providers from loopback.
			fmt.Fprintf(w, "proxy-providers:\n  air: {type: http, url: %q, proxy: DIRECT}\nproxy-groups:\n  - {name: Air, type: select, use: [air]}\nrules:\n  - MATCH,Air\n", srv.URL+"/api/v1/app/proxy/subscriptions/rulesets/air/nodes")
			return
		}
		fmt.Fprint(w, "proxies:\n  - {name: N, type: http, server: n.example, port: 80}\n")
	}))
	defer srv.Close()
	st := OpenStore(t.TempDir())
	src := Source{ID: "air", Name: "test", Kind: "external", URL: srv.URL + "/plan"}
	if err := st.Configure(src); err != nil {
		t.Fatal(err)
	}
	c, err := (Fetcher{Client: srv.Client()}).Refresh(t.Context(), st, src.ID, nil, Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Local || !slices.Contains(c.Notices, Notice{Code: "provider_option_ignored", At: "air · proxy"}) {
		t.Fatal(err, c.Decision, c.Notices)
	}
}

// A group's exclude-type leaves out the providers' nodes of those types, in either spelling.
func TestGroupExcludeTypeFiltersProviderNodes(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"proxies":[],"proxy-providers":{"p":{"type":"inline","payload":[
{"name":"S","type":"ss","server":"s.example","port":80,"cipher":"aes-128-gcm","password":"x"},
{"name":"T","type":"trojan","server":"t.example","port":443,"password":"x"},
{"name":"H","type":"http","server":"h.example","port":80}]}},
"proxy-groups":[{"name":"A","type":"select","use":["p"],"exclude-type":"Shadowsocks|http"}],"rules":["MATCH,A"]}`), &m); err != nil {
		t.Fatal(err)
	}
	s := providers{ctx: t.Context(), cache: map[string][]byte{}}
	if err := s.clash(m); err != nil {
		t.Fatal(err)
	}
	g := object(list(m["proxy-groups"])[0])
	if members := list(g["proxies"]); len(members) != 1 || members[0] != "T" || g["exclude-type"] != nil {
		t.Fatal(g)
	}
}
