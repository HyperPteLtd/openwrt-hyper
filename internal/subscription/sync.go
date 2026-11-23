// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package subscription syncs nodes and routing rules from the signed-in Hyper account,
// in the same flow as the Hyper app (hyper-tunnel app-client subscribe/client.ts):
//
//  1. GET /v1/public/user/subscribe                 -> subscription token (device session)
//  2. GET <api>/api/subscribe/location?token=        -> countries with nodes
//  3. per country: X25519 key pair, then GET <api>/api/subscribe?token=&country=&client_public_key=
//     -> {spk, salt, nonce, data}, opened with info "subscribe-v1" -> REMARKS=/STATUS=/share links
//  4. GET <api>/api/subscribe/rules (JWT)            -> rule catalog -> /etc/hyper/rules
//
// A failed fetch keeps the previous nodes; only a successful sync replaces them. Results
// are dropped when the account changes while the sync runs.
package subscription

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/crypt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
)

const (
	MaxCountries  = 64
	subscribeInfo = "subscribe-v1"
)

// State is written to sync.json and shown by status / sync_status.
type State struct {
	Time         int64          `json:"time"`
	OK           bool           `json:"ok"`
	Error        string         `json:"error,omitempty"`
	Msg          string         `json:"msg,omitempty"`
	Status       any            `json:"status,omitempty"`
	Count        int            `json:"count"`
	Added        int            `json:"added"`
	Removed      int            `json:"removed"`
	Invalid      int            `json:"invalid"`
	Unsupported  map[string]int `json:"unsupported,omitempty"`
	Countries    []string       `json:"countries,omitempty"`
	Failed       []string       `json:"failed,omitempty"`
	Remarks      string         `json:"remarks,omitempty"`
	TrafficUsed  string         `json:"traffic_used,omitempty"`
	TrafficTotal string         `json:"traffic_total,omitempty"`
	Expires      string         `json:"expires,omitempty"`
	Rules        *int           `json:"rules,omitempty"`
}

// Syncer runs one sync at a time (the caller serialises).
type Syncer struct {
	Client    *account.Client
	NodesPath string
	RulesDir  string
	// Proxy returns the core's local proxy URL when it is running ("" otherwise). Reads that
	// fail on a connection or server error are retried the other way.
	Proxy func() string
	// InsecureDefault is subscription.allow_insecure.
	InsecureDefault bool
	Log             func(format string, args ...any)
	Now             func() time.Time
	// HTTP fetches rule list URLs; defaults to account.DefaultHTTP.
	HTTP func(proxy string) *http.Client
}

// Outcome of a sync.
type Outcome struct {
	State        State
	NodesChanged bool
	RulesChanged bool
	Nodes        *nodes.Store // the store as saved (nil on failure)
}

var ErrSessionChanged = account.ErrSessionChanged

func (s *Syncer) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type run struct {
	*Syncer
	ctx      context.Context
	identity string
	proxy    string // the route that last worked
	alt      string // the core's proxy, if any
}

func (r *run) same() bool {
	sess := r.Client.Sessions.Load()
	return sess != nil && sess.Identity() == r.identity
}

// read retries a read once over the other route on connection or server errors.
func (r *run) read(f func(proxy string) account.Result) (account.Result, error) {
	if !r.same() {
		return account.Result{}, ErrSessionChanged
	}
	res := f(r.proxy)
	if !r.same() {
		return account.Result{}, ErrSessionChanged
	}
	if !res.OK && r.alt != "" && (res.Status == 0 || res.Status == 408 || res.Status >= 500) {
		other := r.alt
		if r.proxy != "" {
			other = ""
		}
		res = f(other)
		if !r.same() {
			return account.Result{}, ErrSessionChanged
		}
		if res.OK {
			r.proxy = other
		}
	}
	return res, nil
}

// Run syncs once. A nil error with State.OK false is a reported failure.
func (s *Syncer) Run(ctx context.Context) (Outcome, error) {
	sess := s.Client.Sessions.Load()
	if sess == nil {
		s.logf("Not signed in, skipping.")
		return Outcome{State: State{Time: s.now().Unix(), Error: "not_signed_in"}}, nil
	}
	r := &run{Syncer: s, ctx: ctx, identity: sess.Identity()}
	if s.Proxy != nil {
		r.alt = s.Proxy()
	}
	out, err := r.sync()
	out.State.Time = s.now().Unix()
	return out, err
}

