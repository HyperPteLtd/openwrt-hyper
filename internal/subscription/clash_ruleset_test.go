package subscription

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"go.yaml.in/yaml/v3"
)

type setFixtureTransport func(*http.Request) (*http.Response, error)

func (f setFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const platformFixtureYAML = `mixed-port: 9347
dns: {enable: true}
proxies:
 - {name: Edge, type: http, server: edge.example, port: 8080, username: demo, password: fixture-only-password}
proxy-groups:
 - {name: Streaming, type: select, proxies: [Edge,DIRECT]}
rules:
 - DOMAIN,media.example,Streaming
 - DOMAIN,blocked.example,REJECT
 - MATCH,DIRECT
`

func TestMihomoProviderConvertsRemoteSetsAndKeepsRouting(t *testing.T) {
	data := []byte("payload:\n  - DOMAIN-SUFFIX,example.org\n  - IP-ASN,132203,no-resolve\n")
	converted := []byte("DOMAIN-SUFFIX,example.org\nIP-ASN,132203,no-resolve\n")
	h := sha256.Sum256(converted)
	const name = "Streaming"
	doc := []byte(`proxies:
 - {name: edge, type: http, server: edge.example, port: 443, tls: true}
proxy-groups:
 - {name: Download, type: fallback, proxies: [edge, DIRECT], url: https://example.org/test, interval: 180}
rule-providers:
  Streaming: {type: http, behavior: classical, format: yaml, path: ../../overwrite, url: 'https://public.example/Media.YAML?Case=Sensitive'}
rules:
 - DOMAIN,first.example,REJECT
 - RULE-SET,Streaming,Download
 - GEOSITE,CN,DIRECT
 - MATCH,Download
`)
	c := mustConvert(t, string(doc))
	s := providers{ctx: t.Context(), cache: map[string][]byte{}, f: Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.RequestURI() != "/Media.YAML?Case=Sensitive" || r.Header.Get("Authorization") != "" {
			t.Fatal("request modified or credential leaked")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
	})}}}
	if err := s.clash(c.doc); err != nil {
		t.Fatal(err)
	}
	p, e := parseVerified("account", c, s.binary)
	if e != nil || len(p.Notices) > 0 || len(p.Rules) != 3 || len(p.Groups) != 1 || p.Groups[0].Kind != "fallback" || p.Final != p.Groups[0].ID {
		t.Fatal(e, p.Notices)
	}
	if p.Rules[0].Target != Reject || p.Rules[1].RuleSet != name || p.Rules[1].Target != p.Groups[0].ID || p.Rules[2].RuleSet != DomesticDomainSet {
		t.Fatal("provider order or target changed")
	}
	set := p.BinarySets[0]
	if set.Filename() != hex.EncodeToString(h[:])+".list" || !bytes.Equal(set.Data, converted) || set.Entries != 2 || set.Behavior != "classical" {
		t.Fatal("provider content or path changed", string(set.Data))
	}
	if !slices.Contains(GeoRules(p), "IP-ASN,_") {
		t.Fatal("rule-provider ASN dependency omitted")
	}
	dir := filepath.Join(t.TempDir(), "sets")
	if err := MaterializeSets(p, dir); err != nil {
		t.Fatal(err)
	}
	set.Data[0] ^= 1
	if _, e := set.contents(); e == nil {
		t.Fatal("damaged snapshot accepted")
	}
}

func TestOfflineCNAndMRSFraming(t *testing.T) {
	for _, tt := range []struct{ family, behavior string }{{"geoip", "ipcidr"}, {"geosite", "domain"}} {
		data, e := os.ReadFile("../../files/usr/share/hyper/rules/" + tt.family + "-cn.mrs")
		if e != nil {
			t.Fatal(e)
		}
		set, e := ruleSet("cn", "mrs", tt.behavior, data)
		if e != nil || set.Entries < 500 {
			t.Fatal(tt.family, set.Entries, e)
		}
		other := "domain"
		if tt.behavior == "domain" {
			other = "ipcidr"
		}
		for _, bad := range [][]byte{data[:len(data)/2], []byte("SRS\x02")} {
			if _, e := ruleSet("bad", "mrs", tt.behavior, bad); e == nil {
				t.Fatal("bad MRS accepted")
			}
		}
		if _, e := ruleSet("bad", "mrs", other, data); e == nil {
			t.Fatal("wrong MRS behavior accepted")
		}
	}
}

