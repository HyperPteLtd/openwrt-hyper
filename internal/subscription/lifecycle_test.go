package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestRevocationRetainsSharedRuleProviderUntilLastSourceRemoved(t *testing.T) {
	raw := []byte("example.test\n")
	c := mustConvert(t, "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nproxy-groups: [{name: Streaming, type: select, proxies: [Edge, DIRECT]}]\nrule-providers: {region: {type: inline, behavior: domain, payload: [example.test]}}\nrules: ['RULE-SET,region,Streaming', 'MATCH,DIRECT']")
	s := providers{ctx: t.Context(), cache: map[string][]byte{}}
	if e := s.clash(c.doc); e != nil {
		t.Fatal(e)
	}
	p, e := parseVerified("source", c, s.binary)
	if e != nil || len(p.Notices) != 0 || p.Rules[0].RuleSet != "region" || len(p.BinarySets) != 1 || !bytes.Equal(p.BinarySets[0].Data, raw) {
		t.Fatal(e, p.Notices)
	}
	set := p.BinarySets[0]
	if public, _ := json.Marshal(p.Summary()); bytes.Contains(public, []byte(set.SHA256)) || bytes.Contains(public, []byte(base64.StdEncoding.EncodeToString(raw))) {
		t.Fatal("private binary leaked")
	}
	store := OpenStore(filepath.Join(t.TempDir(), "sources"))
	dir := filepath.Join(t.TempDir(), "sets")
	for _, id := range []string{"source", "two"} {
		tick, e := store.Begin(id)
		if e != nil {
			t.Fatal(e)
		}
		v := p
		v.Source = id
		if e = store.Commit(tick, Candidate{Profile: v, Decision: "ready"}); e != nil {
			t.Fatal(e)
		}
	}
	if e = MaterializeSets(p, dir); e != nil {
		t.Fatal(e)
	}
	f := filepath.Join(dir, set.Filename())
	st, e := os.Stat(f)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(e)
	}
	if e = store.Revoke("source"); e != nil {
		t.Fatal(e)
	}
	if e = store.CollectSets(dir); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(f); e != nil {
		t.Fatal("shared set removed", e)
	}
	if e = store.Revoke("two"); e != nil {
		t.Fatal(e)
	}
	if e = store.CollectSets(dir); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(f); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("unreferenced set persists")
	}
	p.BinarySets[0].SHA256 = hex.EncodeToString(make([]byte, 32))
	if e = MaterializeSets(p, dir); e == nil {
		t.Fatal("accepted changed data")
	}
}
func TestLoginFenceAcrossAllPlans(t *testing.T) {
	a := Account{origin: "https://platform.example", userID: "user", token: "private-bearer", Site: Site{Name: "Platform"}, Plans: []Plan{{ID: 1, Name: "One", Usable: true, Status: "active"}, {ID: 2, Name: "Two", Usable: true, Status: "active"}}}
	s := OpenStore(t.TempDir())
	t1, e := s.BeginLogin(a.origin)
	if e != nil {
		t.Fatal(e)
	}
	ids, e := s.SaveAccount(a, t1)
	if e != nil || len(ids) != 2 {
		t.Fatal(e)
	}
	for _, id := range ids {
		ticket, e := s.Begin(id)
		if e != nil {
			t.Fatal(e)
		}
		p := mustParse(t, basic)
		p.Source = id
		if e = s.Commit(ticket, Candidate{Profile: p, Decision: "ready"}); e != nil {
			t.Fatal(e)
		}
	}
	intent, _ := s.BeginLogin(a.origin)
	changed, e := s.Logout(ids[0])
	if e != nil || len(changed) != 2 {
		t.Fatal(changed, e)
	}
	if _, e = s.SaveAccount(a, intent); !errors.Is(e, ErrSuperseded) {
		t.Fatal("late login resurrected account", e)
	}
	for _, id := range ids {
		if _, e = s.Load(id); !errors.Is(e, ErrRevoked) {
			t.Fatal(e)
		}
		b, _ := os.ReadFile(s.path(id))
		if bytes.Contains(b, []byte(a.token)) || bytes.Contains(b, []byte("fixture-only-password")) {
			t.Fatal("credentials retained")
		}
	}
	intent, _ = s.BeginLogin(a.origin)
	if _, e = s.SaveAccount(a, intent); e != nil {
		t.Fatal(e)
	}
	for _, id := range ids {
		if _, e = s.Load(id); !errors.Is(e, ErrNotReady) {
			t.Fatal("old snapshots returned")
		}
	}
	a.Plans = a.Plans[:1]
	intent, _ = s.BeginLogin(a.origin)
	_, e = s.SaveAccount(a, intent)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load(ids[1]); !errors.Is(e, ErrRevoked) {
		t.Fatal("new login restored withdrawn plan", e)
	}
}
func TestVersionedSnellAndMieruStandard(t *testing.T) {
	v5 := mustParse(t, "snell://strong-password@host.example:443?version=5&plugin=obfs-local;obfs%3Dhttp")
	if v5.Nodes[0].Outbound["version"] != 5 || object(v5.Nodes[0].Outbound["obfs-opts"])["mode"] != "http" {
		t.Fatal(v5)
	}
	if _, e := Parse("s", []byte("snell://strong-password@host.example:443?version=6&obfs=http")); e == nil {
		t.Fatal("v6 silently used v5 obfs")
	}
	// Standard, upstream-schema Mieru protobuf and its published simple equivalent.
	msg := func(k protowire.Number, v []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, k, protowire.BytesType), v)
	}
	num := func(k protowire.Number, v uint64) []byte {
		return protowire.AppendVarint(protowire.AppendTag(nil, k, protowire.VarintType), v)
	}
	user := append(msg(1, []byte("demo")), msg(2, []byte("pass"))...)
	bind := append(num(1, 443), num(2, 1)...)
	server := append(msg(2, []byte("edge.example")), msg(3, bind)...)
	profile := append(msg(1, []byte("edge")), msg(2, user)...)
	profile = append(profile, msg(3, server)...)
	full := mustParse(t, "mieru://"+base64.StdEncoding.EncodeToString(msg(1, profile)))
	simple := mustParse(t, "mierus://demo:pass@edge.example?profile=edge&port=443&protocol=UDP")
	if full.Nodes[0].ID != simple.Nodes[0].ID || full.Nodes[0].Outbound["transport"] != "UDP" {
		t.Fatal(full, simple)
	}
}
func TestAutomaticLocalFallbackCannotRemovePreviouslyRetainedReject(t *testing.T) {
	p := mustParse(t, primary)
	p.Notices = []Notice{{Code: "unsupported_group_option", At: "group"}}
	c := Assess(p, nil, Policy{AllowLocalFallback: true})
	if c.Decision != "confirmation_required" {
		t.Fatal(c.Decision)
	}
}
func TestValidationFenceKeepsRunningSnapshot(t *testing.T) {
	s := OpenStore(t.TempDir())
	if e := s.Configure(Source{ID: "source", Name: "Source", Kind: "file"}); e != nil {
		t.Fatal(e)
	}
	f := Fetcher{}
	if _, e := f.Refresh(t.Context(), s, "source", []byte(basic), Policy{}); e != nil {
		t.Fatal(e)
	}
	f.BeforeCommit = func(_ context.Context, p Profile) (func() error, error) {
		if p.Source != "source" {
			t.Fatal("wrong validated source")
		}
		return func() error { return errors.New("stale_policy") }, nil
	}
	_, e := f.Refresh(t.Context(), s, "source", []byte("http://new.example:8080"), Policy{})
	if e == nil {
		t.Fatal("failed validation committed")
	}
	current, e := s.Load("source")
	if e != nil || current.Nodes[0].Name != "Edge" {
		t.Fatal("last known profile overwritten", e)
	}
}

