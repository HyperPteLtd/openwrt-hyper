package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openweir/subconv"
	"github.com/openweir/subconv/rules"
)

// The suite stays off the network: only the tests that serve a rules branch use one.
func TestMain(m *testing.M) {
	TemplateHosts = nil
	os.Exit(m.Run())
}

// A routing template as the rules branch serves it: emoji group names and a rule set on the branch.
const branchTemplate = `proxy-groups:
  - name: "🚀 Proxy"
    type: select
    proxies: [♻️ Auto, DIRECT]
    include-all: true
    exclude-filter: (?i)expire|剩余
  - name: ♻️ Auto
    type: url-test
    include-all: true
    exclude-filter: (?i)expire|剩余
    url: https://www.gstatic.com/generate_204
    interval: 300
  - name: "🎯 Domestic"
    type: select
    proxies: [DIRECT, "🚀 Proxy"]
rule-providers:
  LAN:
    type: http
    behavior: classical
    format: text
    url: "{rules}/providers/dler/lan.classical.txt"
    path: ./rules/hyper/dler/lan.classical.txt
    interval: 86400
rules:
  - RULE-SET,LAN,DIRECT
  - "GEOIP,CN,🎯 Domestic"
  - "MATCH,🚀 Proxy"
`

const branchLAN = "IP-CIDR,192.168.0.0/16,no-resolve\nDOMAIN-SUFFIX,lan\n"

// testBranch serves a rules branch: its manifest, template and one rule set.
type testBranch struct {
	srv      *httptest.Server
	version  int64
	set      string // the rule set of the manifest, branchLAN when empty
	lan      string // served in place of the manifest's rule set when set
	hang     string // requests under this path wait until the client gives up
	down     atomic.Bool
	setDown  atomic.Bool // the rule set alone is unavailable
	requests atomic.Int32
}

func serveBranch(t *testing.T, version int64) *testBranch {
	t.Helper()
	b := &testBranch{version: version}
	file := func(path, kind, data string) rules.File {
		sum := sha256.Sum256([]byte(data))
		return rules.File{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Kind: kind, Updated: "2026-11-21"}
	}
	release := make(chan struct{})
	b.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.requests.Add(1)
		if b.hang != "" && strings.HasPrefix(r.URL.Path, b.hang) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		if b.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/" + rules.ManifestPath:
			m, _ := json.Marshal(rules.Manifest{Version: b.version, Files: []rules.File{
				file(rules.TemplatePath, "template", branchTemplate),
				file("providers/dler/lan.classical.txt", "rule-set", first(b.set, branchLAN)),
			}})
			w.Write(m)
		case "/" + rules.TemplatePath:
			w.Write([]byte(branchTemplate))
		case "/providers/dler/lan.classical.txt":
			if b.setDown.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(first(b.lan, b.set, branchLAN)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(b.srv.Close)
	t.Cleanup(func() { close(release) })
	hosts, dir := TemplateHosts, TemplateDir
	TemplateHosts, TemplateDir = []rules.Host{{Base: b.srv.URL, Operator: "test"}}, t.TempDir()
	t.Cleanup(func() { TemplateHosts, TemplateDir = hosts, dir })
	return b
}

func (b *testBranch) fetcher() Fetcher { return Fetcher{embeddedClient: b.srv.Client()} }

const twoLinks = "ss://YWVzLTEyOC1nY206cA@hk.example.com:8388#HK%2001\nss://YWVzLTEyOC1nY206cA@info.example.com:8388#%E5%89%A9%E4%BD%99%E6%B5%81%E9%87%8F\n"

func fileSource(t *testing.T, rules string) (*Store, string) {
	t.Helper()
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "s", Name: "test", Kind: "file", Rules: rules}); err != nil {
		t.Fatal(err)
	}
	return store, "s"
}