func TestLocalNativeRuleFilesAreSourceScoped(t *testing.T) {
	f := Fetcher{ruleImportRoot: t.TempDir(), sourceID: "owner"}
	dir := filepath.Join(f.ruleImportRoot, "source-owner")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("example.org\n")
	outside := filepath.Join(t.TempDir(), "list.txt")
	os.WriteFile(outside, data, 0600)
	if _, err := f.localClashSet(outside); err == nil {
		t.Fatal("template selected local filesystem content")
	}
	safe := filepath.Join(dir, "list.txt")
	if err := os.Symlink(outside, safe); err != nil {
		t.Fatal(err)
	}
	if _, err := f.localClashSet(outside); err == nil {
		t.Fatal("sidecar symlink followed")
	}
	os.Remove(safe)
	os.WriteFile(safe, data, 0600)
	got, err := f.localClashSet("./sets/list.txt")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
	f.sourceID = "neighbor"
	if _, err := f.localClashSet(outside); err == nil {
		t.Fatal("read sidecar from other source")
	}
}

func TestOldSnapshotsInvalidateButPlanAndTicketSurvive(t *testing.T) {
	s := OpenStore(t.TempDir())
	id := "account"
	src := Source{ID: id, Kind: "platform", Origin: "https://example.org", Token: "fixture", UserID: "u", PlanID: 7}
	old := Profile{SchemaVersion: 1, Source: id, Nodes: []Node{{Name: "stale-node", Outbound: map[string]any{"password": "stale-password"}}}}
	legacy := record{SchemaVersion: 1, Epoch: 1, Generation: 3, Source: id, Current: &old, Previous: &old, Config: &src, bookkeeping: bookkeeping{Providers: &providerReceipt{Key: "old-cache"}}}
	raw, _ := json.Marshal(legacy)
	os.WriteFile(s.path(id), raw, 0600)
	sts, e := s.List()
	if e != nil || len(sts) != 1 || !sts[0].NeedsRefresh || sts[0].Ready || sts[0].HasHistory {
		t.Fatal(e, sts)
	}
	if _, e := s.Load(id); !errors.Is(e, ErrNotReady) {
		t.Fatal(e)
	}
	after, e := s.raw(id)
	if e != nil || after.Config.Token != "fixture" || after.SchemaVersion != SchemaVersion || after.Epoch <= legacy.Epoch || after.Providers != nil {
		t.Fatal("source migration lost account or authorization fence")
	}
	raw, _ = os.ReadFile(s.path(id))
	if bytes.Contains(raw, []byte("stale-password")) {
		t.Fatal("incompatible snapshot remained on disk")
	}
	ticket, e := s.Begin(id)
	if e != nil {
		t.Fatal(e)
	}
	p, e := Parse(id, []byte(platformFixtureYAML))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Commit(ticket, Assess(p, nil, Policy{})); e != nil {
		t.Fatal(e)
	}
	st, _ := s.Status(id)
	if st.NeedsRefresh || !st.Ready {
		t.Fatal("refresh didn't complete migration")
	}
}

func TestGeodataPinnedOfflineAndInterruptedDownload(t *testing.T) {
	dir := t.TempDir()
	data := []byte("verified fixture")
	h := sha256.Sum256(data)
	u := "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/" + strings.Repeat("a", 40) + "/geosite.dat"
	manifest := filepath.Join(dir, "manifest")
	os.WriteFile(manifest, []byte(fmt.Sprintf("%x %d GeoSite.dat %s\n", h, len(data), u)), 0644)
	file := filepath.Join(dir, "GeoSite.dat")
	os.WriteFile(file, data, 0644)
	if err := PrepareGeodata(t.Context(), []string{"AND,((GEOSITE,company),(NETWORK,TCP)),DIRECT"}, dir, manifest); err != nil {
		t.Fatal("verified cache not usable offline", err)
	}
	p := geoPin{hex.EncodeToString(h[:]), int64(len(data)), "GeoSite.dat", u}
	client := &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("damaged"))}, nil
	})}
	if e := acquireGeo(t.Context(), client, file, p); e == nil {
		t.Fatal("digest mismatch accepted")
	}
	if !pinnedFile(file, p) {
		t.Fatal("failed download replaced good file")
	}
}

