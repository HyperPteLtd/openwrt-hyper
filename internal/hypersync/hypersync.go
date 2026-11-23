// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package hypersync reads the signed-in Hyper account's subscription in the same flow as the
// Hyper app (app-client subscribe/client.ts):
//
//  1. GET /v1/public/user/subscribe                 -> subscription token (device session)
//  2. GET <api>/api/subscribe/location?token=        -> countries with nodes
//  3. per country: X25519 key pair, then GET <api>/api/subscribe?token=&country=&client_public_key=
//     -> {spk, salt, nonce, data}, opened with info "subscribe-v1" -> REMARKS=/STATUS=/share links
//  4. GET <api>/api/subscribe/rules (JWT)            -> rule catalog, each list inline or a URL
//
// It only reads: the subscription layer turns a Feed into a profile. Results are dropped when
// the account changes while it reads.
package hypersync

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/crypt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
)

const (
	MaxCountries  = 64
	subscribeInfo = "subscribe-v1"
	// maxRuleList bounds one downloaded rule list; a longer one is refused, never cut short.
	maxRuleList = 16 << 20
)

var (
	ErrNotSignedIn    = errors.New("not_signed_in")
	ErrSessionChanged = account.ErrSessionChanged
	// ErrLoginRequired: the server no longer accepts the session (HTTP 401).
	ErrLoginRequired = errors.New("login_required")
	// ErrNoSubscription: the account has no subscription the router can use.
	ErrNoSubscription = errors.New("no_subscription")
)

// Reader reads one feed at a time (the caller serialises).
type Reader struct {
	Client *account.Client
	// Alt names the router's other way out ("" when there is none): a read that fails on a
	// connection or server error is retried once that way. Client.HTTP and HTTP know it.
	Alt string
	// HTTP fetches rule list URLs over a way out ("" or Alt).
	HTTP func(way string) *http.Client
	Log  func(format string, args ...any)
}

// Feed is what one read found.
type Feed struct {
	Countries []Country // highest sort first; read ones only
	Failed    []string  // countries that could not be read
	Remarks   string
	Status    string // "Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09"
	// Catalog is nil when the rule catalog could not be read: the previous rules stay.
	Catalog *Catalog
}

// Country is one country's decrypted share links.
type Country struct {
	Code string
	Sort int
	Body []byte
}

// Catalog is the server's rule lists in catalog order.
type Catalog struct {
	Lists  []List
	Errors []string // entries left out of the catalog
}

// List is one catalog entry with its content; Parsed is nil when its URL could not be read
// (Status is the HTTP status, 0 when the transfer failed): its previous copy stays.
type List struct {
	CatalogEntry
	Parsed *RuleList
	Status int
}

type run struct {
	*Reader
	ctx      context.Context
	identity string
	way      string // the way out that last worked
}

func (r *Reader) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *run) same() bool {
	sess := r.Client.Sessions.Load()
	return sess != nil && sess.Identity() == r.identity
}

// read retries a read once the other way on connection or server errors.
func (r *run) read(f func(way string) account.Result) (account.Result, error) {
	if !r.same() {
		return account.Result{}, ErrSessionChanged
	}
	res := f(r.way)
	if !r.same() {
		return account.Result{}, ErrSessionChanged
	}
	if !res.OK && r.Alt != "" && (res.Status == 0 || res.Status == 408 || res.Status >= 500) {
		other := r.Alt
		if r.way != "" {
			other = ""
		}
		res = f(other)
		if !r.same() {
			return account.Result{}, ErrSessionChanged
		}
		if res.OK {
			r.way = other
		}
	}
	return res, nil
}

// Read reads the account's nodes and rule catalog. Every country failing is an error; some
// failing are listed in Failed.
func (r *Reader) Read(ctx context.Context) (*Feed, error) {
	sess := r.Client.Sessions.Load()
	if sess == nil {
		return nil, ErrNotSignedIn
	}
	run := &run{Reader: r, ctx: ctx, identity: sess.Identity()}
	var token string
	res, err := run.read(func(way string) account.Result {
		res, tok := r.Client.RefreshSubscriptionToken(ctx, way)
		token = tok
		return res
	})
	if err != nil {
		return nil, err
	}
	if !res.OK {
		switch {
		case res.Status == http.StatusUnauthorized:
			return nil, ErrLoginRequired
		case res.Error == "no_subscription":
			return nil, ErrNoSubscription
		case res.Error == account.ErrSessionChanged.Error():
			return nil, ErrSessionChanged
		}
		msg := ""
		if res.Msg != nil {
			msg = *res.Msg
		}
		r.logf("Failed to get the subscription token: %s", firstNonEmpty(msg, res.Error, fmt.Sprintf("HTTP %d", res.Status)))
		return nil, failure("subscription_token", res)
	}

	loc, err := run.read(func(way string) account.Result {
		return r.Client.Request(ctx, "GET", account.SubscribePath+"/location", account.Options{NoAuth: true, Proxy: way,
			Timeout: 20 * time.Second, Query: map[string]string{"token": token}})
	})
	if err != nil {
		return nil, err
	}
	countries := ParseLocations(loc.Data)
	if !loc.OK || len(countries) == 0 {
		r.logf("Failed to list countries (HTTP %d).", loc.Status)
		if loc.OK {
			return nil, errors.New("no_countries")
		}
		return nil, failure("locations", loc)
	}
	if len(countries) > MaxCountries {
		countries = countries[:MaxCountries]
	}

	feed := &Feed{}
	for _, c := range countries {
		body, err := run.country(token, c)
		if errors.Is(err, ErrSessionChanged) {
			return nil, err
		}
		if err != nil {
			feed.Failed = append(feed.Failed, c.Code)
			r.logf("Country %s: %v.", c.Code, err)
			continue
		}
		text, remarks, status := splitBody(body)
		if feed.Remarks == "" {
			feed.Remarks = remarks
		}
		if feed.Status == "" {
			feed.Status = status
		}
		feed.Countries = append(feed.Countries, Country{Code: c.Code, Sort: c.Sort, Body: text})
	}
	if len(feed.Countries) == 0 {
		r.logf("Every country failed.")
		return nil, errors.New("nodes")
	}
	feed.Catalog, err = run.catalog()
	if errors.Is(err, ErrSessionChanged) {
		return nil, err
	}
	if !run.same() {
		return nil, ErrSessionChanged
	}
	return feed, nil
}