func TestTemplateRoutesASubscriptionWithoutRules(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Local {
		t.Fatal(err, c.Decision, c.Notices)
	}
	p := c.Profile
	if p.Template == nil || p.Template.Reason != subconv.TemplateNoRules || p.Template.Version != 42 || p.Template.Cached {
		t.Fatalf("%+v", p.Template)
	}
	var names []string
	for _, g := range p.Groups {
		names = append(names, g.Name)
	}
	if !slices.Equal(names, []string{"🚀 Proxy", "♻️ Auto", "🎯 Domestic"}) || len(p.Groups[1].Members) != 1 {
		t.Fatal(names, p.Groups)
	}
	if len(p.BinarySets) != 1 || p.BinarySets[0].Tag != "LAN" || string(p.BinarySets[0].Data) != branchLAN || p.BinarySets[0].Behavior != "classical" {
		t.Fatalf("%+v", p.BinarySets)
	}
	if len(p.Rules) != 2 || p.Rules[0].RuleSet != "LAN" || p.Final != p.Groups[0].ID {
		t.Fatal(p.Rules, p.Final)
	}
	// The router keeps what it got on flash, under the branch's paths.
	for _, path := range []string{rules.ManifestPath, rules.TemplatePath, "providers/dler/lan.classical.txt"} {
		if _, err := os.Stat(filepath.Join(TemplateDir, templateCurrent, path)); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := store.Status(id); st.Template == nil || st.Template.Reason != subconv.TemplateNoRules {
		t.Fatal("status lacks the template")
	}
}

const ownRules = `proxies:
  - {name: HK 01, type: ss, server: hk.example.com, port: 8388, cipher: aes-128-gcm, password: p}
proxy-groups:
  - {name: Mine, type: select, proxies: [HK 01]}
rules:
  - DOMAIN-SUFFIX,a.example,Mine
  - MATCH,Mine
`

func TestRoutingChoice(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(ownRules), Policy{})
	if err != nil || c.Profile.Template != nil || c.Profile.Groups[0].Name != "Mine" {
		t.Fatal("usable rules gave way to the template", err, c.Profile.Template)
	}
	// The choice comes with the update read with it (a file imported again), and is saved with it.
	template := RoutingTemplate
	switched := b.fetcher()
	switched.Routing = &template
	c, err = switched.Refresh(t.Context(), store, id, []byte(ownRules), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Template == nil || c.Profile.Template.Reason != subconv.TemplateRequested || c.Profile.Groups[0].Name != "🚀 Proxy" {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	if st, _ := store.Status(id); st.Rules != RoutingTemplate {
		t.Fatal("choice not saved with its update")
	}
	// Preferences leave the choice as it was; a refresh that asks for none reads with it.
	if err := store.Preferences(id, Preferences{}); err != nil {
		t.Fatal(err)
	}
	if c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(ownRules), Policy{}); err != nil || c.Profile.Template == nil {
		t.Fatal("choice lost", err, c.Decision)
	}
	// An unknown choice is refused before the refresh starts.
	bad := "sometimes"
	switched.Routing = &bad
	if _, err := switched.Refresh(t.Context(), store, id, []byte(ownRules), Policy{}); err == nil {
		t.Fatal("unknown routing choice accepted")
	}
	if st, _ := store.Status(id); st.Rules != RoutingTemplate || st.LastFailure != "" {
		t.Fatal("refused choice reached the source", st.Rules, st.LastFailure)
	}
}

// ownRejectingRules route by the subscription's own rules, which block a domain the template
// does not: switching to the template asks for confirmation.
const ownRejectingRules = `proxies:
  - {name: HK 01, type: ss, server: hk.example.com, port: 8388, cipher: aes-128-gcm, password: p}
proxy-groups:
  - {name: Mine, type: select, proxies: [HK 01]}
rules:
  - DOMAIN-SUFFIX,ads.example,REJECT
  - DOMAIN-SUFFIX,a.example,Mine
  - MATCH,Mine
`

// A routing choice is saved only with the update read with it. A switch whose confirmation is
// never given, or that is blocked, leaves the source on the routing it uses: its scheduled
// refreshes go on reading with that, and an update one of them could not apply stays pending.
// Confirmed, the switch is saved with its update.
func TestRoutingChoiceSavedOnlyWithItsUpdate(t *testing.T) {
	b := serveBranch(t, 42)
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	body := ownRejectingRules
	scheduled := b.fetcher()
	scheduled.Client = &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	template := RoutingTemplate
	switched := scheduled
	switched.Routing = &template
	refresh := func(f Fetcher, policy Policy, decision string) Candidate {
		t.Helper()
		c, err := f.Refresh(t.Context(), store, src.ID, nil, policy)
		if err != nil || c.Decision != decision {
			t.Fatal(err, c.Decision, c.Notices)
		}
		return c
	}
	inUse := func(what, rules string) Status {
		t.Helper()
		st, err := store.Status(src.ID)
		current, _ := store.Load(src.ID)
		if err != nil || st.Rules != rules || (current.Template != nil) != (rules == RoutingTemplate) || (st.Template != nil) != (rules == RoutingTemplate) {
			t.Fatal(what, err, st.Rules, current.Template)
		}
		return st
	}
	own := refresh(scheduled, Policy{}, "ready")

	// The template drops the REJECT rule: the switch waits for the administrator, who cancels.
	c := refresh(switched, Policy{}, "confirmation_required")
	if c.Profile.Template == nil || !slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "rejection_removed" }) {
		t.Fatal(c.Profile.Template, c.Notices)
	}
	inUse("a cancelled switch", RoutingOwn)
	// A custom rule routes by one of the subscription's own groups, which the template lacks.
	refresh(switched, Policy{ActiveIDs: []string{own.Profile.Groups[0].ID}}, "blocked")
	inUse("a blocked switch", RoutingOwn)

	// The provider drops the REJECT rule. A scheduled refresh reads with the subscription's
	// own rules and keeps the update for the administrator; a switch they leave unapplied
	// does not settle it.
	body = strings.Replace(ownRejectingRules, "  - DOMAIN-SUFFIX,ads.example,REJECT\n", "", 1)
	if c := refresh(scheduled, Policy{Scheduled: true}, "confirmation_required"); c.Profile.Template != nil {
		t.Fatal("a scheduled refresh read with the switch left unapplied")
	}
	refresh(switched, Policy{}, "confirmation_required")
	if st := inUse("a second cancelled switch", RoutingOwn); st.PendingUpdate == nil || st.PendingUpdate.Code != PendingConfirmation {
		t.Fatal("a switch left unapplied settled the scheduled update", st.PendingUpdate)
	}

	// Confirmed, the switch is saved with its update, which settles the pending one; scheduled
	// refreshes now read with the template.
	c = refresh(switched, Policy{}, "confirmation_required")
	refresh(switched, Policy{Confirm: c.ConfirmationID}, "ready")
	if st := inUse("a confirmed switch", RoutingTemplate); st.PendingUpdate != nil {
		t.Fatal("applied switch left the update pending", st.PendingUpdate)
	}
	if c := refresh(scheduled, Policy{Scheduled: true}, "ready"); c.Profile.Template == nil {
		t.Fatal("a scheduled refresh lost the saved choice")
	}
	inUse("a scheduled refresh", RoutingTemplate)
}

