package subscriptioncli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	sub "github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// The tests stay off the network: no subscription fetches subconv's routing template.
func TestMain(m *testing.M) {
	sub.TemplateHosts = nil
	os.Exit(m.Run())
}

func TestPublicMethodsDoNotExposeRawSubscription(t *testing.T) {
	st := sub.OpenStore(t.TempDir())
	ctx := context.Background()
	for _, v := range []struct {
		method string
		in     input
	}{{"configure", input{Config: &sub.Source{ID: "file", Kind: "file", Name: "File"}}}, {"refresh", input{Source: "file", Content: "http://user:do-not-leak@private.example:8080#Edge"}}, {"list", input{}}, {"profile", input{Source: "file"}}, {"nodes", input{Source: "file"}}, {"groups", input{Source: "file"}}, {"rules", input{Source: "file"}}, {"status", input{Source: "file"}}} {
		out, _, e := run(ctx, st, v.method, v.in, sub.Fetcher{})
		if e != nil {
			t.Fatal(v.method, e)
		}
		raw, _ := json.Marshal(out)
		for _, s := range []string{"do-not-leak", "private.example", "server_port", "outbound"} {
			if strings.Contains(string(raw), s) {
				t.Fatal(v.method, "exposed", s)
			}
		}
	}
	if _, _, e := run(ctx, st, "profile", input{Source: "file", Limit: 999}, sub.Fetcher{}); e == nil {
		t.Fatal("unbounded read")
	}
}

func TestProviderNamesCannotReExposeCredentials(t *testing.T) {
	p, e := sub.Parse("source", []byte(`{"outbounds":[{"tag":"https://feed.example/sub?token=private-value","type":"http","server":"example.org","server_port":80},{"tag":"Bearer private-value","type":"selector","outbounds":["https://feed.example/sub?token=private-value"]}],"route":{"final":"Bearer private-value"}}`))
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(p.Summary())
	if strings.Contains(string(encoded), "private-value") || p.Summary().Groups[0].Members[0] != p.Nodes[0].ID {
		t.Fatal("public label leaked a credential or lost the assigned member")
	}
}

