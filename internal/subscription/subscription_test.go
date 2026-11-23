package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const basic = "http://demo:fixture-only-password@edge.example:8080#Edge"
const primary = `{"outbounds":[{"type":"http","tag":"Edge","server":"edge.example","server_port":8080,"username":"demo","password":"fixture-only-password"},{"type":"direct","tag":"direct"},{"type":"selector","tag":"Streaming","outbounds":["Edge","direct"]}],"route":{"rules":[{"domain":["media.example"],"outbound":"Streaming"},{"domain":"blocked.example","action":"reject"}],"final":"direct"}}`

func mustConvert(t *testing.T, body string) conversion {
	t.Helper()
	c, err := convert(t.Context(), []byte(body), RoutingOwn, nil)
	if err != nil {
		t.Fatal(err, c.notices)
	}
	return c
}

func mustParse(t *testing.T, s string) Profile {
	t.Helper()
	p, e := Parse("source", []byte(s))
	if e != nil {
		t.Fatal(e, p.Notices)
	}
	return p
}

func TestAllProtocolsImport(t *testing.T) {
	b64 := base64.RawURLEncoding.EncodeToString
	vm := `{"v":"2","add":"host.example","port":"443","id":"7e52ffc3-393c-432f-9afc-598762919983","net":"ws","path":"/p","tls":"tls"}`
	ssr := "host.example:443:auth_sha1_v4:aes-256-cfb:plain:" + b64([]byte("key")) + "/?remarks=" + b64([]byte("ssr"))
	cases := []struct{ protocol, raw, key string }{
		{"vless", "vless://7e52ffc3-393c-432f-9afc-598762919983@host.example:443?security=reality&pbk=jUuJmNLSY1o2H_l4FaS66HB3XVAXqNJhoFrlQ1JyBTw&flow=xtls-rprx-vision&sid=1234&fp=chrome", "flow"},
		{"vmess", "vmess://" + b64([]byte(vm)), "network"},
		{"trojan", "trojan://key@host.example:443?sni=server.example", "sni"},
		{"shadowsocks", "ss://" + b64([]byte("aes-256-gcm:key")) + "@host.example:443", "cipher"},
		{"shadowsocksr", "ssr://" + b64([]byte(ssr)), "protocol"},
		{"anytls", "anytls://key@host.example:443?sni=server.example", "sni"},
		{"hysteria2", "hy2://key@host.example:443?obfs=salamander&obfs-password=mask", "obfs"},
		{"tuic", "tuic://7e52ffc3-393c-432f-9afc-598762919983:key@host.example:443?congestion_control=bbr", "password"},
		{"http", basic, "username"}, {"https", "https://demo:key@host.example:443", "tls"}, {"socks5", "socks5://demo:key@host.example:1080", "type"},
		{"naive", "naive+quic://user:pw@host.example:443", "quic"},
		{"snell", "snell://strong-password@host.example:443?version=6&mode=unshaped&userkey=second", "mode"},
		{"mieru", "mierus://user:key@host.example?profile=plan&port=443&protocol=TCP", "transport"},
		{"nowhere", "nowhere://0102030405060708090a0b0c0d0e0f10@host.example:443?net=udp&morph=1#NW3", "network"},
	}
	for _, c := range cases {
		t.Run(c.protocol, func(t *testing.T) {
			p := mustParse(t, c.raw)
			if len(p.Nodes) != 1 || p.Nodes[0].Protocol != c.protocol || p.Nodes[0].Outbound[c.key] == nil || p.Nodes[0].Special != (c.protocol == "mtproxy") {
				t.Fatal(c.protocol, p.Notices)
			}
		})
	}
}
func TestInputVariantsAndIdentity(t *testing.T) {
	p := mustParse(t, primary)
	n := mustParse(t, basic).Nodes[0]
	uriRenamed := mustParse(t, strings.ReplaceAll(basic, "#Edge", "#renamed"))
	if n.ID != uriRenamed.Nodes[0].ID || n.ID != p.Nodes[0].ID {
		t.Fatal("identity depends on displayed name or format")
	}
	for _, doc := range []string{base64.StdEncoding.EncodeToString([]byte(basic)), "\uFEFF" + basic,
		`proxies:
 - {name: Edge, type: http, server: edge.example, port: 8080, username: demo, password: fixture-only-password}
proxy-groups:
 - {name: Streaming, type: select, proxies: [Edge, DIRECT]}
rules:
 - DOMAIN,media.example,Streaming
 - MATCH,DIRECT`,
		"[Proxy]\nEdge = http, edge.example, 8080, demo, fixture-only-password\n[Proxy Group]\nStreaming = select, Edge, DIRECT\n[Rule]\nFINAL,DIRECT",
		"[server_local]\nhttp=edge.example:8080, username=demo, password=fixture-only-password, tag=Edge\n[policy]\nstatic=Streaming, Edge, DIRECT\n[filter_local]\nfinal,DIRECT"} {
		p := mustParse(t, doc)
		if len(p.Notices) != 0 || len(p.Nodes) != 1 || p.Nodes[0].ID != n.ID {
			t.Fatal(p.Format, p.Notices)
		}
	}
	for _, doc := range []string{`{"version":1,"servers":[{"server":"edge.example"}]}`, `[{"server":"edge.example"}]`, "<html>Access denied</html>", "vless://x@node.test:443?security=unknown", "vector://01@node.test:443"} {
		if _, e := Parse("source", []byte(doc)); e == nil {
			t.Fatalf("accepted invalid format %s", doc)
		}
	}
}
func TestProfileSummaryOmitsPrivateNodeDetails(t *testing.T) {
	raw, err := json.Marshal(mustParse(t, primary).Summary())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-only-password") || strings.Contains(string(raw), "server_port") {
		t.Fatal("private node detail escaped")
	}
}
func TestWholePolicyLossConfirmationAndActiveRefs(t *testing.T) {
	prev := mustParse(t, primary)
	next := mustParse(t, basic)
	a := Assess(next, &prev, Policy{AllowLocalFallback: true})
	if a.Decision != "confirmation_required" || a.ConfirmationID == "" {
		t.Fatal(a.Decision, a.Notices)
	}
	a = Assess(next, &prev, Policy{Confirm: a.ConfirmationID})
	if a.Decision != "ready" || !a.Profile.Local || len(a.Profile.Rules) > 0 {
		t.Fatal(a.Decision)
	}
	next = mustParse(t, strings.ReplaceAll(basic, "8080", "8081"))
	b := Assess(next, &prev, Policy{AllowLocalFallback: true, Confirm: a.ConfirmationID, ActiveIDs: []string{prev.Nodes[0].ID}})
	if b.Decision != "blocked" {
		t.Fatal(b.Decision)
	}
}
func TestAtomicWriteRevokeLateCallbacksRollback(t *testing.T) {
	s := OpenStore(t.TempDir())
	c := Candidate{Profile: mustParse(t, basic), Decision: "ready"}
	for i := 0; i < 2; i++ {
		tick, e := s.Begin("source")
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Commit(tick, c); e != nil {
			t.Fatal(e)
		}
		if s.Commit(tick, c) == nil {
			t.Fatal("ticket reused")
		}
	}
	a, _ := s.Begin("source")
	b, _ := s.Begin("source")
	if !errors.Is(s.Commit(a, c), ErrSuperseded) {
		t.Fatal("stale intent accepted")
	}
	if e := s.RevokeIfCurrent(b); e != nil {
		t.Fatal(e)
	}
	fresh := OpenStore(s.dir)
	if _, e := fresh.Load("source"); !errors.Is(e, ErrRevoked) {
		t.Fatal(e)
	}
	if !errors.Is(fresh.Rollback("source"), ErrRevoked) || !errors.Is(fresh.Commit(b, c), ErrRevoked) {
		t.Fatal("recovered revoked source")
	}
	body, e := os.ReadFile(s.path("source"))
	if e != nil || strings.Contains(string(body), "fixture-only-password") {
		t.Fatal("revoked secrets persisted")
	}
}
func TestPlatformScopeAndAccountLoss(t *testing.T) {
	var mode atomic.Int32
	var configCalls int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/app/billing/subscriptions" {
			fmt.Fprint(w, `[{"id":7,"userId":"account","status":"disabled","usable":false}]`)
			return
		}
		if r.URL.Path == "/api/v1/app/billing/subscriptions/7" {
			fmt.Fprint(w, `{"id":7,"userId":"account","status":"active","usable":true,"url":null,"subscriptionAccess":"app"}`)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/app/proxy/subscriptions/7/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer platform-secret" {
			t.Error("missing platform authentication")
		}
		if r.URL.Query().Get("include") != "production" || r.URL.Query().Get("exclude") != "private" {
			t.Error("lost filter scope", r.URL.Path)
		}
		if r.URL.Query().Has("target") || r.URL.Query().Has("flag") {
			t.Error("router forced a subscription template")
		}
		if r.UserAgent() != subscriptionHeader(platformUserAgent) {
			t.Error("account changed template scope", r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "/nodes") {
			switch mode.Load() {
			case 1:
				http.Error(w, "{}", 401)
				return
			case 2:
				fmt.Fprint(w, `{"usable":false,"nodes":[]}`)
				return
			}
			fmt.Fprint(w, `{"usable":true,"nodes":[{"protocol":"http","status":"active"},{"protocol":"https","status":"online"}]}`)
			return
		}
		atomic.AddInt32(&configCalls, 1)
		// Operators may customize both the body and the attachment name.
		w.Header().Set("Content-Disposition", "attachment; filename=custom.json")
		fmt.Fprint(w, primary)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(h))
	defer srv.Close()
	f := Fetcher{Client: srv.Client()}
	s := OpenStore(t.TempDir())
	src := Source{Kind: "platform", ID: "source", Name: "Plan", UserAgent: "Legacy/4", Origin: srv.URL, Token: "platform-secret", UserID: "account", PlanID: 7, Filters: map[string]string{"flag": "old-template", "include": "production", "exclude": "private"}}
	cfg := func() {
		if e := s.configure(src, true); e != nil {
			t.Fatal(e)
		}
	}
	cfg()
	c, e := f.Refresh(t.Context(), s, src.ID, nil, Policy{})
	if e != nil || c.Decision != "ready" || c.Profile.Format != "sing-box" || len(c.Profile.Nodes) != 1 || len(c.Profile.Groups) != 1 || len(c.Profile.Groups[0].Members) != 2 || configCalls != 1 || !reflect.DeepEqual(c.Notices, []Notice{{Code: "unavailable_inventory", At: "https"}}) {
		t.Fatal(e, c.Decision, c.Notices)
	}
	mode.Store(1)
	if _, e = f.Refresh(t.Context(), s, src.ID, nil, Policy{}); !errors.Is(e, ErrLoginRequired) {
		t.Fatal(e)
	}
	if _, e = s.Load(src.ID); e != nil {
		t.Fatal("login expiry removed cached data", e)
	}
	cfg()
	mode.Store(2)
	if _, e = f.Refresh(t.Context(), s, src.ID, nil, Policy{}); !errors.Is(e, ErrRevoked) {
		t.Fatal(e)
	}
	if _, e = s.Load(src.ID); !errors.Is(e, ErrRevoked) {
		t.Fatal(e)
	}
}
func TestExternalDoesNotUsePlatformAndFailureRetainsSnapshot(t *testing.T) {
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "token=private&filter=a%2Bb" || r.Header.Get("User-Agent") != "Custom UA" || r.Header.Get("Authorization") != "" {
			t.Error("wrong request scope")
		}
		if mode.Load() != 0 {
			http.Error(w, "internal secret", 502)
			return
		}
		io.WriteString(w, basic)
	}))
	defer srv.Close()
	s := OpenStore(t.TempDir())
	src := Source{ID: "ext", Name: "External", Kind: "external", URL: srv.URL + "/sub?token=private&filter=a%2Bb", UserAgent: "Custom UA"}
	if e := s.Configure(src); e != nil {
		t.Fatal(e)
	}
	f := Fetcher{}
	_, e := f.Refresh(t.Context(), s, "ext", nil, Policy{})
	if e != nil {
		t.Fatal(e)
	}
	mode.Store(1)
	_, e = f.Refresh(t.Context(), s, "ext", nil, Policy{})
	if e == nil || strings.Contains(e.Error(), "private") {
		t.Fatal(e)
	}
	p, e := s.Load("ext")
	if e != nil || len(p.Nodes) != 1 {
		t.Fatal(e)
	}
	list, _ := s.List()
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), "private") {
		t.Fatal("URL escaped")
	}
}