func TestMihomoDropsUnsupportedSRSAndPreservesNeighborRules(t *testing.T) {
	body := []byte(`{"outbounds":[{"type":"http","tag":"n","server":"n.example","server_port":80}],"route":{"rule_set":[{"tag":"unavailable","type":"remote","format":"binary","url":"https://127.0.0.1/private.srs"}],"rules":[{"domain":"kept.example","outbound":"n"},{"rule_set":"unavailable","action":"reject"}],"final":"n"}}`)
	p, e := Parse("s", body)
	if e != nil || len(p.Rules) != 1 || len(p.BinarySets) != 0 {
		t.Fatal(e, p.Rules)
	}
	c := Assess(p, nil, Policy{AllowLocalFallback: true})
	if c.Decision != "confirmation_required" || !c.RetainsRouting || len(c.Notices) != 2 || c.Notices[0].Code != "subconv_unsupported_rule_set" || c.Notices[1].Code != "subconv_unsupported_rule" {
		t.Fatal(c.Decision, c.Notices)
	}
}

func TestOversizedMRSLengthRejectedBeforeAllocation(t *testing.T) {
	payload := []byte{'M', 'R', 'S', 1, 1}
	payload = binary.BigEndian.AppendUint64(payload, 1)
	payload = binary.BigEndian.AppendUint64(payload, 0)
	payload = append(payload, 1)
	payload = binary.BigEndian.AppendUint64(payload, 1<<60)
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	defer enc.Close()
	data := enc.EncodeAll(payload, nil)
	if _, e := MRSEntries(data, "ipcidr"); e == nil {
		t.Fatal("accepted impossible number of ranges")
	}
}

func TestCoreFeaturesAreCheckedPerVariant(t *testing.T) {
	no := false
	caps := CoreFeatures{Control: "mihomo-rest-v1", Naive: &no, Protocols: map[string]bool{"nowhere": false, "snell-v6": true, "anytls-reality": false}}
	cases := []struct {
		node Node
		want string
	}{
		{Node{Protocol: "naive"}, "unsupported"}, {Node{Protocol: "nowhere"}, "unsupported"}, {Node{Protocol: "snell", Outbound: map[string]any{"version": 6}}, ""},
		{Node{Protocol: "anytls"}, ""}, {Node{Protocol: "anytls", Outbound: map[string]any{"reality-opts": map[string]any{"public-key": "test"}}}, "unsupported"},
	}
	for _, tt := range cases {
		key := tt.node.CapabilityKey()
		if got := caps.Availability(key); got != tt.want {
			t.Fatalf("%s = %s", key, got)
		}
		if key != "" && (CoreFeatures{}).Availability(key) != "unverified" {
			t.Fatal("uninstalled kernel falsely resolved capabilities")
		}
	}
}

func TestBundledRulesAndConvertedPredicateWithSelectedMihomo(t *testing.T) {
	bin := os.Getenv("HYPER_TEST_MIHO")
	if bin == "" {
		t.Skip("set HYPER_TEST_MIHO to the selected Mihomo executable")
	}
	dir := t.TempDir()
	defs := map[string]any{}
	for _, tt := range []struct{ tag, family, behavior string }{{DomesticIPSet, "geoip", "ipcidr"}, {DomesticDomainSet, "geosite", "domain"}} {
		name := tt.family + "-cn.mrs"
		b, e := os.ReadFile("../../files/usr/share/hyper/rules/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
		defs[tt.tag] = map[string]any{"type": "file", "format": "mrs", "behavior": tt.behavior, "path": "./" + name}
	}
	matcher := "AND,((OR,((DOMAIN-SUFFIX,example.test),(IP-CIDR,192.0.2.0/24))),(NETWORK,TCP),(DST-PORT,443))"
	node := map[string]any{"name": "example", "type": "http", "server": "edge.example", "port": 8080, "username": "user", "password": "password"}
	cfg := map[string]any{"mode": "rule", "mixed-port": 0, "proxies": []map[string]any{node}, "rule-providers": defs, "rules": []string{"RULE-SET," + DomesticIPSet + ",DIRECT,no-resolve", "RULE-SET," + DomesticDomainSet + ",DIRECT", matcher + ",example", "MATCH,REJECT"}}
	b, _ := yaml.Marshal(cfg)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if result, e := exec.CommandContext(ctx, bin, "-t", "-d", dir, "-f", path).CombinedOutput(); e != nil {
		t.Fatalf("Mihomo rejected offline providers: %s", result)
	}
}
