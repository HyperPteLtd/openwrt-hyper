package subscription

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openweir/subconv/rules"
)

// A source chooses its routing (Source.Rules): its own rules, which subconv replaces with its
// routing template when they are missing or too lossy, or always the template.
const (
	RoutingOwn      = ""
	RoutingTemplate = "template"
)

// Where the router gets subconv's rules branch, and keeps what it last got of it. Without
// hosts every subscription keeps its own routing; tests point both elsewhere.
var (
	TemplateHosts = rules.DefaultHosts()
	TemplateDir   = "/etc/hyper/template"
)

// TemplateUse tells that a profile routes by the built-in template, and why.
type TemplateUse struct {
	// Version is the rules branch's manifest version: the Unix time it was built.
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
	// Cached: no host answered, so the router's copy was used.
	Cached bool `json:"cached,omitempty"`
	// Dropped of Total of the subscription's rules did not convert, when that was the reason.
	Dropped int `json:"dropped,omitempty"`
	Total   int `json:"total,omitempty"`
}

// templateBranch is the rules client of one refresh (Client, nil without hosts). Its requests
// go as provider downloads do (Fetcher.request: public addresses only, the redirect policy, the
// router's second way out), and its answers are kept for the refresh, so the rule sets are
// fetched against the manifest the template came with. It reads and stages the router's copy
// of the branch (templateStage), which promote updates. close releases it.
type templateBranch struct {
	*rules.Client
	stage     *templateStage
	transport *branchTransport
	done      func()
}

func (f Fetcher) branch(ctx context.Context) *templateBranch {
	if len(TemplateHosts) == 0 {
		return &templateBranch{done: func() {}}
	}
	e, done := f.embeddedFetcher()
	b := &templateBranch{stage: &templateStage{root: TemplateDir, ctx: ctx}, done: done}
	b.transport = &branchTransport{f: e, seen: map[string]branchAnswer{}}
	if end, ok := ctx.Deadline(); ok {
		b.transport.latest = end.Add(-templateReserve)
	}
	b.Client = &rules.Client{
		HTTP: &http.Client{
			Transport:     b.transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		// The client keeps its own rollback floor in the cache: a version only one mirror served
		// must not become a floor that agreeing hosts cannot lower.
		Hosts: TemplateHosts,
		Cache: b.stage,
	}
	return b
}

// sets gets the rule sets of the template's providers (fetchTemplateSets) within the stage.
func (b *templateBranch) sets(ctx context.Context, version int64, providers map[string]any) (map[string][]byte, error) {
	ctx, cancel := context.WithDeadline(ctx, b.transport.until())
	defer cancel()
	return fetchTemplateSets(ctx, b.Client, version, providers)
}

// offline leaves the client the router's copy alone: no more requests, and nothing of what the
// refresh got. It reports whether the client was online.
func (b *templateBranch) offline() bool {
	if b.Client == nil || !b.transport.stop() {
		return false
	}
	b.stage.discard()
	return true
}

// promote makes what the refresh got the router's copy, once the router read the template and
// its rule sets. It returns why the copy could not be written (templateStage.promote).
func (b *templateBranch) promote() error {
	if b.stage == nil {
		return nil
	}
	return b.stage.promote()
}

func (b *templateBranch) close() {
	if b.stage != nil {
		b.stage.close()
	}
	b.done()
}

type branchAnswer struct {
	r   response
	err error
}

type branchTransport struct {
	f  Fetcher
	mu sync.Mutex
	// latest is when the template stage must end for the rest of the refresh (zero: no limit),
	// deadline when it ends, from its first request.
	latest, deadline time.Time
	stopped          bool
	seen             map[string]branchAnswer
}

// until is when the template stage ends: templateTimeout after its first request, and
// templateReserve before the refresh's deadline at the latest.
func (t *branchTransport) until() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.deadline.IsZero() {
		t.deadline = time.Now().Add(templateTimeout)
		if !t.latest.IsZero() && t.latest.Before(t.deadline) {
			t.deadline = t.latest
		}
	}
	if t.stopped {
		return time.Time{}
	}
	return t.deadline
}

// stop ends the stage. It reports whether it was still running.
func (t *branchTransport) stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	stopped := t.stopped
	t.stopped = true
	return !stopped
}