func fail(code string, status int, msg string) Outcome {
	st := State{Error: code, Msg: msg}
	if status != 0 {
		st.Status = status
	}
	return Outcome{State: st}
}

func (r *run) sync() (Outcome, error) {
	var token string
	res, err := r.read(func(proxy string) account.Result {
		res, tok := r.Client.RefreshSubscriptionToken(r.ctx, proxy)
		token = tok
		return res
	})
	if err != nil {
		return Outcome{}, err
	}
	if !res.OK {
		code := res.Error
		if code == "" {
			code = "subscription_token"
		}
		msg := ""
		if res.Msg != nil {
			msg = *res.Msg
		}
		r.logf("Failed to get the subscription token: %s", firstNonEmpty(msg, code))
		return fail(code, 0, msg), nil
	}

	loc, err := r.read(func(proxy string) account.Result {
		return r.Client.Request(r.ctx, "GET", account.SubscribePath+"/location", account.Options{NoAuth: true, Proxy: proxy,
			Timeout: 20 * time.Second, Query: map[string]string{"token": token}})
	})
	if err != nil {
		return Outcome{}, err
	}
	countries := ParseLocations(loc.Data)
	if !loc.OK || len(countries) == 0 {
		r.logf("Failed to list countries (HTTP %d).", loc.Status)
		code := "locations"
		if loc.OK {
			code = "no_countries"
		}
		return fail(code, loc.Status, ""), nil
	}
	if len(countries) > MaxCountries {
		countries = countries[:MaxCountries]
	}

	fresh := map[string]nodes.Node{}
	var order []string
	st := State{Unsupported: map[string]int{}}
	var failed []string
	for _, c := range countries {
		p, err := r.country(token, c)
		if errors.Is(err, ErrSessionChanged) {
			return Outcome{}, err
		}
		if err != nil {
			failed = append(failed, c.Code)
			r.logf("Country %s: %v.", c.Code, err)
			continue
		}
		st.Invalid += p.Invalid
		for k, v := range p.Unsupported {
			st.Unsupported[k] += v
		}
		if st.Status == nil && p.Status != "" {
			st.Status = p.Status
		}
		if st.Remarks == "" {
			st.Remarks = p.Remarks
		}
		for _, n := range p.Nodes {
			if _, dup := fresh[n.ID]; !dup {
				order = append(order, n.ID)
			}
			fresh[n.ID] = n
		}
	}
	if len(failed) == len(countries) {
		r.logf("Every country failed, keeping the previous nodes.")
		o := fail("nodes", 0, "")
		o.State.Failed = failed
		return o, nil
	}
	if !r.same() {
		return Outcome{}, ErrSessionChanged
	}

	before, err := nodes.Load(r.NodesPath)
	if err != nil {
		before = &nodes.Store{}
	}
	next := &nodes.Store{}
	for _, id := range order {
		next.Nodes = append(next.Nodes, fresh[id])
	}
	added, removed := 0, 0
	for _, old := range before.Nodes {
		if _, ok := fresh[old.ID]; ok {
			continue
		}
		// Nodes of a country that failed this time stay until it answers again.
		if slices.Contains(failed, old.Country) {
			next.Nodes = append(next.Nodes, old)
			continue
		}
		removed++
	}
	for _, id := range order {
		if _, ok := before.Find(id); !ok {
			added++
		}
	}
	changed := !sameNodes(before, next)
	if changed {
		if err := next.Save(r.NodesPath); err != nil {
			return fail("nodes_write_failed", 0, err.Error()), nil
		}
	}
	if !r.same() {
		return Outcome{}, ErrSessionChanged
	}

	rulesChanged, ruleCount, err := r.rules()
	if errors.Is(err, ErrSessionChanged) {
		return Outcome{}, err
	}
	st.OK, st.Count, st.Added, st.Removed, st.Failed = true, len(fresh), added, removed, failed
	for _, c := range countries {
		st.Countries = append(st.Countries, c.Code)
	}
	if s, ok := st.Status.(string); ok {
		st.TrafficUsed, st.TrafficTotal, st.Expires = parseStatus(s)
	}
	if err == nil {
		st.Rules = &ruleCount
	}
	if len(st.Unsupported) == 0 {
		st.Unsupported = nil
	}
	r.logf("%d nodes from %d countries (%d added, %d removed, %d invalid links).",
		len(fresh), len(countries)-len(failed), added, removed, st.Invalid)
	for k, v := range st.Unsupported {
		r.logf("Skipped %d link(s): %s.", v, k)
	}
	return Outcome{State: st, NodesChanged: changed, RulesChanged: rulesChanged, Nodes: next}, nil
}

