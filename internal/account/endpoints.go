// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package account

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/crypt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
)

// API origins. TXT records at TXTHost add fallbacks, as in hyper-tunnel's domainResolver.
const (
	DefaultAPIBase = "https://api.cloudcdnhyper.com"
	StaticAPIBase  = "https://api.hypervpn.app"
	TXTHost        = "tt.cloudcdnhyper.com"
)

var dohServers = []string{"https://dns.alidns.com/resolve", "https://doh.pub/resolve", "https://dns.google/resolve"}

const (
	txtTTL    = 30 * time.Minute
	txtRetry  = 5 * time.Minute
	healthTTL = time.Minute
)

var (
	hostLabel  = lazyre.New(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	quotedJoin = lazyre.New(`"\s+"`) // TXT strings split across quotes
)

// NormalizeOrigin accepts an HTTPS origin or bare host; TXT records never carry
// credentials, ports, paths or queries.
func NormalizeOrigin(v string) string {
	host := strings.ToLower(strings.TrimSpace(v))
	if strings.ContainsFunc(host, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "https://"), "/")
	if len(host) > 253 || !strings.Contains(host, ".") {
		return ""
	}
	for _, l := range strings.Split(host, ".") {
		if len(l) > 63 || !hostLabel().MatchString(l) {
			return ""
		}
	}
	return "https://" + host
}

func origins(values []string) []string {
	var out []string
	for _, v := range values {
		if o := NormalizeOrigin(v); o != "" && !slices.Contains(out, o) {
			out = append(out, o)
		}
		if len(out) >= 16 {
			break
		}
	}
	return out
}

// ParseTXTOrigins reads a decrypted "v=1|host|host" record.
func ParseTXTOrigins(plain string) []string {
	if !strings.HasPrefix(plain, "v=1|") {
		return nil
	}
	body := strings.ReplaceAll(plain[4:], `\010`, "\n")
	return origins(strings.FieldsFunc(body, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == ';' || r == '|'
	}))
}

// endpointCache is kept in RAM (tmpfs); status polling never touches the network.
type endpointCache struct {
	Version   int      `json:"version"`
	Fallbacks []string `json:"fallbacks"`
	Active    string   `json:"active"`
	UpdatedAt int64    `json:"updated_at"`
	FailedAt  int64    `json:"failed_at"`
	HealthyAt int64    `json:"healthy_at"`
}

// Resolver picks a live API origin.
type Resolver struct {
	CachePath string
	Now       func() time.Time
	// DecryptTXT opens a TXT record; defaults to crypt.OpenTXT.
	DecryptTXT func(string) ([]byte, error)
	// Builtin replaces the built-in origins and DoH servers (tests).
	Builtin []string
	DoH     []string

	mu     sync.Mutex
	cache  *endpointCache
	loaded bool
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) builtin() []string {
	if len(r.Builtin) > 0 {
		return r.Builtin
	}
	return []string{DefaultAPIBase, StaticAPIBase}
}

func (r *Resolver) load() *endpointCache {
	if r.loaded {
		return r.cache
	}
	r.loaded = true
	c := &endpointCache{Version: 1, Active: r.builtin()[0]}
	var raw endpointCache
	if b, err := os.ReadFile(r.CachePath); err == nil && json.Unmarshal(b, &raw) == nil && raw.Version == 1 {
		now := r.now().Unix()
		c.Fallbacks = origins(raw.Fallbacks)
		for _, p := range []struct{ dst, src *int64 }{{&c.UpdatedAt, &raw.UpdatedAt}, {&c.FailedAt, &raw.FailedAt}, {&c.HealthyAt, &raw.HealthyAt}} {
			if *p.src > 0 && *p.src <= now {
				*p.dst = *p.src
			}
		}
		if slices.Contains(append(r.builtin(), c.Fallbacks...), raw.Active) {
			c.Active = raw.Active
		}
	}
	r.cache = c
	return c
}

func (r *Resolver) save() {
	if r.CachePath == "" {
		return
	}
	b, _ := json.Marshal(r.cache)
	_ = writeFile(r.CachePath, append(b, '\n'), 0o600)
}

// Current is the origin requests go to first.
func (r *Resolver) Current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.load().Active
}