// A rollback restores the routing choice the profile it goes back to was read with: the status
// shows the routing in use, and the next refresh, a scheduled one too, reads with it instead of
// switching back unasked. Rolled back again, the switch comes back with its choice.
func TestRollbackRestoresTheRoutingChoice(t *testing.T) {
	b := serveBranch(t, 42)
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	scheduled := b.fetcher()
	scheduled.Client = &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(ownRules))}, nil
	})}
	template := RoutingTemplate
	switched := scheduled
	switched.Routing = &template
	refresh := func(f Fetcher, policy Policy) Candidate {
		t.Helper()
		c, err := f.Refresh(t.Context(), store, src.ID, nil, policy)
		if err != nil || c.Decision != "ready" {
			t.Fatal(err, c.Decision, c.Notices)
		}
		return c
	}
	inUse := func(what, rules string) {
		t.Helper()
		st, err := store.Status(src.ID)
		current, _ := store.Load(src.ID)
		if err != nil || st.Rules != rules || (current.Template != nil) != (rules == RoutingTemplate) || (st.Template != nil) != (rules == RoutingTemplate) {
			t.Fatal(what, err, st.Rules, current.Template)
		}
	}
	rollback := func() {
		t.Helper()
		if err := store.Rollback(src.ID); err != nil {
			t.Fatal(err)
		}
	}
	refresh(scheduled, Policy{})
	refresh(switched, Policy{})
	inUse("the switch", RoutingTemplate)

	rollback()
	inUse("the rollback", RoutingOwn)
	if c := refresh(scheduled, Policy{Scheduled: true}); c.Profile.Template != nil || !c.Unchanged() {
		t.Fatal("a scheduled refresh switched the rolled back routing", c.Profile.Template, c.Unchanged())
	}
	inUse("a scheduled refresh after the rollback", RoutingOwn)

	rollback()
	inUse("a second rollback", RoutingTemplate)
	if c := refresh(scheduled, Policy{Scheduled: true}); c.Profile.Template == nil || !c.Unchanged() {
		t.Fatal("a scheduled refresh lost the restored template", c.Profile.Template, c.Unchanged())
	}
	inUse("a scheduled refresh after the second rollback", RoutingTemplate)
}

// Profiles stored before they recorded their routing choice still load and roll back. The
// source keeps its choice then, as before.
func TestRollbackToAProfileWithoutItsRoutingChoice(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(ownRules), Policy{}); err != nil {
		t.Fatal(err)
	}
	template := RoutingTemplate
	switched := b.fetcher()
	switched.Routing = &template
	if c, err := switched.Refresh(t.Context(), store, id, []byte(ownRules), Policy{}); err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision, c.Notices)
	}
	// The record as an earlier version stored it.
	if err := store.update(id, func(r *record) error {
		r.Current.RoutingChoice, r.Previous.RoutingChoice = nil, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(store.path(id)); err != nil || strings.Contains(string(raw), "routing_choice") {
		t.Fatal("record still holds a routing choice", err)
	}
	if err := store.Rollback(id); err != nil {
		t.Fatal(err)
	}
	st, err := store.Status(id)
	current, _ := store.Load(id)
	if err != nil || !st.Ready || current.Template != nil || current.Groups[0].Name != "Mine" || st.Rules != RoutingTemplate {
		t.Fatal("rollback without a recorded choice", err, st.Ready, current.Template, st.Rules)
	}
}

func TestTemplateSurvivesHostsThatDoNotAnswer(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{}); err != nil {
		t.Fatal(err)
	}
	b.down.Store(true)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks+"ss://YWVzLTEyOC1nY206cA@jp.example.com:8388#JP%2001\n"), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Template == nil || !c.Profile.Template.Cached || len(c.Profile.BinarySets) != 1 {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
}

func TestTemplateRefusesARollback(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{}); err != nil {
		t.Fatal(err)
	}
	// A host now serving an older branch is not followed back: the router keeps its copy.
	b.version = 41
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks+"ss://YWVzLTEyOC1nY206cA@jp.example.com:8388#JP%2001\n"), Policy{})
	if err != nil || c.Profile.Template == nil || c.Profile.Template.Version != 42 || !c.Profile.Template.Cached {
		t.Fatal(err, c.Profile.Template)
	}
}

