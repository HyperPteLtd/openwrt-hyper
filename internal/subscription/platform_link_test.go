package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLinkOnlyAccountUsesFreshOwnerLinkWithoutSessionHeaders(t *testing.T) {
	var revision atomic.Int32
	var configs, details, downloads atomic.Int32
	share := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Referer() != "" {
			t.Error("account credentials reached the subscription domain")
		}
		if r.Method != "GET" || r.URL.Path != fmt.Sprintf("/custom/fixture-link-%d", revision.Load()) ||
			r.URL.RawQuery != "signature=a%2bb&include=west+zone" || r.UserAgent() != subscriptionHeader(platformUserAgent) {
			t.Error("issued path, query, filters or template UA changed")
		}
		w.Header().Set("Content-Disposition", "attachment; filename=hyper.yaml")
		fmt.Fprint(w, platformFixtureYAML)
	}))
	defer share.Close()
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/app/site" {
			fmt.Fprint(w, `{"name":"Fixture"}`)
			return
		}
		if r.URL.Path == "/api/auth/sign-in/email" && r.Method == http.MethodPost {
			w.Header().Set("Set-Auth-Token", "test-session")
			fmt.Fprint(w, `{"user":{"id":"member"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-session" || r.Header.Get("Cookie") != "" {
			t.Error("owner API did not use the signed-in session")
		}
		switch r.URL.Path {
		case "/api/v1/app/billing/subscriptions":
			fmt.Fprint(w, `[{"id":7,"userId":"member","planName":"West","status":"active","usable":true,"url":"https://example.invalid/private-list-link"}]`)
		case "/api/v1/app/proxy/subscriptions/7/nodes":
			fmt.Fprint(w, `{"usable":true,"nodes":[{"protocol":"http"}]}`)
		case "/api/v1/app/proxy/subscriptions/7/config":
			configs.Add(1)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"code":"SUBSCRIPTION_APP_DISABLED"}`)
		case "/api/v1/app/billing/subscriptions/7":
			details.Add(1)
			link := fmt.Sprintf("%s/custom/fixture-link-%d?signature=a%%2bb", share.URL, revision.Load())
			fmt.Fprintf(w, `{"id":7,"userId":"member","status":"active","usable":true,"url":%q,"subscriptionAccess":"link"}`, link)
		default:
			t.Errorf("unexpected API %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	client := host.Client()
	client.Jar, _ = cookiejar.New(nil)
	u, _ := url.Parse(share.URL)
	client.Jar.SetCookies(u, []*http.Cookie{{Name: "account", Value: "fixture"}})

	store := OpenStore(t.TempDir())
	intent, err := store.BeginLogin(host.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := Fetcher{Client: client}
	account, err := f.Login(t.Context(), host.URL, "member@example.invalid", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.SaveAccount(account, intent)
	if err != nil || len(ids) != 1 {
		t.Fatal(err, ids)
	}
	id := ids[0]
	if err = store.Preferences(id, Preferences{Filters: map[string]string{"include": "west zone", "flag": "legacy"}}); err != nil {
		t.Fatal(err)
	}
	for i := int32(1); i <= 2; i++ {
		revision.Store(i)
		c, err := f.Refresh(t.Context(), store, id, nil, Policy{})
		if err != nil || c.Decision != "ready" || c.Profile.Format != "hyper" || len(c.Profile.Nodes) != 1 ||
			c.Profile.Nodes[0].Name != "Edge" || configs.Load() != 0 || details.Load() != i || downloads.Load() != i {
			t.Fatalf("refresh %d: %v, %s, calls %d/%d/%d", i, err, c.Decision, configs.Load(), details.Load(), downloads.Load())
		}
	}
	status, err := store.Status(id)
	if err != nil || !status.Ready || status.LoginRequired || status.Revoked || status.Suspended {
		t.Fatal(status, err)
	}
	public, err := json.Marshal([]any{account, status})
	if err != nil || strings.Contains(string(public), "test-session") || strings.Contains(string(public), "fixture-link") || strings.Contains(string(public), "private-list-link") {
		t.Fatal("link or account credential exposed in RPC response")
	}
	stored, err := os.ReadFile(store.path(id))
	if err != nil || strings.Contains(string(stored), "fixture-link") || strings.Contains(string(stored), "private-list-link") {
		t.Fatal("subscription URL persisted")
	}
}

func TestChangingPlatformAccessSelectsOneTemplatePerRefresh(t *testing.T) {
	var field, linkValue string
	var details, configs, downloads int
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signed" && r.Header.Get("Authorization") != "Bearer member-session" {
			t.Error("missing authenticated account request")
		}
		switch r.URL.Path {
		case "/api/v1/app/proxy/subscriptions/7/nodes":
			fmt.Fprint(w, `{"usable":true,"nodes":[]}`)
		case "/api/v1/app/billing/subscriptions/7":
			details++
			fmt.Fprintf(w, `{"id":7,"userId":"account","status":"active","usable":true,"url":%s%s}`, linkValue, field)
		case "/api/v1/app/proxy/subscriptions/7/config":
			configs++
			fmt.Fprint(w, primary)
		case "/signed":
			downloads++
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("session credentials reached the signed link")
			}
			fmt.Fprint(w, primary)
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	store := OpenStore(t.TempDir())
	src := Source{ID: "source", Kind: "platform", Name: "Plan", Origin: host.URL, UserID: "account", Token: "member-session", PlanID: 7}
	if err := store.configure(src, true); err != nil {
		t.Fatal(err)
	}
	f := Fetcher{Client: host.Client()}
	for _, tt := range []struct {
		mode            string
		issued, useLink bool
	}{
		{"link", true, true}, {"both", true, false}, {"app", false, false},
		{"", true, true}, {"", false, false},
	} {
		field = ""
		if tt.mode != "" {
			field = fmt.Sprintf(`,"subscriptionAccess":%q`, tt.mode)
		}
		linkValue = "null"
		if tt.issued {
			linkValue = strconv.Quote(host.URL + "/signed")
		}
		details, configs, downloads = 0, 0, 0
		c, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{})
		if err != nil || c.Decision != "ready" || details != 1 || configs+downloads != 1 || (downloads == 1) != tt.useLink {
			t.Fatalf("mode %q (link issued %t): %v, decision %s, details/config/link %d/%d/%d", tt.mode, tt.issued, err, c.Decision, details, configs, downloads)
		}
	}
}

func TestPlanDownloadAuthorityAndFailureStates(t *testing.T) {
	const cutOff = -1
	const denied = `{"code":"SUBSCRIPTION_APP_DISABLED"}`
	const accountRevoked = `{"code":"ACCOUNT_REVOKED"}`
	const valid = `{"id":7,"userId":"account","status":"active","usable":true,"url":"LINK","subscriptionAccess":"link"}`
	const both = `{"id":7,"userId":"account","status":"active","usable":true,"url":"LINK","subscriptionAccess":"both"}`
	tests := []struct {
		name, configBody, detailBody, linkBody string
		configCode, detailCode, linkCode       int
		want                                   error
	}{
		{"ordinary forbidden", "{}", both, "", 403, 200, 0, errors.New("subscription_http_403")},
		{"wrong status for link mode", denied, both, "", 404, 200, 0, errors.New("subscription_http_404")},
		{"login expired", "{}", both, "", 401, 200, 0, ErrLoginRequired},
		{"account revoked", accountRevoked, both, "", 403, 200, 0, ErrRevoked},
		{"both app disabled between requests", denied, both, "", 403, 200, 0, errAppDisabled},
		{"detail lost session", "", "{}", "", 0, 401, 0, ErrLoginRequired},
		{"unknown access mode", "", strings.Replace(valid, `"link"`, `"future"`, 1), "", 0, 200, 0, errors.New("invalid_plan_detail")},
		{"null access mode", "", strings.Replace(valid, `"link"`, "null", 1), "", 0, 200, 0, errors.New("invalid_plan_detail")},
		{"empty access mode", "", strings.Replace(valid, `"link"`, `""`, 1), "", 0, 200, 0, errors.New("invalid_plan_detail")},
		{"no issued link", "", strings.Replace(valid, `"LINK"`, "null", 1), "", 0, 200, 0, errLinkUnavailable},
		{"plaintext link", "", strings.Replace(valid, "LINK", "http://invalid.test/fixture", 1), "", 0, 200, 0, errLinkUnavailable},
		{"different owner", "", strings.Replace(valid, "account", "other", 1), "", 0, 200, 0, errors.New("invalid_plan_detail")},
		{"different plan", "", strings.Replace(valid, "7", "8", 1), "", 0, 200, 0, errors.New("invalid_plan_detail")},
		{"expired plan", "", strings.ReplaceAll(strings.ReplaceAll(valid, "active", "expired"), "true", "false"), "", 0, 200, 0, ErrSuspended},
		{"exhausted plan", "", strings.Replace(valid, "true", "false", 1), "", 0, 200, 0, ErrSuspended},
		{"disabled plan", "", strings.ReplaceAll(strings.ReplaceAll(valid, "active", "disabled"), "true", "false"), "", 0, 200, 0, ErrRevoked},
		{"conflicting filter", "", strings.Replace(valid, "LINK", "LINK?include=other", 1), "", 0, 200, 0, errLinkUnavailable},
		{"download host cannot revoke account", "", valid, accountRevoked, 0, 200, 403, errLinkUnavailable},
		{"download host cannot expire session", "", valid, "{}", 0, 200, 401, errLinkUnavailable},
		{"download host cannot redirect link", "", valid, "", 0, 200, 302, errLinkUnavailable},
		{"download host no longer serves link", "", valid, "", 0, 200, 404, errLinkUnavailable},
		// A download host that is busy or a download that breaks off can work on the next attempt.
		{"download host overloaded", "", valid, "", 0, 200, 503, errLinkUnreachable},
		{"download host rate limited", "", valid, "", 0, 200, 429, errLinkUnreachable},
		{"download breaks off", "", valid, primary, 0, 200, cutOff, errLinkUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var configCalls, detailCalls, linkCalls atomic.Int32
			host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/app/proxy/subscriptions/7/nodes":
					fmt.Fprint(w, `{"usable":true,"nodes":[]}`)
				case "/api/v1/app/proxy/subscriptions/7/config":
					configCalls.Add(1)
					if tt.configCode != 0 {
						w.WriteHeader(tt.configCode)
					}
					io.WriteString(w, tt.configBody)
				case "/api/v1/app/billing/subscriptions/7":
					detailCalls.Add(1)
					if tt.detailCode != 0 {
						w.WriteHeader(tt.detailCode)
					}
					io.WriteString(w, strings.Replace(tt.detailBody, "LINK", "https://"+r.Host+"/fixture-download", 1))
				case "/fixture-download":
					linkCalls.Add(1)
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Error("credentials on same-origin link")
					}
					code := tt.linkCode
					switch code {
					case 302:
						w.Header().Set("Location", "/not-authorized")
					case cutOff:
						// The connection closes before the length the answer announced.
						w.Header().Set("Content-Length", strconv.Itoa(len(tt.linkBody)+1024))
						code = http.StatusOK
					}
					if code != 0 {
						w.WriteHeader(code)
					}
					io.WriteString(w, tt.linkBody)
				default:
					t.Error("unexpected route", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer host.Close()
			s := OpenStore(t.TempDir())
			src := Source{ID: "source", Kind: "platform", Name: "Plan", Origin: host.URL, UserID: "account", Token: "test-session", PlanID: 7, Filters: map[string]string{"include": "west"}}
			if err := s.configure(src, true); err != nil {
				t.Fatal(err)
			}
			ticket, err := s.Begin(src.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Commit(ticket, Assess(mustParse(t, primary), nil, Policy{})); err != nil {
				t.Fatal(err)
			}
			c, err := (Fetcher{Client: host.Client()}).Refresh(t.Context(), s, src.ID, nil, Policy{})
			if c.Decision == "ready" || err == nil || (!errors.Is(err, tt.want) && err.Error() != tt.want.Error()) {
				t.Fatal("incorrect refresh result", c.Decision, err)
			}
			st, err := s.Status(src.ID)
			if err != nil || st.LoginRequired != (tt.want == ErrLoginRequired) || st.Suspended != (tt.want == ErrSuspended) || st.Revoked != (tt.want == ErrRevoked) || st.Ready != (!st.Suspended && !st.Revoked) {
				t.Fatal("incorrect cached profile state", st, err)
			}
			if (tt.configCode != 0) != (configCalls.Load() == 1) || detailCalls.Load() != 1 || (tt.linkCode != 0) != (linkCalls.Load() == 1) {
				t.Fatal("unexpected download attempts", detailCalls.Load(), linkCalls.Load())
			}
		})
	}
}

func TestExpiredPlanRemainsEligibleForRefreshWithoutLogin(t *testing.T) {
	var renewed atomic.Bool
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer member-session" {
			t.Error("lost session during suspension")
		}
		switch r.URL.Path {
		case "/api/v1/app/proxy/subscriptions/1/nodes":
			fmt.Fprintf(w, `{"usable":%t,"nodes":[]}`, renewed.Load())
		case "/api/v1/app/billing/subscriptions":
			fmt.Fprint(w, `[{"id":1,"userId":"member","status":"expired","usable":false}]`)
		case "/api/v1/app/billing/subscriptions/1":
			fmt.Fprint(w, `{"id":1,"userId":"member","status":"active","usable":true,"url":null,"subscriptionAccess":"app"}`)
		case "/api/v1/app/proxy/subscriptions/1/config":
			if !renewed.Load() {
				t.Error("attempted template download with expired entitlement")
			}
			fmt.Fprint(w, primary)
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	a := Account{origin: host.URL, userID: "member", token: "member-session", Site: Site{Name: "Platform"},
		Plans: []Plan{{ID: 1, Name: "Plan", Status: "expired"}}}
	store := OpenStore(t.TempDir())
	intent, err := store.BeginLogin(host.URL)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.SaveAccount(a, intent)
	if err != nil || len(ids) != 1 {
		t.Fatal(err)
	}
	f := Fetcher{Client: host.Client()}
	id := ids[0]
	if _, err = store.Load(id); !errors.Is(err, ErrSuspended) {
		t.Fatal("expired plan at login", err)
	}
	if _, err = f.Refresh(t.Context(), store, id, nil, Policy{}); !errors.Is(err, ErrSuspended) {
		t.Fatal("expired plan at refresh", err)
	}
	renewed.Store(true)
	c, err := f.Refresh(t.Context(), store, id, nil, Policy{})
	if err != nil || c.Decision != "ready" {
		t.Fatal("renewed plan still unavailable", err, c.Decision)
	}
	if _, err := store.Load(id); err != nil {
		t.Fatal(err)
	}
}

// A suspended plan that resumes with the content it had tells the router again, which dropped
// it, but keeps its rollback snapshot: the profile it returns with only repeats the current one.
func TestResumedPlanKeepsRollbackSnapshot(t *testing.T) {
	var usable atomic.Bool
	var config atomic.Value
	usable.Store(true)
	config.Store("proxies: [{name: Old, type: http, server: old.example, port: 8080}]\nrules: ['MATCH,DIRECT']")
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/app/proxy/subscriptions/1/nodes":
			fmt.Fprintf(w, `{"usable":%t,"nodes":[]}`, usable.Load())
		case "/api/v1/app/billing/subscriptions":
			fmt.Fprintf(w, `[{"id":1,"userId":"member","status":"active","usable":%t}]`, usable.Load())
		case "/api/v1/app/billing/subscriptions/1":
			fmt.Fprint(w, `{"id":1,"userId":"member","status":"active","usable":true,"url":null,"subscriptionAccess":"app"}`)
		case "/api/v1/app/proxy/subscriptions/1/config":
			fmt.Fprint(w, config.Load())
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	a := Account{origin: host.URL, userID: "member", token: "member-session", Site: Site{Name: "Platform"},
		Plans: []Plan{{ID: 1, Name: "Plan", Status: "active", Usable: true}}}
	store := OpenStore(t.TempDir())
	intent, err := store.BeginLogin(host.URL)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.SaveAccount(a, intent)
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	id := ids[0]
	checks := 0
	f := Fetcher{Client: host.Client(), BeforeCommit: func(context.Context, Profile) (func() error, error) {
		checks++
		return func() error { return nil }, nil
	}}
	if c, err := f.Refresh(t.Context(), store, id, nil, Policy{}); err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision)
	}
	config.Store("proxies: [{name: New, type: http, server: new.example, port: 8080}]\nrules: ['MATCH,DIRECT']")
	if c, err := f.Refresh(t.Context(), store, id, nil, Policy{}); err != nil || c.Decision != "ready" || c.Unchanged() {
		t.Fatal(err, c.Decision)
	}
	usable.Store(false)
	if _, err := f.Refresh(t.Context(), store, id, nil, Policy{}); !errors.Is(err, ErrSuspended) {
		t.Fatal(err)
	}
	usable.Store(true)
	checks = 0
	if c, err := f.Refresh(t.Context(), store, id, nil, Policy{}); err != nil || c.Decision != "ready" || c.Unchanged() || checks != 1 {
		t.Fatal("resumed plan not checked and applied", err, c.Decision, c.Unchanged(), checks)
	}
	r, err := store.read(id)
	if err != nil || r.Suspended || !slices.Equal(nodeNames(r.Current), []string{"New"}) || !slices.Equal(nodeNames(r.Previous), []string{"Old"}) {
		t.Fatal("rollback snapshot after resuming", err, r.Suspended, nodeNames(r.Current), nodeNames(r.Previous))
	}
	if st, err := store.Status(id); err != nil || !st.Ready || !st.HasHistory {
		t.Fatal(err, st.Ready, st.HasHistory)
	}
	if err := store.Rollback(id); err != nil {
		t.Fatal(err)
	}
	if p, err := store.Load(id); err != nil || p.Nodes[0].Name != "Old" {
		t.Fatal(err)
	}
}

