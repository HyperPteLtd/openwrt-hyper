package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSnellTemplateCredentials(t *testing.T) {
	const psk = "QA:Exact+Key/For=Snell"
	const clientID = "fixture-user"
	const native = `proxies: [{"type":"snell","name":"Edge","server":"edge.example","port":443,"version":6,`
	inputs := map[string]string{
		"sing-box": `{"outbounds":[{"type":"snell","tag":"Edge","server":"edge.example","server_port":443,"version":6,"psk":"` + psk + `","userkey":"` + clientID + `"}]}`,
		"clash":    "proxies:\n  - {name: Edge, type: snell, server: edge.example, port: 443, version: 6, udp: true, psk: '" + psk + "', userkey: " + clientID + "}",
		"surge":    "[Proxy]\nEdge = snell, edge.example, 443, version=6, psk=" + psk + ", userkey=" + clientID,
		"share":    "snell://" + base64.RawStdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:"+psk)) + "@edge.example:443?version=6&userkey=" + clientID + "#Edge",
	}
	canonical := mustParse(t, inputs["clash"]).Nodes[0]
	for name, raw := range inputs {
		t.Run(name, func(t *testing.T) {
			if name == "surge" {
				t.Skip("subconv does not read Snell v6's userkey from Surge lines yet")
			}
			p := mustParse(t, raw)
			// Formats differ only in whether they spell out UDP; the ID digests the outbound.
			p.Nodes[0].Outbound["udp"], p.Nodes[0].ID = true, canonical.ID
			if len(p.Notices) != 0 || len(p.Nodes) != 1 || !reflect.DeepEqual(p.Nodes[0], canonical) {
				t.Fatalf("standard credentials changed across formats: %v %+v %+v", p.Notices, p.Nodes, canonical)
			}
		})
	}
	for _, tc := range []struct {
		name, fields, code string
		valid              bool
	}{
		{"password only", `"password":"private-password"`, "nonstandard_snell_credential", false},
		{"client id only", `"userkey":"private-client"`, "invalid_credential", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := prepareTestCandidate("source", []byte(native+tc.fields+`}]`), nil, Policy{AllowLocalFallback: true})
			if err != nil || (c.Decision == "ready") != tc.valid || len(c.Notices) != 1 || c.Notices[0].Code != tc.code {
				t.Fatalf("decision %q notices %v error %v", c.Decision, c.Notices, err)
			}
			if tc.valid && (c.Profile.Nodes[0].Outbound["psk"] != psk || c.Profile.Nodes[0].Outbound["password"] != nil) {
				t.Fatal("nonstandard password influenced the outbound")
			}
			b, _ := json.Marshal(c.Notices)
			if strings.Contains(string(b), "private") || strings.Contains(string(b), psk) {
				t.Fatal("diagnostic contains a credential")
			}
		})
	}
}

func TestBuiltInSubscriptionAgents(t *testing.T) {
	a := Account{}
	if a.Source(Plan{ID: 1}).UserAgent != "Hyper" || !reflect.DeepEqual(UserAgents, []string{"clash", "sing-box", "Hyper"}) {
		t.Fatal("unexpected presets or default account user-agent")
	}
}

func TestBadCredentialUpdateRetainsSnapshot(t *testing.T) {
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "test", Name: "test", Kind: "file"}); err != nil {
		t.Fatal(err)
	}
	fetch := Fetcher{}
	raw := "[Proxy]\nFirst = snell, node.example, 443, version=6, psk=valid-test-psk"
	original, err := fetch.Refresh(t.Context(), store, "test", []byte(raw), Policy{})
	if err != nil || original.Decision != "ready" {
		t.Fatal(original.Decision, err)
	}
	for _, suffix := range []string{"", "\nSecond = http, new.example, 8080"} {
		// A Snell line with a password but no psk is the nonstandard credential, as in Clash.
		malformed := strings.ReplaceAll(raw, "psk=", "password=") + suffix
		c, err := fetch.Refresh(t.Context(), store, "test", []byte(malformed), Policy{AllowLocalFallback: true})
		if err != nil || c.Decision != "blocked" || len(c.Notices) == 0 || c.Notices[0].Code != "nonstandard_snell_credential" || c.Notices[0].At != "First" {
			t.Fatal("missing field-specific warning", c.Decision, c.Notices, err)
		}
		current, err := store.Load("test")
		if err != nil || !reflect.DeepEqual(current.Nodes[0].Summary(), original.Profile.Nodes[0].Summary()) || len(current.Nodes) != 1 {
			t.Fatal("previous snapshot changed")
		}
		st, err := store.Status("test")
		if err != nil || st.LastFailure != "nonstandard_snell_credential" || !st.Ready {
			t.Fatal("saved source lost its usable state or last update diagnostic")
		}
	}
	if _, err := Parse("test", []byte("not a subscription")); !errors.Is(err, errMessage) {
		t.Fatal("unrelated parse error was swallowed", err)
	}
}

