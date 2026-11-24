// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/coreproxy"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const userAgent = "luci-app-hyper (OpenWrt)"

// EmailCodeTypeAuth is the verification code type for login (as in the Hyper app).
const EmailCodeTypeAuth = 2

// SubscribePath is where subscription endpoints live under the API origin.
const SubscribePath = "/api/subscribe"

var (
	errUnavailable = errors.New("api_unavailable")
	errTimeout     = errors.New("timeout")
)

// Paths the browser may reach through the `api` method: what the account pages read, and
// signing a device out. Everything else, purchases included (the website's), is refused.
var allowlist = []func() *regexp.Regexp{
	lazyre.New(`^/v1/common/site/config$`),
	lazyre.New(`^/v1/public/announcement/list$`),
	lazyre.New(`^/v1/public/user/(info|subscribe|devices|device/kick_offline|affiliate/count)$`),
}

// AllowedPath reports whether the browser may call path through the router.
func AllowedPath(path string) bool {
	if strings.Contains(path, "..") || strings.ContainsAny(path, "?#") ||
		strings.ContainsFunc(path, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return false
	}
	for _, re := range allowlist {
		if re().MatchString(path) {
			return true
		}
	}
	return false
}

var idempotencyKey = lazyre.New(`^[A-Za-z0-9._:-]{8,128}$`)

// ValidIdempotencyKey is the Idempotency-Key shape payment-v2 accepts.
func ValidIdempotencyKey(k string) bool { return idempotencyKey().MatchString(k) }

// Client calls Hyper service.
type Client struct {
	Sessions *Sessions
	Resolver *Resolver
	DeviceID func() string
	// HTTP builds the client for a proxy URL ("" = direct).
	HTTP func(proxy string) *http.Client
}

// Options of one request.
type Options struct {
	Query          map[string]string
	Body           any
	NoAuth         bool
	Raw            bool // return the body unparsed in Result.Body
	Proxy          string
	IdempotencyKey string
	Timeout        time.Duration
}