func TestExternalPresetsChangeOnlyRequestHeader(t *testing.T) {
	var received string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.UserAgent()
		if r.URL.RawQuery != "chosen=1" || r.Header.Get("Authorization") != "" {
			t.Error("user-agent changed the original query or attached credentials")
		}
		fmt.Fprint(w, primary)
	}))
	defer host.Close()
	s := OpenStore(t.TempDir())
	if err := s.Configure(Source{ID: "url", Name: "test", Kind: "external", URL: host.URL + "/sub?chosen=1"}); err != nil {
		t.Fatal(err)
	}
	for _, ua := range []string{"", "sing-box", "clash", "Hyper", "Legacy/1.14 custom"} {
		if err := s.Preferences("url", Preferences{UserAgent: ua}); err != nil {
			t.Fatal(err)
		}
		c, err := (Fetcher{}).Refresh(t.Context(), s, "url", nil, Policy{})
		want := first(ua, "clash")
		if ua == "Hyper" {
			want = "Hyper/" + packageVersion + " (OpenWrt)"
		}
		if err != nil || c.Decision != "ready" || c.Profile.Format != "sing-box" || received != want {
			t.Fatal("incorrect request header or content detection", ua, c.Decision, err)
		}
	}
}

func TestPlatformAutoTemplateWithoutQuery(t *testing.T) {
	calls := 0
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "Hyper/") || !strings.HasSuffix(r.UserAgent(), " (OpenWrt)") {
			t.Error("platform UA cannot select the Hyper template")
		}
		switch r.RequestURI {
		case "/api/v1/app/billing/subscriptions/7":
			fmt.Fprint(w, `{"id":7,"userId":"account","status":"active","usable":true,"url":"https://unused.invalid","subscriptionAccess":"both"}`)
		case "/api/v1/app/proxy/subscriptions/7/nodes":
			fmt.Fprint(w, `{"usable":true,"nodes":[{"name":"Edge","protocol":"http"}]}`)
		case "/api/v1/app/proxy/subscriptions/7/config":
			calls++
			w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''hyper.yaml")
			w.Header().Set("X-Routing-Warnings", "3")
			w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=10")
			fmt.Fprint(w, platformFixtureYAML)
		default:
			t.Error("unexpected path or query", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	store := OpenStore(t.TempDir())
	src := Source{Kind: "platform", ID: "platform", Name: "Plan", Origin: host.URL, Token: "fixture", UserID: "account", PlanID: 7}
	if err := store.configure(src, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Preferences(src.ID, Preferences{UserAgent: "clash"}); err != nil {
		t.Fatal(err)
	}
	c, err := (Fetcher{Client: host.Client()}).Refresh(t.Context(), store, src.ID, nil, Policy{})
	if err != nil || calls != 1 || c.Decision != "ready" || c.Usage == nil || c.Usage.Total == nil || *c.Usage.Total != 10 {
		t.Fatal("complete template did not import in one config request", calls, err, c.Decision)
	}
	st, err := OpenStore(store.dir).Status(src.ID)
	if err != nil || st.Agent != platformUserAgent || st.CustomAgent || st.Format != "hyper" || st.Local || !reflect.DeepEqual(st.Notices, []Notice{{Code: "platform_routing_warnings", At: "3"}}) {
		t.Fatal("saved template status did not survive reopening", st, err)
	}
}
func TestProviderExpansionAndIndependentCredentials(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("User-Agent") != "Hyper" {
			t.Error("private headers sent")
		}
		switch r.URL.Path {
		case "/nodes":
			fmt.Fprint(w, `proxies: [{name: Extra, type: http, server: extra.example, port: 81}]`)
		case "/rules":
			fmt.Fprint(w, `payload: ["+.movies.example"]`)
		default:
			http.NotFound(w, r)
		}
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(h))
	defer srv.Close()
	s := providers{f: Fetcher{Client: srv.Client()}, ctx: t.Context(), cache: map[string][]byte{}}
	m := map[string]any{"proxy-providers": map[string]any{"extra": map[string]any{"type": "http", "url": srv.URL + "/nodes"}},
		"rule-providers": map[string]any{"media": map[string]any{"type": "http", "url": srv.URL + "/rules", "behavior": "domain"}},
		"proxy-groups":   []any{map[string]any{"name": "Streaming", "type": "select", "use": []any{"extra"}}}, "rules": []any{"RULE-SET,media,Streaming", "MATCH,DIRECT"}}
	if e := s.clash(m); e != nil {
		t.Fatal(e)
	}
	p, parseErr := parseVerified("source", conversion{doc: m}, s.binary)
	if parseErr != nil || len(p.BinarySets) != 1 {
		t.Fatal(parseErr)
	}
	content, err := p.BinarySets[0].contents()
	if err != nil || !bytes.Contains(content, []byte("movies.example")) {
		t.Fatal(err, string(content))
	}
	if len(p.Notices) > 0 || len(p.Nodes) != 1 || p.Groups[0].Members[0] != p.Nodes[0].ID || len(p.Rules) != 1 || p.Rules[0].RuleSet != "media" || p.Final != Direct {
		t.Fatal(p)
	}
	f := Fetcher{}
	if _, _, e := f.expandSourceProviders(context.Background(), map[string]any{"proxy-providers": map[string]any{"id": map[string]any{"type": "http", "url": srv.URL}}}, "", nil, nil); e == nil {
		t.Fatal("untrusted nested provider reached router loopback")
	}
}

func TestAccountLevelWithdrawalPurgesSiblingPlans(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ephemeral" {
			t.Fatal("unexpected credential scope")
		}
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"code":"ACCOUNT_REVOKED"}`)
	}))
	defer server.Close()
	store := OpenStore(t.TempDir())
	for i, id := range []string{"first", "second"} {
		src := Source{ID: id, Kind: "platform", Name: id, Origin: server.URL, UserID: "account", PlanID: i + 1, Token: "ephemeral"}
		if e := store.configure(src, true); e != nil {
			t.Fatal(e)
		}
	}
	_, e := (Fetcher{Client: server.Client()}).Refresh(context.Background(), store, "first", nil, Policy{})
	var w *AccountWithdrawal
	if !errors.As(e, &w) || len(w.Sources) != 2 {
		t.Fatalf("expected both affected plans, got %v", e)
	}
	for _, id := range []string{"first", "second"} {
		r, e := store.raw(id)
		if e != nil || !r.Revoked || r.Config != nil || r.Current != nil || r.Previous != nil {
			t.Fatal("revoked plan still accessible")
		}
	}
}

func TestAmbiguousNotFoundAndBillingTimeoutKeepsRunningProfile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/nodes") {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "temporary upstream timeout", http.StatusGatewayTimeout)
	}))
	defer server.Close()
	store := OpenStore(t.TempDir())
	src := Source{ID: "source", Kind: "platform", Name: "test", Origin: server.URL, UserID: "account", PlanID: 3, Token: "ephemeral"}
	if e := store.configure(src, true); e != nil {
		t.Fatal(e)
	}
	ticket, e := store.Begin(src.ID)
	if e != nil {
		t.Fatal(e)
	}
	candidate := Assess(mustParse(t, primary), nil, Policy{})
	if e = store.Commit(ticket, candidate); e != nil {
		t.Fatal(e)
	}
	_, e = (Fetcher{Client: server.Client()}).Refresh(context.Background(), store, src.ID, nil, Policy{})
	if e == nil || errors.Is(e, ErrSuspended) || errors.Is(e, ErrRevoked) {
		t.Fatalf("ambiguous update: %v", e)
	}
	st, e := store.Status(src.ID)
	if e != nil || !st.Ready || st.LastFailure != "entitlement_check_failed" {
		t.Fatal("failed refresh deactivated or failed to report the last working profile")
	}
}

func TestReplacementURLKeepsWorkingConfigUntilVerified(t *testing.T) {
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, basic) }))
	defer original.Close()
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "http://other.example.org:82#Other") }))
	defer next.Close()
	s := OpenStore(t.TempDir())
	if e := s.Configure(Source{ID: "external", Name: "User", Kind: "external", URL: original.URL}); e != nil {
		t.Fatal(e)
	}
	refresh := func(url string) error {
		_, e := (Fetcher{ReplacementURL: url}).Refresh(context.Background(), s, "external", nil, Policy{})
		return e
	}
	if e := refresh(""); e != nil {
		t.Fatal(e)
	}
	before, _ := s.Status("external")
	if e := refresh(bad.URL); e == nil {
		t.Fatal("bad URL accepted")
	}
	r, _ := s.raw("external")
	if r.Config.URL != original.URL || r.Epoch != before.Epoch || r.Current == nil {
		t.Fatal("failed URL replacement changed source")
	}
	if e := refresh(next.URL); e != nil {
		t.Fatal(e)
	}
	r, _ = s.raw("external")
	if r.Config.URL != next.URL || r.Epoch <= before.Epoch || r.Current.Nodes[0].Name != "Other" || r.Previous != nil {
		t.Fatal("URL and snapshot not committed together")
	}
}

func TestUsageHeaderOnlyPublishesVerifiedNumericValues(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=12; download=34; total=1000; expire=1767225600; bearer=private")
		io.WriteString(w, basic)
	}))
	defer fixture.Close()
	s := OpenStore(t.TempDir())
	if e := s.Configure(Source{ID: "header", Kind: "external", Name: "public", URL: fixture.URL + "/token/hidden-value", UserAgent: "private custom UA"}); e != nil {
		t.Fatal(e)
	}
	if _, e := (Fetcher{}).Refresh(context.Background(), s, "header", nil, Policy{}); e != nil {
		t.Fatal(e)
	}
	st, e := s.Status("header")
	if e != nil || st.Usage == nil || st.Usage.Upload != 12 || st.Usage.Download != 34 || st.Usage.Total == nil || *st.Usage.Total != 1000 || st.Usage.Expires != "2026-01-01T00:00:00Z" {
		t.Fatalf("missing usage %+v", st.Usage)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "hidden-value") {
		t.Fatal("private request data in public summary")
	}
	for _, raw := range []string{"upload=1; download=-3", "upload=2; upload=3; download=4", "total=5"} {
		if parseUsage(raw) != nil {
			t.Fatalf("accepted ambiguous header %q", raw)
		}
	}
}

func prepareTestCandidate(sourceID string, body []byte, previous *Profile, policy Policy) (Candidate, error) {
	p, err := Parse(sourceID, body)
	if err != nil && !(errors.Is(err, errNoUsableNodes) && p.credentialIssue() != "") {
		return Candidate{}, err
	}
	return Assess(p, previous, policy), nil
}

// A killed helper's record write leaves a temporary file, which the next refresh removes.
func TestStoreSweepsSnapshotLeftovers(t *testing.T) {
	store, id := fileSource(t, RoutingOwn)
	leftover := filepath.Join(store.dir, ".snapshot-123")
	if err := os.WriteFile(leftover, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); err == nil {
		t.Fatal("leftover stayed")
	}
}
