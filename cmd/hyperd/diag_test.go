// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The HTTP diagnostic reads as curl's -w timings did, follows no redirect, and a failure
// reads as its cause.
func TestHTTPCheck(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		t.Error("redirect followed")
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	host := strings.TrimPrefix(srv.URL, "https://")

	out, ok := httpCheck(context.Background(), client, host)
	if !ok || !regexp.MustCompile(`^HTTP 302 \| connect \d+\.\d{3}s \| tls \d+\.\d{3}s \| ttfb \d+\.\d{3}s\n$`).MatchString(out) {
		t.Fatalf("%v %q", ok, out)
	}

	srv.Close()
	out, ok = httpCheck(context.Background(), client, host)
	if ok || strings.HasPrefix(out, "Get ") || !strings.Contains(out, "refused") {
		t.Fatalf("%v %q", ok, out)
	}
}