func TestInlineRuleAndNodeProvidersKeepGroupAndSet(t *testing.T) {
	source := Source{ID: "fixture", Name: "Fixture", Kind: "file"}
	store := OpenStore(t.TempDir())
	if err := store.Configure(source); err != nil {
		t.Fatal(err)
	}
	body := []byte("proxy-providers:\n  rem:\n    type: inline\n    payload: [{type: http, name: HK, server: hk.example.org, port: 80}]\nproxy-groups: [{name: Media, type: select, use: [rem]}]\nrule-providers:\n  local:\n    type: inline\n    behavior: domain\n    payload: [example.org, +.office.example.org]\nrules: [\"RULE-SET,local,Media\", \"MATCH,DIRECT\"]")
	c, err := (Fetcher{}).Refresh(context.Background(), store, source.ID, body, Policy{})
	if err != nil || c.Decision != "ready" || len(c.Profile.Nodes) != 1 || len(c.Profile.Groups) != 1 || len(c.Profile.Rules) != 1 || len(c.Profile.BinarySets) != 1 || c.Profile.BinarySets[0].Entries != 2 {
		t.Fatalf("%v, decision %s, notices %v", err, c.Decision, c.Notices)
	}
	for _, rule := range c.Profile.Rules {
		if rule.RuleSet != "local" || rule.Target != c.Profile.Groups[0].ID {
			t.Fatal("provider identity or target lost")
		}
	}
}

func TestSnellV5TLSObfuscationParsesInEveryForm(t *testing.T) {
	for _, raw := range []string{"snell://strong-password@host.example:443?version=5&plugin=obfs-local;obfs%3Dtls", "[Proxy]\nExample = snell, host.example, 443, psk=strong-password, version=5, obfs=tls"} {
		p := mustParse(t, raw)
		if object(p.Nodes[0].Outbound["obfs-opts"])["mode"] != "tls" {
			t.Fatal("missing TLS obfuscation", raw)
		}
	}
}
