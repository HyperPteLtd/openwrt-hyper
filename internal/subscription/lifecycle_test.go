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
	p := parseWithSets(t, "source", "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nproxy-groups: [{name: Streaming, type: select, proxies: [Edge, DIRECT]}]\nrules: ['RULE-SET,region,Streaming', 'MATCH,DIRECT']", textSet("region", "domain", string(raw)))
	if len(p.Notices) != 0 || p.Rules[0].RuleSet != "region" || len(p.BinarySets) != 1 || !bytes.Equal(p.BinarySets[0].Data, raw) {
		t.Fatal(p.Notices)
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
	if e := MaterializeSets(p, dir); e != nil {
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
	s, fd := fixtureSource(t, "source", basic)
	f := Fetcher{}
	if _, e := f.Refresh(t.Context(), s, "source", Policy{}); e != nil {
		t.Fatal(e)
	}
	f.BeforeCommit = func(_ context.Context, p Profile) (func() error, error) {
		if p.Source != "source" {
			t.Fatal("wrong validated source")
		}
		return func() error { return errors.New("stale_policy") }, nil
	}
	fd.body = "http://new.example:8080"
	_, e := f.Refresh(t.Context(), s, "source", Policy{})
	if e == nil {
		t.Fatal("failed validation committed")
	}
	current, e := s.Load("source")
	if e != nil || current.Nodes[0].Name != "Edge" {
		t.Fatal("last known profile overwritten", e)
	}
}

func TestSnellV5TLSObfuscationParses(t *testing.T) {
	p := mustParse(t, "snell://strong-password@host.example:443?version=5&plugin=obfs-local;obfs%3Dtls")
	if object(p.Nodes[0].Outbound["obfs-opts"])["mode"] != "tls" {
		t.Fatal("missing TLS obfuscation")
	}
}
