package subscription

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderCaseKeepsSignedPathAndQuery(t *testing.T) {
	const path = "/A%2fb/Rules/AS132203.mrs?Token=aB+C%2fZ&Signature=ABca"
	want := "https://rules.example:9443" + path
	if got := providerAddress("HTTPS://RuLeS.Example:9443" + path); got != want {
		t.Fatalf("%q", got)
	}
	doc := map[string]any{"proxy-providers": map[string]any{"N": map[string]any{"url": "https://RuLeS.Example:9443" + path}}, "rule-providers": map[string]any{"X": map[string]any{"url": want}}}
	normalizeProviderAddresses(doc)
	if field(object(object(doc["proxy-providers"])["N"]), "url") != field(object(object(doc["rule-providers"])["X"]), "url") {
		t.Fatal("provider aliases were not canonicalized")
	}
}

func TestPartialRulesKeepOtherRoutesAndRequireConsentForRejection(t *testing.T) {
	src := mustParse(t, primary)
	src.notice("mihomo_unsupported_rule_set", "missing set", false)
	c := Assess(src, nil, Policy{})
	if c.Decision != "ready" || c.Profile.Local || len(c.Profile.Rules) != 2 || len(c.Profile.Groups) != 1 {
		t.Fatal(c.Decision, c.Profile.Local)
	}
	src.notice("rule_provider_reference_unavailable", "rule 5", true)
	c = Assess(src, nil, Policy{AllowLocalFallback: true})
	if c.Decision != "confirmation_required" || !c.RetainsRouting || c.ConfirmationID == "" {
		t.Fatal(c.Decision)
	}
	c = Assess(src, nil, Policy{Confirm: c.ConfirmationID})
	if c.Decision != "ready" || c.Profile.Local || len(c.Profile.Rules) != 2 {
		t.Fatal(c.Decision)
	}
	src.notice("script", "script", true)
	c = Assess(src, nil, Policy{})
	if c.RetainsRouting || c.Decision != "confirmation_required" {
		t.Fatal("unrepresentable scripts must use ordinary loss policy")
	}
}

func TestClashHiddenPreservesRoutedGroup(t *testing.T) {
	p := mustParse(t, `{"proxies":[{"type":"http","name":"test","server":"proxy.example","port":443}],"proxy-groups":[{"name":"Internal","type":"select","hidden":true,"proxies":["test"]}],"rules":["DOMAIN,example.test,Internal","MATCH,DIRECT"]}`)
	if len(p.Notices) != 0 || !p.Summary().Groups[0].Hidden || p.Rules[0].Target != p.Groups[0].ID {
		t.Fatal("hidden group lost")
	}
}

func TestScheduledProvidersExpireAndManualRefreshFetches(t *testing.T) {
	var hits atomic.Int32
	var body []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plan" {
			w.Write(body)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, "payload:\n  - sample.example\n")
	}))
	defer server.Close()
	body = ruleFixture(server.URL, 1)
	st := OpenStore(t.TempDir())
	src := Source{ID: "scheduled", Name: "test", Kind: "external", URL: server.URL + "/plan"}
	if err := st.Configure(src); err != nil {
		t.Fatal(err)
	}
	f := Fetcher{Client: server.Client()}
	check := func(scheduled bool, want int32) {
		t.Helper()
		c, e := f.Refresh(t.Context(), st, src.ID, nil, Policy{Scheduled: scheduled})
		if e != nil || c.Decision != "ready" || hits.Load() != want {
			t.Fatalf("status=%s err=%v hits=%d want=%d", c.Decision, e, hits.Load(), want)
		}
	}
	check(false, 1)
	check(true, 1)
	check(false, 2)
	if e := st.update(src.ID, func(r *record) error { r.Providers.Expires = time.Now().Add(-time.Hour); return nil }); e != nil {
		t.Fatal(e)
	}
	check(true, 3)
	body = []byte(strings.Replace(string(body), "set0", "set1", -1))
	check(true, 4)
}

func TestGeoNamesUseDatasetCaseAndRespectCustomFeeds(t *testing.T) {
	if !clashDefaultGeo(map[string]any{"geox-url": map[string]any{"geoip": "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geoip.dat"}}, "GEOIP") {
		t.Fatal("official geodata not recognized")
	}
	if clashDefaultGeo(map[string]any{"geox-url": map[string]any{"geoip": "https://example.test/MetaCubeX/meta-rules-dat@release/geoip.dat"}}, "GEOIP") {
		t.Fatal("custom geodata silently substituted")
	}
}