// A rule set unlike the manifest, or one no host serves, is not used: the subscription is
// still updated, and routes by the router's default until a later refresh gets the set.
func TestTemplateRuleSetsMustMatchTheManifest(t *testing.T) {
	b := serveBranch(t, 42)
	b.lan = "DOMAIN-SUFFIX,tampered.example\n"
	store, id := fileSource(t, RoutingOwn)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{})
	if err != nil || c.Decision != "ready" || !c.Profile.Local || c.Profile.Template != nil || len(c.Profile.Nodes) != 2 {
		t.Fatal("a rule set unlike the manifest stopped or entered the update", err, c.Decision, c.Profile.Template)
	}
	if !slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "subconv_template_unavailable" }) {
		t.Fatal(c.Notices)
	}
	if _, err := os.Stat(filepath.Join(TemplateDir, templateCurrent, "providers/dler/lan.classical.txt")); err == nil {
		t.Fatal("tampered rule set cached")
	}
	b.lan = ""
	if c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{AllowLocalFallback: true}); err != nil || c.Profile.Template == nil {
		t.Fatal("a later refresh did not take the template", err, c.Decision)
	}
}

const lossyRejections = `proxies:
  - {name: HK 01, type: ss, server: hk.example.com, port: 8388, cipher: aes-128-gcm, password: p}
proxy-groups:
  - {name: Mine, type: select, proxies: [HK 01]}
rules:
  - USER-AGENT,ads*,REJECT
  - DOMAIN-SUFFIX,a.example,Mine
  - MATCH,Mine
`

// Rules the template replaced no longer weigh: an automatic update does not stop for the
// rejection a subscription always loses.
func TestTemplateUpdatesNeedNoConfirmation(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	for range 2 {
		c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(lossyRejections), Policy{Scheduled: true})
		if err != nil || c.Decision != "ready" || c.Profile.Template == nil || c.Profile.Template.Reason != subconv.TemplateDropped {
			t.Fatal(err, c.Decision, c.Notices)
		}
		if slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Dangerous || n.Code == "subconv_unsupported_rule" }) {
			t.Fatal(c.Notices)
		}
	}
}

// A subscription on a geodata mirror the router does not trust: subconv keeps its GEOIP and
// GEOSITE rules, and the router drops them (clashDefaultGeo).
const mirrorGeo = `geox-url:
  geoip: https://cdn.jsdmirror.com/gh/MetaCubeX/meta-rules-dat@release/geoip.dat
  geosite: https://cdn.jsdmirror.com/gh/MetaCubeX/meta-rules-dat@release/geosite.dat
  mmdb: https://cdn.jsdmirror.com/gh/MetaCubeX/meta-rules-dat@release/country.mmdb
proxies:
  - {name: HK 01, type: ss, server: hk.example.com, port: 8388, cipher: aes-128-gcm, password: p}
  - {name: HK 02, type: ss, server: hk2.example.com, port: 8388, cipher: aes-128-gcm, password: p}
proxy-groups:
  - {name: Proxy, type: select, proxies: [HK 01, HK 02]}
rules:
`

func mirrorRules(rules ...string) string {
	return mirrorGeo + "  - " + strings.Join(rules, "\n  - ") + "\n"
}

// The rules the router drops itself count with subconv's toward the template: the same tenth,
// and the final rule.
func TestRouterDropsCountTowardTheTemplate(t *testing.T) {
	domains := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("DOMAIN-SUFFIX,d%d.example,Proxy", i))
		}
		return out
	}
	long := strings.Repeat("L", 400)
	for _, tc := range []struct {
		name, body      string
		reason          string
		dropped, total  int
		ownRules, notes int // with the subscription's own rules: those kept, and the router's notices
	}{
		{name: "custom geodata", reason: subconv.TemplateDropped, dropped: 4, total: 6,
			body: mirrorRules("GEOSITE,google,Proxy", "GEOSITE,youtube,Proxy", "GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "DOMAIN-SUFFIX,a.example,Proxy", "MATCH,Proxy")},
		{name: "a tenth", ownRules: 8, notes: 1,
			body: mirrorRules(slices.Concat([]string{"GEOSITE,google,Proxy"}, domains(8), []string{"MATCH,Proxy"})...)},
		// subconv drops the USER-AGENT rule, too few by itself, and the router one more.
		{name: "with subconv's", reason: subconv.TemplateDropped, dropped: 2, total: 11,
			body: mirrorRules(slices.Concat([]string{"USER-AGENT,ads*,REJECT", "GEOSITE,google,Proxy"}, domains(8), []string{"MATCH,Proxy"})...)},
		{name: "missing rule set", reason: subconv.TemplateDropped, dropped: 1, total: 3,
			body: "rule-providers:\n  ads: {type: file, behavior: domain, path: ./ads.yaml}\n" + mirrorRules("RULE-SET,ads,REJECT", "DOMAIN-SUFFIX,a.example,Proxy", "MATCH,Proxy")},
		// A group name too long for the router: its group and the final rule to it go.
		{name: "final rule", reason: subconv.TemplateNoMatch,
			body: strings.Replace(mirrorRules("DOMAIN-SUFFIX,a.example,Proxy", "MATCH,"+long), "rules:", "  - {name: "+long+", type: select, proxies: [HK 01]}\nrules:", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := serveBranch(t, 42)
			store, id := fileSource(t, RoutingOwn)
			f := b.fetcher()
			f.ruleImportRoot = t.TempDir()
			c, err := f.Refresh(t.Context(), store, id, []byte(tc.body), Policy{Scheduled: true})
			if err != nil || c.Decision != "ready" {
				t.Fatal(err, c.Decision, c.Notices)
			}
			p := c.Profile
			if tc.reason == "" {
				if p.Template != nil || p.Groups[0].Name != "Proxy" || len(p.Rules) != tc.ownRules || p.Final == "" || len(c.Notices) != tc.notes {
					t.Fatal("the subscription's own rules gave way", p.Template, p.Rules, c.Notices)
				}
				return
			}
			want := TemplateUse{Version: 42, Reason: tc.reason, Dropped: tc.dropped, Total: tc.total}
			if p.Template == nil || *p.Template != want || p.Groups[0].Name != "🚀 Proxy" || p.Local {
				t.Fatalf("%+v %v", p.Template, c.Notices)
			}
			// The rules the template replaced no longer weigh.
			if slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Dangerous || partialNotices[n.Code] }) {
				t.Fatal(c.Notices)
			}
			if st, _ := store.Status(id); st.Template == nil || *st.Template != want {
				t.Fatalf("status: %+v", st.Template)
			}
		})
	}
}