// failure is a stable code for a failed read: transient ones (no answer, 408, 5xx) say so.
func failure(what string, res account.Result) error {
	if res.Status == 0 || res.Status == 408 || res.Status >= 500 {
		return errors.New(what + "_unreachable")
	}
	return fmt.Errorf("%s_http_%d", what, res.Status)
}

func (r *run) country(token string, c Location) ([]byte, error) {
	priv, pub, err := crypt.Keygen(rand.Reader)
	if err != nil {
		return nil, err
	}
	res, err := r.read(func(way string) account.Result {
		return r.Client.Request(r.ctx, "GET", account.SubscribePath, account.Options{NoAuth: true, Raw: true, Proxy: way,
			Timeout: 30 * time.Second, Query: map[string]string{"token": token, "country": c.Code, "client_public_key": crypt.EncodeB64URL(pub)}})
	})
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("HTTP %d", res.Status)
	}
	return decryptBody(res.Body, priv)
}

// splitBody decodes a base64 body and takes out its REMARKS= / STATUS= lines; the rest are
// share links.
func splitBody(body []byte) (text []byte, remarks, status string) {
	b := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	if !bytes.Contains(b, []byte("://")) {
		if dec, err := decodeBase64(b); err == nil {
			b = bytes.TrimSpace(dec)
		}
	}
	var out strings.Builder
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "REMARKS="):
			remarks = strings.TrimSpace(line[len("REMARKS="):])
		case strings.HasPrefix(line, "STATUS="):
			status = strings.TrimSpace(line[len("STATUS="):])
		case line != "":
			out.WriteString(line + "\n")
		}
	}
	return []byte(out.String()), remarks, status
}

func decodeBase64(b []byte) ([]byte, error) {
	s := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' {
			return -1
		}
		return r
	}, string(b))
	s = strings.TrimRight(s, "=")
	if strings.ContainsAny(s, "-_") {
		return base64.RawURLEncoding.DecodeString(s)
	}
	return base64.RawStdEncoding.DecodeString(s)
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
	// A JSON document that is not an envelope.
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

// ParseStatus reads "Traffic: <used> / <total> | Expires: <date>".
func ParseStatus(s string) (used, total, expires string) {
	for part := range strings.SplitSeq(s, "|") {
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

// catalog reads the rule catalog and every list's content. A failed catalog is nil (the
// previous rules stay); a failed list has no content (its previous copy stays).
func (r *run) catalog() (*Catalog, error) {
	res, err := r.read(func(way string) account.Result {
		return r.Client.Request(r.ctx, "GET", account.SubscribePath+"/rules", account.Options{Proxy: way, Timeout: 20 * time.Second})
	})
	if err != nil {
		return nil, err
	}
	if !res.OK {
		r.logf("Rules: fetch failed (HTTP %d), keeping the previous rules.", res.Status)
		return nil, nil
	}
	entries, errs := NormalizeCatalog(res.Data)
	for _, e := range errs {
		r.logf("Rules: %s", e)
	}
	c := &Catalog{Errors: errs}
	for _, e := range entries {
		l := List{CatalogEntry: e}
		text := e.Inline
		if e.URL != "" {
			body, status := r.download(e.URL)
			l.Status = status
			if body == nil {
				r.logf("Rules: %s download failed (HTTP %d).", e.Key, status)
				c.Lists = append(c.Lists, l)
				continue
			}
			text = string(body)
		}
		if !r.same() {
			return nil, ErrSessionChanged
		}
		parsed := ParseRuleList(text)
		l.Parsed = &parsed
		c.Lists = append(c.Lists, l)
	}
	return c, nil
}

func (r *run) download(url string) ([]byte, int) {
	status := 0
	get := func(way string) []byte {
		hc := http.DefaultClient
		if r.HTTP != nil {
			hc = r.HTTP(way)
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
		// A transfer cut off on the way (status 0 again) is tried the other way.
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
	if b := get(r.way); b != nil {
		return b, status
	}
	if r.Alt != "" && (status == 0 || status >= 500) {
		other := r.Alt
		if r.way != "" {
			other = ""
		}
		if b := get(other); b != nil {
			r.way = other
			return b, status
		}
	}
	return nil, status
}
