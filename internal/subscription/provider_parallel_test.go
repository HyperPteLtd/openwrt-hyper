package subscription

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ruleFixture(origin string, n int) []byte {
	var b strings.Builder
	b.WriteString("proxies:\n  - {name: test, type: http, server: example.test, port: 80}\nproxy-groups:\n  - {name: Proxy, type: select, proxies: [test]}\nrule-providers:\n")
	for i := range n {
		fmt.Fprintf(&b, "  set%d: {type: http, behavior: domain, format: yaml, interval: 86400, url: '%s/api/v1/app/proxy/subscriptions/rulesets/set%d/source'}\n", i, origin, i)
	}
	b.WriteString("rules:\n")
	for i := range n {
		fmt.Fprintf(&b, "  - RULE-SET,set%d,DIRECT\n", i)
	}
	b.WriteString("  - MATCH,Proxy\n")
	return []byte(b.String())
}

func TestParallelRuleDownloadsRetainOrder(t *testing.T) {
	var active, peak, count atomic.Int32
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		count.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		if n == providerWorkers {
			select {
			case <-started:
			default:
				close(started)
			}
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Error("downloads did not overlap")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credential sent to rule provider")
		}
		name := strings.Split(r.URL.Path, "/")[7]
		fmt.Fprintf(w, "payload:\n  - %s.example.test\n", name)
	}))
	defer server.Close()
	c := mustConvert(t, string(ruleFixture(server.URL, 8)))
	sets, _, err := (Fetcher{Client: server.Client()}).expandSourceProviders(t.Context(), c.doc, server.URL, nil, nil)
	if err != nil || peak.Load() != providerWorkers || count.Load() != 8 {
		t.Fatalf("peak %d, count %d: %v", peak.Load(), count.Load(), err)
	}
	p, err := parseVerified("sample", c, sets)
	if err != nil || len(p.Rules) != 8 {
		t.Fatalf("rules %d, err %v", len(p.Rules), err)
	}
	for i, rule := range p.Rules {
		if rule.RuleSet != fmt.Sprint("set", i) {
			t.Fatalf("rule %d: %+v", i, rule)
		}
	}
}

func BenchmarkProviderRefresh12(b *testing.B) {
	var input []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/subscription" {
			w.Write(input)
			return
		}
		time.Sleep(40 * time.Millisecond)
		io.WriteString(w, "payload:\n  - example.test\n")
	}))
	defer server.Close()
	input = ruleFixture(server.URL, 12)
	store := OpenStore(b.TempDir())
	src := Source{ID: "benchmark", Name: "Benchmark", Kind: "external", URL: server.URL + "/subscription"}
	if err := store.Configure(src); err != nil {
		b.Fatal(err)
	}
	f := Fetcher{Client: server.Client()}
	for b.Loop() {
		c, err := f.Refresh(context.Background(), store, src.ID, nil, Policy{})
		if err != nil || c.Decision != "ready" || len(c.Profile.Rules) != 12 {
			b.Fatal(c.Decision, len(c.Profile.Rules), err)
		}
	}
}

func TestWarningSummaryOnlyAcceptsKnownCompleteCounts(t *testing.T) {
	h := http.Header{"X-Routing-Warnings": []string{"2"}}
	for input, want := range map[string]string{"": "2", "CUSTOM_TEMPLATE=2": "CUSTOM_TEMPLATE=2", "STATIC_REJECT=1,CUSTOM_TEMPLATE=1": "CUSTOM_TEMPLATE=1,STATIC_REJECT=1", "CUSTOM_TEMPLATE=1": "2", "secret-token=2": "2", "STATIC_REJECT=1,SENSITIVE_PRIVATE_VALUE=1": "OTHER=1,STATIC_REJECT=1", "STATIC_REJECT=1,STATIC_REJECT=1": "2"} {
		h.Set("X-Routing-Warning-Codes", input)
		if got := templateWarningSummary(h); got != want {
			t.Errorf("%q: %q, want %q", input, got, want)
		}
	}
}