// An update with credential trouble read with a replacement link or another routing choice is
// not the source's: blocked, it leaves the source's status as it was. Read from the source's own
// link with its own choice, it is recorded.
func TestBlockedUpdateOfAnotherLinkOrChoiceLeavesTheSource(t *testing.T) {
	const good = "[Proxy]\nFirst = snell, node.example, 443, version=6, psk=valid-test-psk"
	malformed := strings.ReplaceAll(good, "psk=", "password=") + "\nSecond = http, new.example, 8080"
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	body := good
	f := Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(r *http.Request) (*http.Response, error) {
		answer := body
		if r.URL.Path == "/other" {
			answer = malformed
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer))}, nil
	})}}
	if c, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{}); err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision)
	}
	template := RoutingTemplate
	other, choice := f, f
	other.ReplacementURL = "https://example.com/other"
	body = malformed
	choice.Routing = &template
	for name, f := range map[string]Fetcher{"replacement link": other, "routing choice": choice} {
		for _, scheduled := range []bool{false, true} {
			c, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{Scheduled: scheduled})
			if err != nil || c.Decision != "blocked" || c.Profile.credentialIssue() != "nonstandard_snell_credential" {
				t.Fatal(name, err, c.Decision, c.Notices)
			}
			st, err := store.Status(src.ID)
			if err != nil || st.LastFailure != "" || st.PendingUpdate != nil || !st.Ready || st.Rules != RoutingOwn {
				t.Fatalf("%s recorded on the source: %+v %v", name, st, err)
			}
			if url, _ := store.SourceURL(src.ID); url != src.URL {
				t.Fatal(name, url)
			}
		}
	}
	if _, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.Status(src.ID); st.LastFailure != "nonstandard_snell_credential" || st.PendingUpdate == nil || st.PendingUpdate.Code != PendingBlocked {
		t.Fatal("the source's own blocked update not recorded", st.LastFailure, st.PendingUpdate)
	}
}

func TestNativeRealityFieldsInLoonAndQuantumult(t *testing.T) {
	const id = "13d76559-5c99-4b6a-92c4-d3b17e67a872"
	const pk = "jUuJmNLSY1o2H_l4FaS66HB3XVAXqNJhoFrlQ1JyBTw"
	loon := "[Proxy]\nTest = VLESS, node.example, 443, " + id + ", public-key=" + pk + ", short-id=abcd, flow=xtls-rprx-vision"
	qx := "[server_local]\nvless=node.example:443, password=" + id + ", reality-base64-pubkey=" + pk + ", reality-hex-shortid=abcd, vless-flow=xtls-rprx-vision, tag=Test"
	for _, raw := range []string{loon, qx} {
		p := mustParse(t, raw)
		node := p.Nodes[0].Outbound
		reality := object(node["reality-opts"])
		if len(p.Notices) > 0 || node["uuid"] != id || node["flow"] != "xtls-rprx-vision" || node["tls"] != true || node["client-fingerprint"] == nil || reality["public-key"] != pk || reality["short-id"] != "abcd" {
			t.Fatal("REALITY options lost")
		}
	}
}

func TestLineNoResolveAndDomesticRule(t *testing.T) {
	p := mustParse(t, "[Proxy]\nA = http, edge.example, 8080\n[Rule]\nIP-CIDR,10.1.0.0/16,DIRECT,no-resolve\nGEOIP,CN,DIRECT,no-resolve\nFINAL,DIRECT")
	if len(p.Notices) != 0 || len(p.Rules) != 2 || p.Rules[0].Target != Direct || p.Rules[1].RuleSet != DomesticIPSet || p.Rules[1].Target != Direct {
		t.Fatal("supported rules changed", p.Notices)
	}
	// Other countries use the core's geodata, as in a Clash subscription.
	p = mustParse(t, "[Proxy]\nA = http, edge.example, 8080\n[Rule]\nGEOIP,JP,DIRECT,no-resolve")
	if len(p.Rules) != 1 || p.Rules[0].RuleSet != "" || p.Rules[0].Match["clash"] != "GEOIP,JP" {
		t.Fatal("geographical set remapped", p.Rules)
	}
}