// A subscription asks for the template or keeps its own rules, whatever the router drops: with
// the template chosen, the reason stays the choice, and without a rules branch, subconv never
// takes the template.
func TestRouterDropsLeaveFixedRoutingAlone(t *testing.T) {
	body := mirrorRules("GEOSITE,google,Proxy", "GEOSITE,youtube,Proxy", "GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "DOMAIN-SUFFIX,a.example,Proxy", "MATCH,Proxy")
	store, id := fileSource(t, RoutingOwn)
	c, err := (Fetcher{}).Refresh(t.Context(), store, id, []byte(body), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Template != nil || len(c.Profile.Rules) != 1 || c.Profile.Final == "" {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Profile.Rules)
	}
	b := serveBranch(t, 42)
	store, id = fileSource(t, RoutingTemplate)
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(body), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Template == nil || *c.Profile.Template != (TemplateUse{Version: 42, Reason: subconv.TemplateRequested}) {
		t.Fatal(err, c.Decision, c.Profile.Template)
	}
}

// A rule the router drops is dangerous as one subconv drops: when it rejected traffic.
func TestRejectedRulesAreWeighedByTheirTarget(t *testing.T) {
	for rule, want := range map[string]bool{
		"GEOSITE,category-ads-all,REJECT":              true,
		"GEOIP,AD,REJECT-DROP,no-resolve":              true,
		"IP-CIDR,10.0.0.0/8,reject-200,no-resolve,src": true,
		"DOMAIN-SUFFIX,reject.example,Proxy":           false,
		"RULE-SET,reject,DIRECT":                       false,
	} {
		if rejects(rule) != want || dropsRejection(rule+": rule type") != want {
			t.Error(rule)
		}
	}
	// Custom geodata, and a rule type the router does not compile.
	p := mustParse(t, mirrorRules("GEOSITE,category-ads-all,REJECT", "GEOSITE,reject-list,Proxy", "PROCESS-NAME,ads,REJECT", "PROCESS-NAME,reject-helper,DIRECT", "MATCH,Proxy"))
	var got []string
	for _, n := range p.Notices {
		got = append(got, fmt.Sprint(n.Code, " ", n.At, " ", n.Dangerous))
	}
	if !slices.Equal(got, []string{"rule_provider_custom_geo GEOSITE category-ads-all true", "rule_provider_custom_geo GEOSITE reject-list false", "mihomo_unsupported_rule rule 3 true", "mihomo_unsupported_rule rule 4 false"}) {
		t.Fatal(got)
	}
}

// The template's rules mean the default geodata, whatever the subscription chose for its own,
// and a script the subscription routed by is gone with its rules.
func TestTemplateIgnoresTheSubscriptionsGeodataAndScript(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingTemplate)
	body := "geox-url:\n  geoip: https://example.org/my/geoip.dat\nscript:\n  code: |\n    def main(ctx, m): return 'DIRECT'\n" + ownRules
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(body), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Template == nil {
		t.Fatal(err, c.Decision, c.Notices)
	}
	if !slices.ContainsFunc(c.Profile.Rules, func(r Rule) bool { return r.RuleSet == DomesticIPSet }) {
		t.Fatal("the template's GEOIP,CN rule was dropped", c.Profile.Rules)
	}
	if slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "script" || n.Code == "rule_provider_custom_geo" }) {
		t.Fatal(c.Notices)
	}
}