func TestIdenticalRefreshUpdatesUsageAndHonorsNewerIntent(t *testing.T) {
	var downloads atomic.Int32
	started, resume := make(chan struct{}), make(chan struct{})
	late, revoked := make(chan struct{}), make(chan struct{})
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := downloads.Add(1)
		if i == 3 {
			close(started)
			<-resume
		}
		if i == 5 {
			close(late)
			<-revoked
		}
		w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=0; total=100", i))
		fmt.Fprint(w, primary)
	}))
	defer host.Close()
	s := OpenStore(t.TempDir())
	if err := s.Configure(Source{ID: "same", Kind: "external", Name: "Repeated", URL: host.URL}); err != nil {
		t.Fatal(err)
	}
	f := Fetcher{Client: host.Client()}
	if c, err := f.Refresh(t.Context(), s, "same", nil, Policy{}); err != nil || c.Unchanged() {
		t.Fatal("first snapshot", err)
	}
	if err := s.update("same", func(r *record) error {
		r.UpdatedAt = "2000-01-01T00:00:00Z"
		r.LastFailure = "subscription_request_failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c, err := f.Refresh(t.Context(), s, "same", nil, Policy{}); err != nil || !c.Unchanged() {
		t.Fatal("identical snapshot", err)
	}
	status, err := s.Status("same")
	if err != nil || status.Usage == nil || status.Usage.Upload != 2 || status.LastFailure != "" || status.UpdatedAt == "2000-01-01T00:00:00Z" || status.HasHistory {
		t.Fatalf("metadata after identical refresh: %+v, err %v", status, err)
	}
	finished := make(chan error, 1)
	go func() { _, err := f.Refresh(t.Context(), s, "same", nil, Policy{}); finished <- err }()
	<-started
	_, newErr := f.Refresh(t.Context(), s, "same", nil, Policy{})
	close(resume)
	if newErr != nil || !errors.Is(<-finished, ErrSuperseded) {
		t.Fatal("stale refresh overwrote newer metadata", newErr)
	}
	status, err = s.Status("same")
	if err != nil || status.Usage == nil || status.Usage.Upload != 4 {
		t.Fatal("stale usage", err)
	}
	go func() { _, err := f.Refresh(t.Context(), s, "same", nil, Policy{}); finished <- err }()
	<-late
	withdrawErr := s.Revoke("same")
	close(revoked)
	if err := <-finished; withdrawErr != nil || !errors.Is(err, ErrSuperseded) {
		t.Fatal("late download reported another withdrawal", withdrawErr, err)
	}
}