// Result is one API reply: wrapped replies ({code,msg,data}) are unwrapped.
type Result struct {
	OK     bool            `json:"ok"`
	Status int             `json:"status"`
	Code   *int            `json:"code"`
	Msg    *string         `json:"msg"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
	Body   []byte          `json:"-"`
}

func (c *Client) client(proxy string) *http.Client {
	if c.HTTP != nil {
		return c.HTTP(proxy)
	}
	return DefaultHTTP(proxy)
}

// ErrPlainOverProxy: only TLS authenticates the far end through the core's proxy.
var ErrPlainOverProxy = errors.New("plain http is not sent through the proxy")

// DefaultHTTP is a client without redirects, optionally through the core's loopback proxy.
// Through the proxy, each connection is trusted only once the core (root) accepted it
// (coreproxy), and only https is sent: plain http could be changed on the way.
func DefaultHTTP(proxy string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	// Each caller makes its own client for one job (an API call, a sync, a health check):
	// reuse within it, but leave no idle connections behind for the default 90 s.
	tr.IdleConnTimeout = 5 * time.Second
	if proxy != "" {
		u, err := url.Parse(proxy)
		tr.Proxy = func(r *http.Request) (*url.URL, error) {
			if err != nil {
				return nil, err
			}
			if r.URL.Scheme != "https" {
				return nil, ErrPlainOverProxy
			}
			return u, nil
		}
		tr.DialContext = coreproxy.Dial
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func encodeQuery(q map[string]string) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	v := url.Values{}
	for _, k := range keys {
		v.Set(k, q[k])
	}
	return "?" + v.Encode()
}

// Request calls path on the current API origin.
func (c *Client) Request(ctx context.Context, method, path string, o Options) Result {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, min(timeout, 60*time.Second))
	defer cancel()
	var body []byte
	if o.Body != nil {
		var err error
		if body, err = json.Marshal(o.Body); err != nil {
			return Result{Error: "invalid_body"}
		}
	}
	jwt := ""
	if !o.NoAuth {
		if s := c.Sessions.Load(); s != nil {
			jwt = s.JWT
		}
	}
	key := ""
	if ValidIdempotencyKey(o.IdempotencyKey) {
		key = o.IdempotencyKey
	}
	replayable := method == http.MethodGet || method == http.MethodHead ||
		(method == http.MethodPost && strings.HasPrefix(path, "/v2/orders/") && key != "")
	full := path + encodeQuery(o.Query)
	res, err := c.Resolver.do(ctx, c.client(o.Proxy), func(base string) (*http.Request, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+full, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Login-Type", "device")
		req.Header.Set("X-Device-Platform", "Linux")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if jwt != "" {
			req.Header.Set("Authorization", "Bearer "+jwt)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		return req, nil
	}, replayable)
	if err != nil {
		e := errUnavailable.Error()
		if ctx.Err() != nil {
			e = errTimeout.Error()
		}
		return Result{Error: e}
	}
	raw, _ := io.ReadAll(res.Body)
	ok2xx := res.StatusCode >= 200 && res.StatusCode < 300
	if o.Raw {
		return Result{OK: ok2xx, Status: res.StatusCode, Body: raw}
	}
	var payload any
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &payload) != nil {
		return Result{Status: res.StatusCode, Error: "invalid_json"}
	}
	r := Result{Status: res.StatusCode, OK: ok2xx}
	if m, isObj := payload.(map[string]any); isObj {
		code, hasCode := m["code"]
		msg, hasMsg := m["msg"]
		if hasCode && hasMsg {
			if f, ok := code.(float64); ok {
				n := int(f)
				r.Code = &n
				r.OK = ok2xx && (n == 200 || n == 0)
			} else {
				r.OK = false
			}
			if s, ok := msg.(string); ok {
				r.Msg = &s
			}
			r.Data, _ = json.Marshal(m["data"])
			return r
		}
	}
	r.Data = json.RawMessage(bytes.TrimSpace(raw))
	if len(r.Data) == 0 {
		r.Data = json.RawMessage("null")
	}
	return r
}

func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Login signs in by email with a password or verification code (the code doubles as the
// password; unknown emails are registered on the fly) and stores the session.
func (c *Client) Login(ctx context.Context, email, password, code, cfToken, invite string) Result {
	id := ""
	if c.DeviceID != nil {
		id = c.DeviceID()
	}
	if id == "" {
		id = email
	}
	pw := password
	if pw == "" {
		pw = code
	}
	res := c.Request(ctx, http.MethodPost, "/v1/auth/login", Options{NoAuth: true, Body: map[string]any{
		"identifier": id, "email": email, "code": nonEmpty(code), "password": nonEmpty(pw),
		"cf_token": nonEmpty(cfToken), "invite": nonEmpty(invite)}})
	return c.store(res, email, "login_failed")
}

// Register creates an account and stores the session.
func (c *Client) Register(ctx context.Context, email, password, code, invite, cfToken string) Result {
	id := ""
	if c.DeviceID != nil {
		id = c.DeviceID()
	}
	res := c.Request(ctx, http.MethodPost, "/v1/auth/register", Options{NoAuth: true, Body: map[string]any{
		"email": email, "password": password, "code": nonEmpty(code), "invite": nonEmpty(invite),
		"cf_token": nonEmpty(cfToken), "identifier": id}})
	return c.store(res, email, "register_failed")
}

func (c *Client) store(res Result, email, fail string) Result {
	var data struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(res.Data, &data)
	if !res.OK || data.Token == "" {
		if res.Error == "" {
			res.Error = fail
		}
		res.OK = false
		return res
	}
	if err := c.Sessions.Save(&Session{JWT: data.Token, Email: email, LoginAt: time.Now().Unix()}); err != nil {
		return Result{Error: "session_write_failed"}
	}
	return Result{OK: true, Status: res.Status}
}

// RefreshSubscriptionToken picks the account's subscription (an active one first) and
// keeps its delivery token in the session. Only device sessions receive a token.
func (c *Client) RefreshSubscriptionToken(ctx context.Context, proxy string) (Result, string) {
	sess := c.Sessions.Load()
	if sess == nil {
		return Result{Error: "not_signed_in"}, ""
	}
	id := sess.Identity()
	res := c.Request(ctx, http.MethodGet, "/v1/public/user/subscribe", Options{Proxy: proxy})
	if !res.OK {
		if res.Error == "" {
			res.Error = "subscribe_list_failed"
		}
		return res, ""
	}
	var data struct {
		List []struct {
			ID         json.RawMessage `json:"id"`
			Token      string          `json:"token"`
			Status     int             `json:"status"`
			ExpireTime json.RawMessage `json:"expire_time"`
		} `json:"list"`
	}
	_ = json.Unmarshal(res.Data, &data)
	pick := -1
	for i, s := range data.List {
		if s.Token == "" {
			continue
		}
		if s.Status == 1 {
			pick = i
			break
		}
		if pick < 0 {
			pick = i
		}
	}
	if pick < 0 {
		return Result{Status: 200, Error: "no_subscription"}, ""
	}
	p := data.List[pick]
	if err := c.Sessions.Update(id, func(s *Session) {
		s.SubToken, s.UserSubscribeID, s.ExpireTime = p.Token, p.ID, p.ExpireTime
	}); err != nil {
		return Result{Status: 409, Error: ErrSessionChanged.Error()}, ""
	}
	return Result{OK: true, Status: 200}, p.Token
}