// Seventy rule sets arrive six at a time.
func TestTemplateSetsDownloadInParallel(t *testing.T) {
	var active, peak atomic.Int32
	files := []rules.File{}
	providers := map[string]any{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+rules.ManifestPath {
			m, _ := json.Marshal(rules.Manifest{Version: 7, Files: files})
			w.Write(m)
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("DOMAIN," + strings.TrimPrefix(r.URL.Path, "/") + "\n"))
	}))
	defer srv.Close()
	files = append(files, rules.File{Path: rules.TemplatePath, Size: 1, SHA256: strings.Repeat("0", 64)})
	for i := range 70 {
		path := fmt.Sprintf("providers/set%d.txt", i)
		data := "DOMAIN," + path + "\n"
		sum := sha256.Sum256([]byte(data))
		files = append(files, rules.File{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		providers[fmt.Sprintf("s%d", i)] = map[string]any{"type": "http", "url": srv.URL + "/" + path}
	}
	client := &rules.Client{HTTP: srv.Client(), Hosts: []rules.Host{{Base: srv.URL, Operator: "test"}}, Cache: &rules.MemoryCache{}}
	sets, err := fetchTemplateSets(t.Context(), client, 7, providers)
	if err != nil || len(sets) != 70 || string(sets["s3"]) != "DOMAIN,providers/set3.txt\n" {
		t.Fatal(err, len(sets))
	}
	if p := peak.Load(); p < 2 || p > templateParallel {
		t.Fatal("concurrent downloads:", p)
	}
}

func TestWithoutTemplateTheRouterRoutesByDefault(t *testing.T) {
	b := serveBranch(t, 42)
	b.down.Store(true)
	store, id := fileSource(t, RoutingTemplate)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(ownRules), Policy{})
	if err != nil || c.Decision != "ready" || !c.Profile.Local || c.Profile.Template != nil || len(c.Profile.Groups) != 0 {
		t.Fatal(err, c.Decision, c.Notices)
	}
	if !slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "subconv_template_unavailable" && !strings.Contains(n.At, "://") }) {
		t.Fatal(c.Notices)
	}
}

func TestBuildLeavesTheConfigToTheRouter(t *testing.T) {
	b := serveBranch(t, 42)
	branch := b.fetcher().branch(t.Context())
	defer branch.close()
	c, err := convert(t.Context(), []byte(ownRules), RoutingTemplate, branch.Client)
	if err != nil || c.template == nil {
		t.Fatal(err, c.notices)
	}
	for key := range c.doc {
		if !slices.Contains([]string{"proxies", "proxy-groups", "rule-providers", "rules"}, key) {
			t.Fatal("subconv wrote config the router writes itself:", key)
		}
	}
}

// The rules branch is reached as a provider is: a host on a private address is refused.
func TestBranchKeepsTheProviderPolicy(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	c, err := (Fetcher{}).Refresh(t.Context(), store, id, []byte(twoLinks), Policy{})
	if err != nil || c.Profile.Template != nil || b.requests.Load() != 0 {
		t.Fatal("reached a branch on loopback", err, b.requests.Load())
	}
}

const jpLink = "ss://YWVzLTEyOC1nY206cA@jp.example.com:8388#JP%2001\n"

// The version the router's copy holds.
func copiedVersion(t *testing.T) int64 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(TemplateDir, templateCurrent, rules.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	m, err := rules.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return m.Version
}

// routesByCopy reports a refresh that routed by the router's copy of version 42.
func routesByCopy(c Candidate, err error) bool {
	p := c.Profile
	return err == nil && c.Decision == "ready" && p.Template != nil && p.Template.Version == 42 && p.Template.Cached &&
		len(p.BinarySets) == 1 && string(p.BinarySets[0].Data) == branchLAN &&
		!slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "subconv_template_unavailable" })
}

// A version that arrives in part leaves the router's copy as it was: the refresh routes by the
// copy, as do later ones without hosts, scheduled ones included.
func TestTemplateArrivingInPartKeepsTheCopy(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{}); err != nil {
		t.Fatal(err)
	}
	// Version 43 changes the rule set, and no host serves it.
	b.version, b.set = 43, branchLAN+"DOMAIN-SUFFIX,new.example\n"
	b.setDown.Store(true)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks+jpLink), Policy{Scheduled: true})
	if !routesByCopy(c, err) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	if v := copiedVersion(t); v != 42 {
		t.Fatal("the copy holds version", v)
	}
	b.down.Store(true)
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{Scheduled: true})
	if !routesByCopy(c, err) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	// Once the rule set arrives, the copy moves to 43.
	b.down.Store(false)
	b.setDown.Store(false)
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{})
	if err != nil || c.Profile.Template == nil || c.Profile.Template.Version != 43 || c.Profile.Template.Cached || copiedVersion(t) != 43 {
		t.Fatal(err, c.Profile.Template)
	}
}

// A rule set that matches its manifest but that the router cannot use (ruleSet): a web page.
const unusableSet = "<!DOCTYPE html><html><body>Not found</body></html>\n"

// A version whose rule sets the router cannot use, though subconv and the manifest accept them,
// is as one that did not arrive: the refresh routes by the router's copy, which stays, or by
// default without one; the version never becomes the copy.
func TestTemplateUnusableRuleSetsKeepTheCopy(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{}); err != nil {
		t.Fatal(err)
	}
	b.version, b.set = 43, unusableSet
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks+jpLink), Policy{Scheduled: true})
	if !routesByCopy(c, err) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	if v := copiedVersion(t); v != 42 {
		t.Fatal("the copy holds version", v)
	}
	b.down.Store(true)
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{Scheduled: true})
	if !routesByCopy(c, err) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}

	// Without a copy: default routing, and still no copy.
	b = serveBranch(t, 42)
	b.set = unusableSet
	store, id = fileSource(t, RoutingTemplate)
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(ownRules), Policy{})
	if err != nil || c.Decision != "ready" || !c.Profile.Local || c.Profile.Template != nil ||
		!slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "subconv_template_unavailable" }) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	if _, err := os.Lstat(filepath.Join(TemplateDir, templateCurrent)); err == nil {
		t.Fatal("a version the router cannot use became its copy")
	}
}