func TestOnlyChangedProfilesNotifyRouter(t *testing.T) {
	st := sub.OpenStore(t.TempDir())
	ctx := t.Context()
	if err := st.Configure(sub.Source{ID: "file", Kind: "file", Name: "File"}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	f := sub.Fetcher{BeforeCommit: func(context.Context, sub.Profile) (func() error, error) {
		checks++
		return func() error { return nil }, nil
	}}
	first := "http://old.example:81#Edge"
	next := "http://new.example:81#Edge"
	for i, text := range []string{first, next, next} {
		_, changes, err := run(ctx, st, "refresh", input{Source: "file", Content: text}, f)
		wantNotify := i < 2
		if err != nil || (len(changes) == 1) != wantNotify || len(changes) > 1 {
			t.Fatalf("refresh %d: %v, notified %v", i, err, changes)
		}
	}
	if checks != 2 {
		t.Fatalf("expected two candidate checks, got %d", checks)
	}
	if err := st.Rollback("file"); err != nil {
		t.Fatal(err)
	}
	actual, err := st.Load("file")
	original, _ := sub.Parse("file", []byte(first))
	if err != nil || actual.Nodes[0].ID != original.Nodes[0].ID {
		t.Fatal("identical refresh displaced rollback snapshot", err)
	}
}

// The daemon logs a refresh whose update was not applied by the code it returns.
func TestRefreshReturnsPendingUpdate(t *testing.T) {
	st := sub.OpenStore(t.TempDir())
	if err := st.Configure(sub.Source{ID: "file", Kind: "file", Name: "File"}); err != nil {
		t.Fatal(err)
	}
	const feed = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['DOMAIN,ads.example,REJECT', 'MATCH,DIRECT']"
	for i, text := range []string{feed, strings.Replace(feed, "REJECT", "DIRECT", 1)} {
		out, _, err := run(t.Context(), st, "refresh", input{Source: "file", Content: text, Scheduled: true}, sub.Fetcher{})
		raw, _ := json.Marshal(out)
		if pending := strings.Contains(string(raw), `"pending_update":{"code":"confirmation_required","notices":["rejection_removed"]}`); err != nil || pending != (i == 1) {
			t.Fatal(err, string(raw))
		}
	}
}

// decode reads a request as Main does, refusing fields the helper does not know.
func decode(raw string) (input, error) {
	var in input
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	return in, d.Decode(&in)
}

// A routing choice comes with the refresh request and is saved with the update it commits.
func TestRefreshCarriesRoutingChoice(t *testing.T) {
	st := sub.OpenStore(t.TempDir())
	if err := st.Configure(sub.Source{ID: "file", Kind: "file", Name: "File"}); err != nil {
		t.Fatal(err)
	}
	in, err := decode(`{"source":"file","content":"http://edge.example:81#Edge","rules":"template"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t.Context(), st, "refresh", in, sub.Fetcher{}); err != nil {
		t.Fatal(err)
	}
	if s, err := st.Status("file"); err != nil || s.Rules != sub.RoutingTemplate {
		t.Fatal("routing choice not saved with its update", err, s.Rules)
	}
}

// A LuCI tab open from before the upgrade still sends the routing choice with the other
// preferences. The save goes through, and the choice is ignored rather than saved ahead of
// the update read with it.
func TestPreferencesIgnoreARoutingChoice(t *testing.T) {
	st := sub.OpenStore(t.TempDir())
	if err := st.Configure(sub.Source{ID: "link", Kind: "external", Name: "Link", URL: "https://feed.example/sub"}); err != nil {
		t.Fatal(err)
	}
	in, err := decode(`{"source":"link","preferences":{"name":"Renamed","user_agent":"sing-box","filters":null,"allow_local_fallback":true,"rules":"template"}}`)
	if err != nil {
		t.Fatal("preferences with a routing choice refused", err)
	}
	if _, _, err := run(t.Context(), st, "preferences", in, sub.Fetcher{}); err != nil {
		t.Fatal(err)
	}
	s, err := st.Status("link")
	if err != nil || s.Rules != sub.RoutingOwn || s.Name != "Renamed" || s.Agent != "sing-box" || !s.LocalFallback {
		t.Fatal("preferences with a routing choice", err, s.Rules, s.Name, s.Agent, s.LocalFallback)
	}
}

// The rules page checks a GEOSITE,CN or GEOIP,CN set, or a local profile's mainland routes,
// against the router's mainland lists: the sets the core routes them by.
func TestMainlandSetsMatchTheRoutersLists(t *testing.T) {
	saved := cnlist.Packaged
	t.Cleanup(func() { cnlist.Packaged = saved })
	cnlist.Packaged = "../../../files/usr/share/hyper/rules"
	st := sub.OpenStore(t.TempDir())
	for source, content := range map[string]string{
		"own":   "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['GEOSITE,CN,DIRECT', 'GEOIP,CN,DIRECT,no-resolve', 'MATCH,Edge']",
		"local": "http://edge.example:81#Edge",
	} {
		if err := st.Configure(sub.Source{ID: source, Kind: "file", Name: source}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := run(t.Context(), st, "refresh", input{Source: source, Content: content}, sub.Fetcher{}); err != nil {
			t.Fatal(err)
		}
		for _, q := range []struct {
			set, query string
			want       bool
		}{{sub.DomesticDomainSet, "www.baidu.com", true}, {sub.DomesticDomainSet, "www.example.org", false}, {sub.DomesticIPSet, "223.5.5.5", true}, {sub.DomesticIPSet, "8.8.8.8", false}} {
			out, _, err := run(t.Context(), st, "set_match", input{Source: source, RuleSet: q.set, Query: q.query}, sub.Fetcher{})
			if err != nil || out.(map[string]bool)["matched"] != q.want {
				t.Fatal(source, q, out, err)
			}
		}
	}
}