func sameNodes(a, b *nodes.Store) bool {
	x, _ := json.Marshal(a.Nodes)
	y, _ := json.Marshal(b.Nodes)
	return string(x) == string(y)
}

func (r *run) country(token string, c Location) (*nodes.Parsed, error) {
	priv, pub, err := crypt.Keygen(rand.Reader)
	if err != nil {
		return nil, err
	}
	res, err := r.read(func(proxy string) account.Result {
		return r.Client.Request(r.ctx, "GET", account.SubscribePath, account.Options{NoAuth: true, Raw: true, Proxy: proxy,
			Timeout: 30 * time.Second, Query: map[string]string{"token": token, "country": c.Code, "client_public_key": crypt.EncodeB64URL(pub)}})
	})
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("HTTP %d", res.Status)
	}
	body, err := decryptBody(res.Body, priv)
	if err != nil {
		return nil, err
	}
	return nodes.Parse(body, nodes.ParseOptions{Country: c.Code, Sort: c.Sort, InsecureDefault: r.InsecureDefault})
}

// decryptBody opens an encrypted {spk, salt, nonce, data} envelope; plain bodies pass through.
func decryptBody(body []byte, priv []byte) ([]byte, error) {
	var env any
	if json.Unmarshal(body, &env) != nil {
		return body, nil
	}
	switch v := env.(type) {
	case string:
		return []byte(v), nil
	case map[string]any:
		get := func(k string) ([]byte, error) {
			s, _ := v[k].(string)
			if s == "" {
				return nil, errors.New("decrypt")
			}
			return crypt.DecodeB64(s)
		}
		spk, e1 := get("spk")
		salt, e2 := get("salt")
		nonce, e3 := get("nonce")
		data, e4 := get("data")
		if err := errors.Join(e1, e2, e3, e4); err != nil {
			return nil, errors.New("decrypt")
		}
		plain, err := crypt.Open(priv, spk, salt, nonce, subscribeInfo, data)
		if err != nil {
			return nil, errors.New("decrypt")
		}
		return plain, nil
	}
	// A JSON document that is not an envelope (a sing-box or Clash subscription).
	return body, nil
}

// Location is one country of /api/subscribe/location, highest sort first.
type Location struct {
	Code string
	Sort int
}

var countryCode = lazyre.New(`^[A-Za-z0-9_-]{1,16}$`)

