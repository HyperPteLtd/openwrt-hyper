// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package cnlist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const packaged = "../../luci-app-hyper/root/etc/hyper/rule-set"

func bundled(t *testing.T, list string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(packaged, list+".mrs"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheck(t *testing.T) {
	domains, addresses := bundled(t, Domains), bundled(t, Addresses)
	if err := Check(Domains, domains); err != nil {
		t.Fatal("packaged domains:", err)
	}
	if err := Check(Addresses, addresses); err != nil {
		t.Fatal("packaged addresses:", err)
	}
	for name, c := range map[string]struct {
		list string
		data []byte
	}{
		"swapped":      {Domains, addresses},
		"swapped back": {Addresses, domains},
		"truncated":    {Domains, domains[:len(domains)/2]},
		"error page":   {Addresses, []byte(strings.Repeat("<html>rate limited</html>", 1000))},
		"unknown list": {"geosite-us", domains},
	} {
		if Check(c.list, c.data) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func lists(t *testing.T) Lists {
	t.Helper()
	l := Lists{Dir: filepath.Join(t.TempDir(), "cnlists"), Packaged: t.TempDir()}
	for _, list := range Names {
		os.WriteFile(filepath.Join(l.Packaged, list+".mrs"), bundled(t, list), 0o644)
		old := time.Now().Add(-30 * 24 * time.Hour)
		os.Chtimes(filepath.Join(l.Packaged, list+".mrs"), old, old)
	}
	return l
}

func TestUpdateAndPath(t *testing.T) {
	l := lists(t)
	if l.Path(Domains) != filepath.Join(l.Packaged, "geosite-cn.mrs") || !l.Updated().IsZero() {
		t.Fatal("no download yet: the packaged copy is in use")
	}
	var direct, viaProxy, broken bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := strings.TrimPrefix(r.URL.Path, "/")
		if r.Header.Get("If-None-Match") == list {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if broken {
			w.Write([]byte("not a list"))
			return
		}
		w.Header().Set("ETag", list)
		w.Write(bundled(t, list))
	}))
	t.Cleanup(srv.Close)
	u := &Updater{Lists: l, URLs: map[string]string{Domains: srv.URL + "/geosite-cn", Addresses: srv.URL + "/geoip-cn"},
		Proxy: func() string { return "socks5://core" },
		HTTP: func(proxy string) *http.Client {
			if proxy == "" {
				direct = true
				return &http.Client{Transport: failing{}} // GitHub unreachable straight out
			}
			viaProxy = true
			return srv.Client()
		}}
	// The same bytes as the packaged copy: nothing written.
	changed, err := u.Update(context.Background())
	if err != nil || len(changed) != 0 || !direct || !viaProxy {
		t.Fatal(changed, err, direct, viaProxy)
	}
	if _, err := os.Stat(l.Dir); err == nil {
		t.Fatal("an unchanged list was written")
	}
	// A newer list: written, and then preferred over the packaged copy.
	u.etags = nil
	os.WriteFile(filepath.Join(l.Packaged, "geoip-cn.mrs"), bundled(t, Domains), 0o644) // packaged copy differs now
	os.Chtimes(filepath.Join(l.Packaged, "geoip-cn.mrs"), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	if changed, err = u.Update(context.Background()); err != nil || len(changed) != 1 || changed[0] != Addresses {
		t.Fatal(changed, err)
	}
	if l.Path(Addresses) != filepath.Join(l.Dir, "geoip-cn.mrs") || l.Path(Domains) != filepath.Join(l.Packaged, "geosite-cn.mrs") {
		t.Fatal(l.Path(Addresses), l.Path(Domains))
	}
	// Unchanged upstream: a 304, nothing written.
	if changed, err = u.Update(context.Background()); err != nil || len(changed) != 0 {
		t.Fatal(changed, err)
	}
	// A bad download never replaces the list in use.
	u.etags, broken = nil, true
	if _, err = u.Update(context.Background()); !errors.Is(err, errInvalid) {
		t.Fatal(err)
	}
	if l.Path(Addresses) != filepath.Join(l.Dir, "geoip-cn.mrs") {
		t.Fatal("download lost")
	}
	// A plugin update shipping a newer packaged copy wins over an older download.
	os.Chtimes(filepath.Join(l.Packaged, "geoip-cn.mrs"), time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if l.Path(Addresses) != filepath.Join(l.Packaged, "geoip-cn.mrs") {
		t.Fatal("newer packaged copy ignored")
	}
	// A corrupted download is not used.
	os.Chtimes(filepath.Join(l.Packaged, "geoip-cn.mrs"), time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	os.WriteFile(filepath.Join(l.Dir, "geoip-cn.mrs"), []byte("corrupt"), 0o644)
	if l.Path(Addresses) != filepath.Join(l.Packaged, "geoip-cn.mrs") {
		t.Fatal("corrupt download used")
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unreachable")
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		updated, checked, attempt time.Time
		failures                  int
		want                      bool
	}{
		"packaged copies":            {want: true},
		"downloaded six days ago":    {updated: now.Add(-6 * 24 * time.Hour)},
		"downloaded eight days ago":  {updated: now.Add(-8 * 24 * time.Hour), want: true},
		"checked yesterday":          {updated: now.Add(-30 * 24 * time.Hour), checked: now.Add(-24 * time.Hour)},
		"dated in the future":        {updated: now.Add(48 * time.Hour), want: true},
		"failed half an hour ago":    {attempt: now.Add(-30 * time.Minute), failures: 1},
		"failed two hours ago":       {attempt: now.Add(-2 * time.Hour), failures: 1, want: true},
		"third failure, three hours": {attempt: now.Add(-3 * time.Hour), failures: 3},
		"tenth failure, a day":       {attempt: now.Add(-25 * time.Hour), failures: 10, want: true},
	} {
		if got := Due(now, c.updated, c.checked, c.attempt, c.failures); got != c.want {
			t.Errorf("%s: due %v", name, got)
		}
	}
}