// A copy whose rule sets the router cannot use, as an earlier version may have kept: the
// refresh routes by default rather than failing, which the administrator confirms, as any
// loss of the routing.
func TestTemplateUnusableCopyRoutesByDefault(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{}); err != nil {
		t.Fatal(err)
	}
	b.set = unusableSet
	r, err := b.srv.Client().Get(b.srv.URL + "/" + rules.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	gen, _ := os.Readlink(filepath.Join(TemplateDir, templateCurrent))
	for path, data := range map[string]string{rules.ManifestPath: string(manifest), "providers/dler/lan.classical.txt": unusableSet} {
		path = filepath.Join(TemplateDir, gen, path)
		if os.Remove(path) != nil || os.WriteFile(path, []byte(data), 0600) != nil {
			t.Fatal("cannot rewrite", path)
		}
	}
	b.down.Store(true)
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{Scheduled: true})
	if err != nil || c.Decision != "confirmation_required" || c.Profile.Template != nil || len(c.Profile.Rules) != 0 ||
		!slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "subconv_template_unavailable" }) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
}

// A copy the router could not save is told, as nothing worse: the refresh routes by the version
// it got, and a later one saves it.
func TestTemplateCopyNotSavedIsTold(t *testing.T) {
	notSaved := func(c Candidate) bool {
		return slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "template_cache_write_failed" && !n.Dangerous })
	}
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	failSyncs(t, func(string) bool { return true })
	c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{})
	if err != nil || c.Decision != "ready" || c.Profile.Template == nil || c.Profile.Template.Cached || c.Profile.Local || !notSaved(c) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	if _, err := os.Lstat(filepath.Join(TemplateDir, templateCurrent)); err == nil {
		t.Fatal("a generation that failed its sync was published")
	}
	// The sync after the link moved fails: the copy holds the version, but may lose it.
	failSyncs(t, func(dir string) bool {
		_, err := os.Lstat(filepath.Join(TemplateDir, templateCurrentNext))
		return dir == TemplateDir && err != nil
	})
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks+jpLink), Policy{})
	if err != nil || c.Decision != "ready" || !notSaved(c) || copiedVersion(t) != 42 {
		t.Fatal(err, c.Decision, c.Notices)
	}
	failSyncs(t, func(string) bool { return false })
	c, err = b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{})
	if err != nil || c.Decision != "ready" || notSaved(c) {
		t.Fatal(err, c.Decision, c.Notices)
	}
}