// ParseLocations reads {locations:{CODE:{sort}}} (optionally under data).
func ParseLocations(raw json.RawMessage) []Location {
	var doc struct {
		Data      json.RawMessage            `json:"data"`
		Locations map[string]json.RawMessage `json:"locations"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	if doc.Locations == nil && doc.Data != nil {
		_ = json.Unmarshal(doc.Data, &doc)
	}
	var out []Location
	for code, v := range doc.Locations {
		c := strings.TrimSpace(code)
		if !countryCode().MatchString(c) {
			continue
		}
		var meta struct {
			Sort any `json:"sort"`
		}
		_ = json.Unmarshal(v, &meta)
		sort := -1000000
		switch x := meta.Sort.(type) {
		case float64:
			sort = int(x)
		case string:
			if n, err := strconv.Atoi(x); err == nil {
				sort = n
			}
		}
		out = append(out, Location{c, sort})
	}
	slices.SortFunc(out, func(a, b Location) int {
		if a.Sort != b.Sort {
			return b.Sort - a.Sort
		}
		return strings.Compare(a.Code, b.Code)
	})
	return out
}

func parseStatus(s string) (used, total, expires string) {
	for _, part := range strings.Split(s, "|") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "Traffic:"):
			if t := strings.Split(strings.TrimSpace(part[8:]), "/"); len(t) == 2 {
				used, total = strings.TrimSpace(t[0]), strings.TrimSpace(t[1])
			}
		case strings.HasPrefix(part, "Expires:"):
			expires = strings.TrimSpace(part[8:])
		}
	}
	return
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// rules syncs the server rule catalog into RulesDir. The index is published last, then
// files no longer referenced are removed.
func (r *run) rules() (changed bool, count int, err error) {
	res, err := r.read(func(proxy string) account.Result {
		return r.Client.Request(r.ctx, "GET", account.SubscribePath+"/rules", account.Options{Proxy: proxy, Timeout: 20 * time.Second})
	})
	if err != nil {
		return false, 0, err
	}
	if !res.OK {
		r.logf("Rules: fetch failed (HTTP %d), keeping the previous rules.", res.Status)
		return false, 0, errors.New("rules")
	}
	entries, errs := NormalizeCatalog(res.Data)
	for _, e := range errs {
		r.logf("Rules: %s", e)
	}
	if err := os.MkdirAll(r.RulesDir, 0o755); err != nil {
		return false, 0, err
	}
	var previous []IndexEntry
	before, _ := os.ReadFile(filepath.Join(r.RulesDir, "index.json"))
	_ = json.Unmarshal(before, &previous)
	var index []IndexEntry
	for _, e := range entries {
		text := e.Inline
		if e.URL != "" {
			body, status := r.download(e.URL)
			if body == nil {
				var prev *IndexEntry
				for i := range previous {
					if previous[i].Key == e.Key {
						prev = &previous[i]
					}
				}
				suffix := ""
				if prev != nil {
					suffix = ", keeping the previous copy"
					p := *prev
					p.Policy, p.Enabled, p.Sort, p.Stale = e.Policy, e.Enabled, e.Sort, true
					index = append(index, p)
				}
				r.logf("Rules: %s download failed (HTTP %d)%s.", e.Key, status, suffix)
				continue
			}
			text = string(body)
		}
		if !r.same() {
			return false, 0, ErrSessionChanged
		}
		list := ParseRuleList(text)
		domain, ip := list.Files()
		id := rulekey.ID(e.Key)
		for name, body := range map[string][]byte{id + ".domain.json": domain, id + ".ip.json": ip} {
			if body != nil {
				if err := publish(filepath.Join(r.RulesDir, name), body); err != nil {
					r.logf("Rules: cannot publish a rule file, keeping the previous index.")
					return false, 0, err
				}
			}
		}
		source := "inline"
		if e.URL != "" {
			source = "url"
		}
		geo := list.GeoIP
		if geo == nil {
			geo = []string{}
		}
		index = append(index, IndexEntry{Key: e.Key, Policy: e.Policy, Enabled: e.Enabled, Sort: e.Sort,
			HasDomain: domain != nil, HasIP: ip != nil, GeoIP: geo, Count: list.Count, Skipped: list.Skipped, Source: source})
	}
	if index == nil {
		index = []IndexEntry{}
	}
	after, _ := json.Marshal(index)
	after = append(after, '\n')
	if !r.same() {
		return false, 0, ErrSessionChanged
	}
	if err := publish(filepath.Join(r.RulesDir, "index.json"), after); err != nil {
		r.logf("Rules: cannot publish the rule index, keeping the previous index.")
		return false, 0, err
	}
	keep := map[string]bool{"index.json": true}
	for _, e := range index {
		id := rulekey.ID(e.Key)
		keep[id+".domain.json"], keep[id+".ip.json"] = true, true
	}
	if list, err := os.ReadDir(r.RulesDir); err == nil {
		for _, f := range list {
			if !keep[f.Name()] && !f.IsDir() {
				os.Remove(filepath.Join(r.RulesDir, f.Name()))
			}
		}
	}
	r.logf("Rules: %d lists.", len(index))
	return string(before) != string(after), len(index), nil
}

// maxRuleList bounds one downloaded rule list.
const maxRuleList = 16 << 20

func (r *run) download(url string) ([]byte, int) {
	status := 0
	get := func(proxy string) []byte {
		hc := account.DefaultHTTP(proxy)
		if r.HTTP != nil {
			hc = r.HTTP(proxy)
		}
		ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("User-Agent", "luci-app-hyper")
		res, err := hc.Do(req)
		if err != nil {
			return nil
		}
		defer res.Body.Close()
		status = res.StatusCode
		if status != 200 {
			return nil
		}
		// A list over the limit is refused, never kept cut short; a transfer cut off on the
		// way (status 0 again) is tried over the other route, as a failed connection is.
		body, err := io.ReadAll(io.LimitReader(res.Body, maxRuleList+1))
		if err != nil {
			status = 0
			return nil
		}
		if len(body) > maxRuleList {
			return nil
		}
		return body
	}
	if b := get(r.proxy); b != nil {
		return b, status
	}
	if r.alt != "" && (status == 0 || status >= 500) {
		other := r.alt
		if r.proxy != "" {
			other = ""
		}
		if b := get(other); b != nil {
			r.proxy = other
			return b, status
		}
	}
	return nil, status
}

func publish(path string, data []byte) error { return atomicfile.WriteFile(path, data, 0o644) }
