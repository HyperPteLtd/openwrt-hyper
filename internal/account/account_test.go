// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type api struct {
	*httptest.Server
	logins, subscribes, configs atomic.Int32
	down                        atomic.Bool
}

func newAPI(t *testing.T) *api {
	a := &api{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.down.Load() {
			w.WriteHeader(502)
			return
		}
		if r.Header.Get("Login-Type") != "device" && r.URL.Path != "/v1/common/site/config" {
			t.Errorf("missing device header on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/v1/common/site/config":
			a.configs.Add(1)
			w.Write([]byte(`{"code":200,"msg":"ok","data":{}}`))
		case "/v1/auth/login":
			a.logins.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["identifier"] != "dev-1" || body["password"] != "123456" || body["code"] != "123456" {
				w.Write([]byte(`{"code":401,"msg":"bad","data":null}`))
				return
			}
			w.Write([]byte(`{"code":200,"msg":"ok","data":{"token":"jwt-1"}}`))
		case "/v1/public/user/subscribe":
			a.subscribes.Add(1)
			if r.Header.Get("Authorization") != "Bearer jwt-1" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"code":200,"msg":"","data":{"list":[{"id":1,"token":"","status":1},{"id":2,"token":"old","status":0},{"id":3,"token":"tok","status":1,"expire_time":99}]}}`))
		case "/v2/subscribe/locations":
			w.Write([]byte(`{"locations":{"JP":{"sort":1}}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(a.Close)
	return a
}

func newClient(t *testing.T, bases ...string) *Client {
	dir := t.TempDir()
	return &Client{
		Sessions: &Sessions{Path: filepath.Join(dir, "session")},
		Resolver: &Resolver{CachePath: filepath.Join(dir, "endpoints.json"), Builtin: bases, DoH: []string{"http://127.0.0.1:1/none"}},
		DeviceID: func() string { return "dev-1" },
	}
}

func TestLoginAndToken(t *testing.T) {
	a := newAPI(t)
	c := newClient(t, a.URL)
	ctx := context.Background()
	if r := c.Login(ctx, "a@b.c", "", "000000", "", ""); r.OK || r.Code == nil || *r.Code != 401 || c.Sessions.Load() != nil {
		t.Fatalf("%+v", r)
	}
	if r := c.Login(ctx, "a@b.c", "", "123456", "", ""); !r.OK {
		t.Fatalf("%+v", r)
	}
	s := c.Sessions.Load()
	if s == nil || s.JWT != "jwt-1" || s.Email != "a@b.c" {
		t.Fatal(s)
	}
	if fi, _ := os.Stat(c.Sessions.Path); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	r, tok := c.RefreshSubscriptionToken(ctx, "")
	if !r.OK || tok != "tok" || c.Sessions.Load().SubToken != "tok" || string(c.Sessions.Load().ExpireTime) != "99" {
		t.Fatalf("%+v %q", r, tok)
	}
	// The same token again leaves the file (flash) unwritten.
	before, _ := os.Stat(c.Sessions.Path)
	if r, tok = c.RefreshSubscriptionToken(ctx, ""); !r.OK || tok != "tok" {
		t.Fatalf("%+v %q", r, tok)
	}
	if after, _ := os.Stat(c.Sessions.Path); !os.SameFile(before, after) {
		t.Fatal("an unchanged session was written again")
	}
	// Unwrapped replies keep their body as data.
	if r := c.Request(ctx, "GET", "/v2/subscribe/locations", Options{}); !r.OK || r.Code != nil || !strings.Contains(string(r.Data), "JP") {
		t.Fatalf("%+v", r)
	}
	if err := c.Sessions.Update("other", func(*Session) {}); !errors.Is(err, ErrSessionChanged) {
		t.Fatal(err)
	}
	c.Sessions.Clear()
	if c.Sessions.Load() != nil {
		t.Fatal("not cleared")
	}
}

func TestFailover(t *testing.T) {
	bad, good := newAPI(t), newAPI(t)
	bad.down.Store(true)
	c := newClient(t, bad.URL, good.URL)
	ctx := context.Background()
	// A read is replayed on the next origin.
	if r := c.Request(ctx, "GET", "/v2/subscribe/locations", Options{}); !r.OK {
		t.Fatalf("%+v", r)
	}
	if c.Resolver.Current() != good.URL {
		t.Fatal(c.Resolver.Current())
	}
	// A login probes origins first and is sent exactly once.
	c2 := newClient(t, bad.URL, good.URL)
	if r := c2.Login(ctx, "a@b.c", "", "123456", "", ""); !r.OK {
		t.Fatalf("%+v", r)
	}
	if bad.logins.Load() != 0 || good.logins.Load() != 1 {
		t.Fatal(bad.logins.Load(), good.logins.Load())
	}
	// The healthy origin is remembered for later processes.
	c3 := &Client{Sessions: c.Sessions, Resolver: &Resolver{CachePath: c.Resolver.CachePath, Builtin: []string{bad.URL, good.URL}}}
	if c3.Resolver.Current() != good.URL {
		t.Fatal(c3.Resolver.Current())
	}
	good.down.Store(true)
	if r := c2.Login(ctx, "a@b.c", "", "123456", "", ""); r.OK || r.Error != "api_unavailable" {
		t.Fatalf("%+v", r)
	}
}

func TestTXTDiscovery(t *testing.T) {
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != TXTHost {
			t.Error(r.URL)
		}
		w.Write([]byte(`{"Status":0,"Answer":[{"type":16,"data":"\"rec\" \"ord\""},{"type":1,"data":"1.2.3.4"}]}`))
	}))
	defer doh.Close()
	r := &Resolver{CachePath: filepath.Join(t.TempDir(), "c.json"), DoH: []string{doh.URL},
		DecryptTXT: func(s string) ([]byte, error) {
			if s != "record" {
				return nil, errors.New("bad")
			}
			return []byte("v=1|API-One.example.com|https://two.example.com/,bad_host|nodot"), nil
		}}
	r.mu.Lock()
	r.refresh(context.Background(), http.DefaultClient, false)
	got := r.load().Fallbacks
	r.mu.Unlock()
	if len(got) != 2 || got[0] != "https://api-one.example.com" || got[1] != "https://two.example.com" {
		t.Fatal(got)
	}
}

func TestAllowedPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/v1/public/user/info":                true,
		"/v1/public/announcement/list":        true,
		"/v1/public/user/device/kick_offline": true,
		// Purchases are the website's.
		"/v2/orders/abc_1/payment":                          false,
		"/v1/public/order/detail":                           false,
		"/v2/orders/purchase":                               false,
		"/v1/public/user/info?x=1":                          false,
		"/v1/public/order/../../admin":                      false,
		"/v1/admin/users":                                   false,
		"/v1/public/user/info ":                             false,
		"/v2/orders/" + strings.Repeat("a", 65) + "/cancel": false,
	} {
		if AllowedPath(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
	if !ValidIdempotencyKey("order-12345") || ValidIdempotencyKey("short") {
		t.Fatal("idempotency key")
	}
}

// Through the core's proxy only https goes, and only to a listener the core accepted on.
func TestDefaultHTTPThroughProxy(t *testing.T) {
	c := DefaultHTTP("socks5://127.0.0.1:1")
	if _, err := c.Get("http://example.com/list.txt"); !errors.Is(err, ErrPlainOverProxy) {
		t.Fatalf("plain http over the proxy: %v", err)
	}
	if _, err := c.Get("https://example.com/"); err == nil || errors.Is(err, ErrPlainOverProxy) {
		t.Fatalf("https over a port nothing holds: %v", err)
	}
	// Per-job clients must not keep idle connections (and their goroutines) around.
	for _, p := range []string{"", "socks5://127.0.0.1:1"} {
		if tr := DefaultHTTP(p).Transport.(*http.Transport); tr.IdleConnTimeout <= 0 || tr.IdleConnTimeout > 10*time.Second {
			t.Errorf("proxy %q: idle timeout %v", p, tr.IdleConnTimeout)
		}
	}
}

// An update that read the session before a sign-out never writes it back.
func TestUpdateKeepsSignOut(t *testing.T) {
	s := &Sessions{Path: filepath.Join(t.TempDir(), "session")}
	if err := s.Save(&Session{JWT: "a"}); err != nil {
		t.Fatal(err)
	}
	id := s.Load().Identity()
	err := s.Update(id, func(sess *Session) {
		// Waits for the lock Update holds, as a sign-out arriving meanwhile would.
		done := make(chan struct{})
		go func() { s.Clear(); close(done) }()
		select {
		case <-done:
			t.Error("sign-out ran in the middle of an update")
		case <-time.After(50 * time.Millisecond):
		}
		sess.SubToken = "t"
	})
	if err != nil {
		t.Fatal(err)
	}
	// The sign-out goes through after the update, and stays.
	deadline := time.Now().Add(time.Second)
	for s.Load() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Load() != nil {
		t.Fatal("the sign-out was lost")
	}
}