// The template stage leaves the refresh the time it needs after it: out of time, the refresh
// routes by the router's copy, or by default, rather than failing.
func TestTemplateStageKeepsTheRefreshsTime(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	if _, err := b.fetcher().Refresh(t.Context(), store, id, []byte(twoLinks), Policy{}); err != nil {
		t.Fatal(err)
	}
	// Version 43's rule set takes longer than the refresh has.
	b.version, b.set, b.hang = 43, branchLAN+"DOMAIN-SUFFIX,new.example\n", "/providers/"
	ctx, cancel := context.WithTimeout(t.Context(), templateReserve+time.Second)
	defer cancel()
	c, err := b.fetcher().Refresh(ctx, store, id, []byte(twoLinks+jpLink), Policy{})
	if !routesByCopy(c, err) || ctx.Err() != nil {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
	if v := copiedVersion(t); v != 42 {
		t.Fatal("the copy holds version", v)
	}
	// A refresh already within its reserve asks no host.
	ctx, cancel = context.WithTimeout(t.Context(), templateReserve-time.Second)
	defer cancel()
	before := b.requests.Load()
	c, err = b.fetcher().Refresh(ctx, store, id, []byte(twoLinks), Policy{})
	if !routesByCopy(c, err) || b.requests.Load() != before {
		t.Fatal(err, c.Decision, c.Profile.Template, b.requests.Load()-before)
	}

	// Without a copy, hosts that do not answer in time leave the router's default routing.
	b = serveBranch(t, 42)
	b.hang = "/"
	store, id = fileSource(t, RoutingTemplate)
	ctx, cancel = context.WithTimeout(t.Context(), templateReserve+time.Second)
	defer cancel()
	c, err = b.fetcher().Refresh(ctx, store, id, []byte(ownRules), Policy{})
	if err != nil || c.Decision != "ready" || !c.Profile.Local || c.Profile.Template != nil || ctx.Err() != nil ||
		!slices.ContainsFunc(c.Notices, func(n Notice) bool { return n.Code == "subconv_template_unavailable" }) {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
}

// When the router's own drops send a subscription to the template, the second reading keeps
// within the stage too.
func TestTemplateStageCoversTheAutomaticChoice(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingOwn)
	f := b.fetcher()
	f.ruleImportRoot = t.TempDir()
	geo := []string{"GEOSITE,google,Proxy", "GEOSITE,youtube,Proxy", "GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "DOMAIN-SUFFIX,a.example,Proxy"}
	body := mirrorRules(append(geo, "MATCH,Proxy")...)
	if c, err := f.Refresh(t.Context(), store, id, []byte(body), Policy{}); err != nil || c.Profile.Template == nil || copiedVersion(t) != 42 {
		t.Fatal(err, c.Profile.Template)
	}
	b.version, b.set, b.hang = 43, branchLAN+"DOMAIN-SUFFIX,new.example\n", "/providers/"
	ctx, cancel := context.WithTimeout(t.Context(), templateReserve+time.Second)
	defer cancel()
	body = mirrorRules(append(geo, "DOMAIN-SUFFIX,b.example,Proxy", "MATCH,Proxy")...)
	c, err := f.Refresh(ctx, store, id, []byte(body), Policy{Scheduled: true})
	if !routesByCopy(c, err) || c.Profile.Template.Reason != subconv.TemplateDropped || c.Profile.Template.Dropped != 4 || c.Profile.Template.Total != 7 ||
		ctx.Err() != nil || copiedVersion(t) != 42 {
		t.Fatal(err, c.Decision, c.Profile.Template, c.Notices)
	}
}

// The subscription's providers download once in a refresh, however many times it reads the
// subscription: with the template the router's drops sent it to, or with the router's copy
// after the hosts' version.
func TestReadingsShareTheProviderDownloads(t *testing.T) {
	b := serveBranch(t, 42)
	var downloads atomic.Int32
	nodes := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write([]byte("proxies:\n  - {name: P1, type: ss, server: p1.example.com, port: 8388, cipher: aes-128-gcm, password: p}\n"))
	}))
	defer nodes.Close()
	store, id := fileSource(t, RoutingOwn)
	f := b.fetcher()
	f.ruleImportRoot = t.TempDir()
	body := "proxy-providers:\n  extra: {type: http, url: " + nodes.URL + "/nodes, interval: 3600}\n" +
		mirrorRules("GEOSITE,google,Proxy", "GEOSITE,youtube,Proxy", "GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "DOMAIN-SUFFIX,a.example,Proxy", "MATCH,Proxy")
	c, err := f.Refresh(t.Context(), store, id, []byte(body), Policy{})
	if err != nil || c.Profile.Template == nil || c.Profile.Template.Reason != subconv.TemplateDropped || len(c.Profile.Nodes) != 3 || downloads.Load() != 1 {
		t.Fatal(err, c.Profile.Template, len(c.Profile.Nodes), downloads.Load())
	}
	b.version, b.set = 43, unusableSet
	downloads.Store(0)
	c, err = f.Refresh(t.Context(), store, id, []byte(body), Policy{})
	if !routesByCopy(c, err) || len(c.Profile.Nodes) != 3 || downloads.Load() != 1 {
		t.Fatal(err, c.Profile.Template, c.Notices, downloads.Load())
	}
}

// The stage starts at its first request, and ends before the refresh's reserve.
func TestTemplateStageDeadline(t *testing.T) {
	start := time.Now()
	tr := &branchTransport{}
	if d := tr.until(); d.Before(start.Add(templateTimeout)) || d.After(time.Now().Add(templateTimeout)) {
		t.Fatal("without a refresh deadline:", d.Sub(start))
	}
	end := time.Now().Add(templateReserve + 5*time.Second)
	tr = &branchTransport{latest: end.Add(-templateReserve)}
	if d := tr.until(); !d.Equal(end.Add(-templateReserve)) || !tr.until().Equal(d) {
		t.Fatal("not before the reserve:", d.Sub(start))
	}
	if !tr.stop() || tr.stop() || !tr.until().IsZero() {
		t.Fatal("a stopped stage has time")
	}
}

func TestSelectionsOfRemovedGroupsReset(t *testing.T) {
	prev := mustParse(t, ownRules)
	next := mustParse(t, strings.ReplaceAll(ownRules, "Mine", "Other"))
	gone, node := prev.Groups[0].ID, prev.Nodes[0].ID
	if c := Assess(next, &prev, Policy{Choices: map[string]string{gone: node}}); c.Decision != "ready" {
		t.Fatal("a selection of a removed group blocked the update", c.Decision)
	}
	kept := next.Groups[0].ID
	if c := Assess(next, &prev, Policy{Choices: map[string]string{kept: "node:" + strings.Repeat("0", 32)}}); c.Decision != "blocked" {
		t.Fatal("a selection in a kept group lost its member silently", c.Decision)
	}
}

// Every notice the router reports has a text in LuCI.
func TestTemplateNoticesHaveTexts(t *testing.T) {
	js, err := os.ReadFile("../../files/www/luci-static/resources/hyper/pages/sources.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"subconv_template_unavailable", "provider_option_ignored", "dialer_proxy_unavailable"} {
		if !strings.Contains(string(js), "\n    "+code+": ") {
			t.Error("no text for", code)
		}
	}
	for _, reason := range []string{subconv.TemplateRequested, subconv.TemplateNoRules, subconv.TemplateNoMatch, subconv.TemplateDropped} {
		if !strings.Contains(string(js), reason+": ") {
			t.Error("no text for template reason", reason)
		}
	}
}