func (t *branchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithDeadline(req.Context(), t.until())
	defer cancel()
	// Out of time, no host answers: subconv takes the router's copy, as without hosts.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := req.URL.String()
	t.mu.Lock()
	a, ok := t.seen[key]
	t.mu.Unlock()
	if !ok {
		a.r, a.err = t.f.request(ctx, http.MethodGet, key, nil, "", "Hyper", false, MaxBody)
		// A request the client gave up on, or ran out of time for, says nothing about the host.
		if ctx.Err() == nil {
			t.mu.Lock()
			t.seen[key] = a
			t.mu.Unlock()
		}
	}
	if a.err != nil {
		return nil, a.err
	}
	return &http.Response{
		StatusCode:    a.r.status,
		Status:        fmt.Sprintf("%d %s", a.r.status, http.StatusText(a.r.status)),
		Header:        a.r.header,
		Body:          io.NopCloser(bytes.NewReader(a.r.body)),
		ContentLength: int64(len(a.r.body)),
		Request:       req,
	}, nil
}

// embeddedFetcher is f for the URLs a document or the rules branch chose: public addresses
// only, over the router's second way out too. done releases its connections.
func (f Fetcher) embeddedFetcher() (Fetcher, func()) {
	e := Fetcher{Fallback: f.Fallback, egress: f.egress, embedded: true, Client: f.embeddedClient}
	if e.Client != nil {
		return e, func() {}
	}
	tr := publicProviderTransport()
	e.Client = &http.Client{Transport: tr, Timeout: 15 * time.Second}
	return e, tr.CloseIdleConnections
}

// The template stage of a refresh, the template and then its rule sets, has templateTimeout
// and leaves the refresh's last templateReserve to what follows: the subscription's own
// providers, the candidate's checks and its commit. Out of time, the refresh routes by the
// router's copy of the template, or by default. Rule sets download six at a time: a cold cache
// holds some seventy.
const (
	templateParallel = 6
	templateTimeout  = 40 * time.Second
	templateReserve  = 20 * time.Second
)

var errTemplateSet = errors.New("template_rule_set_unavailable")

// fetchTemplateSets gets the rule set of each of the template's providers (by provider name):
// from the hosts serving the template's version, checked against its manifest, or else from the
// router's copy of that version, which takes no time. It fails when any is unavailable.
func fetchTemplateSets(ctx context.Context, client *rules.Client, version int64, providers map[string]any) (map[string][]byte, error) {
	paths := map[string]string{}
	for name, def := range providers {
		path, ok := branchPath(client.Hosts, field(object(def), "url"))
		if !ok {
			return nil, errTemplateSet
		}
		paths[name] = path
	}
	sets := make(map[string][]byte, len(paths))
	if snap, err := client.Latest(ctx); err == nil && snap.Manifest.Version == version {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var mu sync.Mutex
		var failure error
		slots := make(chan struct{}, templateParallel)
		var wg sync.WaitGroup
		for name, path := range paths {
			wg.Go(func() {
				slots <- struct{}{}
				defer func() { <-slots }()
				if ctx.Err() != nil {
					return
				}
				data, err := snap.Fetch(ctx, path)
				mu.Lock()
				defer mu.Unlock()
				if err != nil && failure == nil {
					failure = err
					cancel()
				}
				sets[name] = data
			})
		}
		wg.Wait()
		if failure != nil || ctx.Err() != nil {
			return nil, errTemplateSet
		}
		return sets, nil
	}
	b, _ := client.Cache.Get(rules.ManifestPath)
	m, err := rules.ParseManifest(b)
	if err != nil || m.Version != version {
		return nil, errTemplateSet
	}
	for name, path := range paths {
		file, listed := m.File(path)
		data, ok := client.Cache.Get(path)
		if !listed || !ok || !file.Matches(data) {
			return nil, errTemplateSet
		}
		sets[name] = data
	}
	return sets, nil
}

// branchPath is a template provider's place on the branch: its URL without the host the
// template was filled in with.
func branchPath(hosts []rules.Host, url string) (string, bool) {
	for _, h := range hosts {
		if p, ok := strings.CutPrefix(url, h.Base+"/"); ok && rules.ValidPath(p) {
			return p, true
		}
	}
	return "", false
}
