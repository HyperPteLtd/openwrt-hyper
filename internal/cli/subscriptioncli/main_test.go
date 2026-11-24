package subscriptioncli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	sub "github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// fixtures configures the test-only fixture sources ids in st, each serving its document of the
// returned map, which the test changes between refreshes.
func fixtures(t *testing.T, st *sub.Store, ids ...string) map[string]string {
	t.Helper()
	docs := map[string]string{}
	previous := sub.Fixture
	sub.Fixture = func(_ context.Context, id string) ([]byte, error) { return []byte(docs[id]), nil }
	t.Cleanup(func() { sub.Fixture = previous })
	for _, id := range ids {
		if err := st.Configure(sub.Source{ID: id, Kind: sub.FixtureSource, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	return docs
}

func TestPublicMethodsDoNotExposeRawSubscription(t *testing.T) {
	st := sub.OpenStore(t.TempDir())
	fixtures(t, st, "file")["file"] = "http://user:do-not-leak@private.example:8080#Edge"
	ctx := context.Background()
	for _, v := range []struct {
		method string
		in     input
	}{{"refresh", input{Source: "file"}}, {"list", input{}}, {"profile", input{Source: "file"}}, {"nodes", input{Source: "file"}}, {"groups", input{Source: "file"}}, {"rules", input{Source: "file"}}, {"status", input{Source: "file"}}} {
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
	p, e := sub.Parse("source", []byte(`{"proxies":[{"name":"https://feed.example/sub?token=private-value","type":"http","server":"example.org","port":80}],"proxy-groups":[{"name":"Bearer private-value","type":"select","proxies":["https://feed.example/sub?token=private-value"]}],"rules":["MATCH,Bearer private-value"]}`))
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
	docs := fixtures(t, st, "file")
	checks := 0
	f := sub.Fetcher{BeforeCommit: func(context.Context, sub.Profile) (func() error, error) {
		checks++
		return func() error { return nil }, nil
	}}
	first := "http://old.example:81#Edge"
	next := "http://new.example:81#Edge"
	for i, text := range []string{first, next, next} {
		docs["file"] = text
		_, changes, err := run(ctx, st, "refresh", input{Source: "file"}, f)
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
	docs := fixtures(t, st, "file")
	const feed = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['DOMAIN,ads.example,REJECT', 'MATCH,DIRECT']"
	for i, text := range []string{feed, strings.Replace(feed, "REJECT", "DIRECT", 1)} {
		docs["file"] = text
		out, _, err := run(t.Context(), st, "refresh", input{Source: "file", Scheduled: true}, sub.Fetcher{})
		raw, _ := json.Marshal(out)
		if pending := strings.Contains(string(raw), `"pending_update":{"code":"confirmation_required","notices":["rejection_removed"]}`); err != nil || pending != (i == 1) {
			t.Fatal(err, string(raw))
		}
	}
}

// The rules page checks a GEOSITE,CN or GEOIP,CN set, or a local profile's mainland routes,
// against the router's mainland lists: the sets the core routes them by.
func TestMainlandSetsMatchTheRoutersLists(t *testing.T) {
	saved := cnlist.Packaged
	t.Cleanup(func() { cnlist.Packaged = saved })
	cnlist.Packaged = "../../../files/usr/share/hyper/rules"
	st := sub.OpenStore(t.TempDir())
	docs := fixtures(t, st, "own", "local")
	docs["own"] = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['GEOSITE,CN,DIRECT', 'GEOIP,CN,DIRECT,no-resolve', 'MATCH,Edge']"
	docs["local"] = "http://edge.example:81#Edge"
	for source := range docs {
		if _, _, err := run(t.Context(), st, "refresh", input{Source: source}, sub.Fetcher{}); err != nil {
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

// A lab serves fixture sources from a directory; without one (a router) there are none.
func TestLabFixture(t *testing.T) {
	if labFixture("") != nil || labFixture("relative") != nil {
		t.Fatal("fixture sources without a lab directory")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "probes.yaml"), []byte("proxies: []\n"), 0o644)
	if b, err := labFixture(dir)(context.Background(), "probes"); err != nil || string(b) != "proxies: []\n" {
		t.Fatal(string(b), err)
	}
}