func (r *Resolver) candidates() []string {
	c := r.load()
	// Fallbacks were normalized when read; built-in origins are trusted as written.
	var out []string
	for _, o := range append(append([]string{c.Active}, r.builtin()...), c.Fallbacks...) {
		if o != "" && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

func (r *Resolver) healthy(base string) {
	c := r.load()
	c.Active, c.HealthyAt = base, r.now().Unix()
	r.save()
}

func (r *Resolver) failed(base string) {
	c := r.load()
	if c.Active != base {
		return
	}
	c.HealthyAt = 0
	c.Active = r.builtin()[0]
	for _, o := range r.candidates() {
		if o != base {
			c.Active = o
			break
		}
	}
	r.save()
}

// refresh re-reads the TXT fallbacks, bounded to a few seconds.
func (r *Resolver) refresh(ctx context.Context, hc *http.Client, force bool) bool {
	c := r.load()
	now := r.now().Unix()
	if c.FailedAt > 0 && now-c.FailedAt < int64(txtRetry/time.Second) {
		return false
	}
	if !force && c.UpdatedAt > 0 && now-c.UpdatedAt < int64(txtTTL/time.Second) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	decrypt := r.DecryptTXT
	if decrypt == nil {
		decrypt = crypt.OpenTXT
	}
	servers := dohServers
	if len(r.DoH) > 0 {
		servers = r.DoH
	}
	for _, server := range servers {
		if ctx.Err() != nil {
			break
		}
		found := lookupTXT(ctx, hc, server, decrypt)
		if len(found) == 0 {
			continue
		}
		c.Fallbacks, c.UpdatedAt, c.FailedAt = found, r.now().Unix(), 0
		if !slices.Contains(append(r.builtin(), found...), c.Active) {
			c.Active, c.HealthyAt = r.builtin()[0], 0
		}
		r.save()
		return true
	}
	c.FailedAt = r.now().Unix()
	r.save()
	return true
}

func lookupTXT(ctx context.Context, hc *http.Client, server string, decrypt func(string) ([]byte, error)) []string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"?name="+url.QueryEscape(TXTHost)+"&type=TXT", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/dns-json, application/json")
	res, err := hc.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 65537))
	if res.StatusCode != 200 || len(body) > 65536 {
		return nil
	}
	var data struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if json.Unmarshal(body, &data) != nil || data.Status != 0 {
		return nil
	}
	var found []string
	for i, a := range data.Answer {
		if i >= 32 {
			break
		}
		if a.Type != 16 {
			continue
		}
		record := strings.TrimSpace(quotedJoin().ReplaceAllString(strings.Trim(a.Data, `"`), ""))
		if len(record) > 16384 {
			continue
		}
		if plain, err := decrypt(record); err == nil {
			found = origins(append(found, ParseTXTOrigins(string(plain))...))
		}
	}
	return found
}

func retryable(res *http.Response, err error) bool {
	if err != nil || res == nil {
		return true
	}
	s := res.StatusCode
	return (s >= 300 && s < 400) || s == 408 || s >= 500
}

// do sends one API request, trying origins in order. Non-replayable requests (login,
// codes, payments without an idempotency key) first confirm a live origin with a public
// GET and are sent at most once.
func (r *Resolver) do(ctx context.Context, hc *http.Client, build func(base string) (*http.Request, error), replayable bool) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	refreshed := r.refresh(ctx, hc, false)
	tried := map[string]bool{}
	for round := 0; round < 2; round++ {
		for _, base := range r.candidates() {
			if tried[base] || ctx.Err() != nil {
				continue
			}
			tried[base] = true
			c := r.load()
			if !replayable && !(base == c.Active && c.HealthyAt > 0 && r.now().Unix()-c.HealthyAt < int64(healthTTL/time.Second)) {
				if !probe(ctx, hc, base) {
					r.failed(base)
					continue
				}
				r.healthy(base)
			}
			req, err := build(base)
			if err != nil {
				return nil, err
			}
			timeout := 15 * time.Second
			if replayable {
				timeout = 8 * time.Second
			}
			rctx, cancel := context.WithTimeout(ctx, timeout)
			res, err := hc.Do(req.WithContext(rctx))
			if !retryable(res, err) {
				body, rerr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
				res.Body.Close()
				cancel()
				if rerr != nil {
					return nil, rerr
				}
				res.Body = io.NopCloser(strings.NewReader(string(body)))
				r.healthy(base)
				return res, nil
			}
			if res != nil {
				res.Body.Close()
			}
			cancel()
			r.failed(base)
			if !replayable {
				return nil, errUnavailable
			}
		}
		if round == 0 && !refreshed && ctx.Err() == nil {
			r.refresh(ctx, hc, true)
		}
	}
	return nil, errUnavailable
}

func probe(ctx context.Context, hc *http.Client, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/common/site/config", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var data map[string]any
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&data) != nil {
		return false
	}
	if code, ok := data["code"].(float64); ok && code != 0 && code != 200 {
		return false
	}
	return true
}
