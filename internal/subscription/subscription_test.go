package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const basic = "http://demo:fixture-only-password@edge.example:8080#Edge"

// primary is basic's node in a Clash document, JSON this time, with a group and rules.
const primary = `{"proxies":[{"name":"Edge","type":"http","server":"edge.example","port":8080,"username":"demo","password":"fixture-only-password"}],"proxy-groups":[{"name":"Streaming","type":"select","proxies":["Edge","DIRECT"]}],"rules":["DOMAIN,media.example,Streaming","DOMAIN,blocked.example,REJECT","MATCH,DIRECT"]}`

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
			if len(p.Nodes) != 1 || p.Nodes[0].Protocol != c.protocol || p.Nodes[0].Outbound[c.key] == nil || p.Nodes[0].Special != (c.protocol == "mtproxy") || p.Format != "uri" {
				t.Fatal(c.protocol, p.Notices)
			}
		})
	}
}

// A node is the same node whatever its displayed name and whether it came as a share link or
// in a Clash document, YAML or JSON.
func TestInputVariantsAndIdentity(t *testing.T) {
	p := mustParse(t, primary)
	n := mustParse(t, basic).Nodes[0]
	uriRenamed := mustParse(t, strings.ReplaceAll(basic, "#Edge", "#renamed"))
	if n.ID != uriRenamed.Nodes[0].ID || n.ID != p.Nodes[0].ID || p.Format != "clash" {
		t.Fatal("identity depends on displayed name or format")
	}
	for _, doc := range []string{"\uFEFF" + basic, "\uFEFF" + primary,
		`proxies:
 - {name: Edge, type: http, server: edge.example, port: 8080, username: demo, password: fixture-only-password}
proxy-groups:
 - {name: Streaming, type: select, proxies: [Edge, DIRECT]}
rules:
 - DOMAIN,media.example,Streaming
 - MATCH,DIRECT`} {
		p := mustParse(t, doc)
		if len(p.Notices) != 0 || len(p.Nodes) != 1 || p.Nodes[0].ID != n.ID {
			t.Fatal(p.Format, p.Notices)
		}
	}
	for _, doc := range []string{`{"version":1,"servers":[{"server":"edge.example"}]}`, `[{"server":"edge.example"}]`, "<html>Access denied</html>", "", base64.StdEncoding.EncodeToString([]byte(basic)),
		"vless://x@node.test:443?security=unknown", "vector://01@node.test:443"} {
		if _, e := Parse("source", []byte(doc)); e == nil {
			t.Fatalf("accepted invalid format %s", doc)
		}
	}
	if _, e := Parse("source", make([]byte, MaxBody+1)); e == nil || e.Error() != "subscription_size_limit" {
		t.Fatal(e)
	}
}
func TestProfileSummaryOmitsPrivateNodeDetails(t *testing.T) {
	raw, err := json.Marshal(mustParse(t, primary).Summary())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-only-password") || strings.Contains(string(raw), "edge.example") {
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
	if e := s.Revoke("source"); e != nil {
		t.Fatal(e)
	}
	fresh := OpenStore(s.dir)
	if _, e := fresh.Load("source"); !errors.Is(e, ErrRevoked) {
		t.Fatal(e)
	}
	if !errors.Is(fresh.Rollback("source"), ErrRevoked) || !errors.Is(fresh.Commit(b, c), ErrRevoked) {
		t.Fatal("recovered revoked source")
	}
	if _, e := fresh.Begin("source"); !errors.Is(e, ErrRevoked) {
		t.Fatal("refresh of a revoked source began", e)
	}
	body, e := os.ReadFile(s.path("source"))
	if e != nil || strings.Contains(string(body), "fixture-only-password") {
		t.Fatal("revoked secrets persisted")
	}
}

// A read that fails keeps the profile the router runs, and the source's status names only
// the failure's code.
func TestFailureRetainsSnapshot(t *testing.T) {
	store, fd := fixtureSource(t, "sub", basic)
	f := Fetcher{}
	if _, e := f.Refresh(t.Context(), store, "sub", Policy{}); e != nil {
		t.Fatal(e)
	}
	fd.reachable, fd.failure = false, errors.New("locations_http_502")
	if _, e := f.Refresh(t.Context(), store, "sub", Policy{}); e == nil || e.Error() != "locations_http_502" {
		t.Fatal(e)
	}
	p, e := store.Load("sub")
	if e != nil || len(p.Nodes) != 1 {
		t.Fatal(e)
	}
	st, _ := store.Status("sub")
	if !st.Ready || st.LastFailure != "locations_http_502" {
		t.Fatal(st)
	}
	// A failure that is no code is recorded as update_failed.
	fd.failure = errors.New("Something: went wrong at https://x.example/?token=private")
	f.Refresh(t.Context(), store, "sub", Policy{})
	list, _ := store.List()
	b, _ := json.Marshal(list)
	if st, _ := store.Status("sub"); st.LastFailure != "update_failed" || strings.Contains(string(b), "private") {
		t.Fatal(st.LastFailure, string(b))
	}
}

// A killed helper's record write leaves a temporary file, which the next refresh removes.
func TestStoreSweepsSnapshotLeftovers(t *testing.T) {
	store, _ := fixtureSource(t, "sub", basic)
	leftover := filepath.Join(store.dir, ".snapshot-123")
	if err := os.WriteFile(leftover, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("sub"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); err == nil {
		t.Fatal("leftover stayed")
	}
}
