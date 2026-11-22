// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/update"
)

func testPaths(t *testing.T) Paths {
	// unix socket paths are limited to ~100 bytes; t.TempDir() can exceed that
	dir, err := os.MkdirTemp("", "hyperd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return Paths{RunDir: dir, Socket: dir + "/s", Lock: dir + "/lock", Pulse: dir + "/pulse", Owned: dir + "/owned",
		Network: dir + "/network", Secret: dir + "/secret", Control: "127.0.0.1:1",
		UCI: dir + "/uci-hyper", Etc: dir + "/etc", CoreDir: dir + "/core", CoreBin: dir + "/mihomo",
		Log: dir + "/hyper.log", CoreLog: dir + "/core.log", Leases: dir + "/leases", Update: dir + "/update",
		Plan: dir + "/plan.env", Self: dir + "/hyperd"}
}

// The method table is the luci.hyper contract the web UI and ACL rely on.
func TestMethodTable(t *testing.T) {
	want := strings.Fields(`acllist_read acllist_write api check_update checkin checkin_status diag get_config
		health_check install_update lan_clients latency latency_cancel latency_status log_clean login logout logs
		nodes register restart rules_list select_node send_code set_access_control set_config set_enabled set_mode
		set_rule_enabled status sync_status sync_subscription task_status traffic`)
	if len(methodArgs) != len(want) {
		t.Fatalf("%d methods, want %d", len(methodArgs), len(want))
	}
	for _, m := range want {
		if _, ok := methodArgs[m]; !ok {
			t.Errorf("missing method %s", m)
		}
	}
	// Every ACL-granted method must exist.
	acl, err := os.ReadFile("../../luci-app-hyper/root/usr/share/rpcd/acl.d/luci-app-hyper.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]struct {
		Read, Write struct {
			Ubus map[string][]string `json:"ubus"`
		}
	}
	if err = json.Unmarshal(acl, &doc); err != nil {
		t.Fatal(err)
	}
	for _, perm := range doc {
		for _, m := range append(perm.Read.Ubus["luci.hyper"], perm.Write.Ubus["luci.hyper"]...) {
			if _, ok := methodArgs[m]; !ok {
				t.Errorf("ACL grants unknown method %s", m)
			}
		}
	}
}

func TestServeAndCall(t *testing.T) {
	p := testPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, p) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(p.Socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, _ := os.Stat(p.Socket); st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", st.Mode().Perm())
	}
	if err := serve(context.Background(), p); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second serve: %v", err)
	}

	out, err := rpcdCall(ctx, p.Socket, "status", strings.NewReader(`{"ubus_rpc_session":"secret-token"}`))
	if err != nil || !strings.Contains(string(out), `"version"`) {
		t.Fatalf("status: %s %v", out, err)
	}
	out, err = rpcdCall(ctx, p.Socket, "nodes", strings.NewReader(``))
	if err != nil || strings.TrimSpace(string(out)) != `{"main_node":null,"nodes":[]}` {
		t.Fatalf("nodes: %s %v", out, err)
	}
	if _, err = rpcdCall(ctx, p.Socket, "rm_rf", strings.NewReader(`{}`)); err == nil {
		t.Fatal("unknown method must be refused before reaching serve")
	}
	if _, err = rpcdCall(ctx, p.Socket, "status", strings.NewReader(`[1]`)); err == nil {
		t.Fatal("non-object input must be refused")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	out, err = rpcdCall(context.Background(), p.Socket, "status", strings.NewReader(`{}`))
	if err != nil || string(out) != `{"error":"service_unavailable"}` {
		t.Fatalf("stopped: %s %v", out, err)
	}
}

func TestSessionTokenNotForwarded(t *testing.T) {
	p := testPaths(t)
	var got string
	m := newManager(p)
	m.handlers["status"] = func(_ context.Context, args json.RawMessage) (any, error) { got = string(args); return nil, nil }
	srv := httptest.NewUnstartedServer(m)
	l, err := netListenUnix(p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	if _, err = rpcdCall(context.Background(), p.Socket, "status", strings.NewReader(`{"ubus_rpc_session":"tok","x":1}`)); err != nil {
		t.Fatal(err)
	}
	if got != `{"x":1}` {
		t.Fatalf("handler saw %s", got)
	}
}

func TestGuardFailsOpen(t *testing.T) {
	p := testPaths(t)
	// Live controller that accepts the secret.
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"version":"v"}`))
	}))
	defer ctrl.Close()
	p.Control = strings.TrimPrefix(ctrl.URL, "http://")
	faults := filepath.Join(p.RunDir, "faults")
	os.WriteFile(p.Network, []byte("#!/bin/sh\n[ \"$1\" = fault ] && echo x >> "+faults+"\n"), 0o755)
	os.WriteFile(p.Secret, []byte("k\n"), 0o600)
	os.WriteFile(p.Owned, nil, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pulse(ctx, p.Pulse)
	gctx, gcancel := context.WithCancel(context.Background())
	go guard(gctx, p, guardTiming{Tick: 30 * time.Millisecond, Misses: 3, Probe: 200 * time.Millisecond, Stale: 3 * time.Second})

	time.Sleep(1500 * time.Millisecond) // healthy across a pulse interval, controller answers
	if _, err := os.Stat(faults); err == nil {
		gcancel()
		t.Fatal("fault raised while healthy")
	}
	ctrl.Close() // core dies
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(faults); err == nil {
			break
		}
		if time.Now().After(deadline) {
			gcancel()
			t.Fatal("guard did not release the network after the core died")
		}
		time.Sleep(20 * time.Millisecond)
	}
	gcancel()
}

func TestGuardStalePulse(t *testing.T) {
	p := testPaths(t)
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"v"}`)) }))
	defer ctrl.Close()
	p.Control = strings.TrimPrefix(ctrl.URL, "http://")
	faults := filepath.Join(p.RunDir, "faults")
	os.WriteFile(p.Network, []byte("#!/bin/sh\necho x >> "+faults+"\n"), 0o755)
	os.WriteFile(p.Secret, []byte("k"), 0o600)
	os.WriteFile(p.Owned, nil, 0o600)
	old := time.Now().Add(-time.Minute)
	os.WriteFile(p.Pulse, nil, 0o600)
	os.Chtimes(p.Pulse, old, old) // serve hung: core still answers, pulse frozen
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go guard(ctx, p, guardTiming{Tick: 20 * time.Millisecond, Misses: 3, Stale: time.Second})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(faults); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("stale pulse did not trigger fault")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGuardIdleWithoutTakeover(t *testing.T) {
	p := testPaths(t)
	faults := filepath.Join(p.RunDir, "faults")
	os.WriteFile(p.Network, []byte("#!/bin/sh\necho x >> "+faults+"\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	guard(ctx, p, guardTiming{Tick: 10 * time.Millisecond, Misses: 1})
	if _, err := os.Stat(faults); err == nil {
		t.Fatal("guard acted while hyper owns no network state")
	}
}

// guard probes the controller address serve configured, not the built-in default.
func TestGuardUsesConfiguredController(t *testing.T) {
	p := testPaths(t)
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"v"}`)) }))
	defer ctrl.Close()
	p.Control = "127.0.0.1:1" // nothing listens on the default
	os.WriteFile(p.Secret, []byte("k\n"), 0o600)
	os.WriteFile(p.Secret+".addr", []byte(strings.TrimPrefix(ctrl.URL, "http://")+"\n"), 0o600)
	os.WriteFile(p.Pulse, nil, 0o600)
	if !healthy(context.Background(), p, guardTiming{Probe: time.Second, Stale: time.Minute}) {
		t.Fatal("guard ignored the configured controller address")
	}
	os.Remove(p.Secret + ".addr")
	if healthy(context.Background(), p, guardTiming{Probe: 200 * time.Millisecond, Stale: time.Minute}) {
		t.Fatal("default address answered")
	}
}

func newManager(paths Paths) *manager {
	return newManagerWith(context.Background(), paths, defaultSystem())
}

// A core directory an earlier build left under another account is rebuilt; our own is kept.
func TestReclaimDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "core")
	me := os.Geteuid()
	if err := reclaimDir(dir, me); err != nil {
		t.Fatal("a missing directory is nothing to reclaim:", err)
	}
	os.MkdirAll(filepath.Join(dir, "devices"), 0o700)
	cache := filepath.Join(dir, "cache.db")
	os.WriteFile(cache, []byte("x"), 0o600)
	if err := reclaimDir(dir, me); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("our own state was removed:", err)
	}
	// Owned by someone else as far as reclaimDir can tell.
	if err := reclaimDir(dir, me+1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a directory another account owns was kept")
	}
}

// Only MIPS builds keep the core's TLS off the post-quantum key share.
func TestCoreEnv(t *testing.T) {
	env := coreEnv()
	if mips := runtime.GOARCH == "mips" || runtime.GOARCH == "mipsle"; mips != (len(env) == 1 && env[0] == "GODEBUG=tlsmlkem=0") {
		t.Fatalf("%s: %v", runtime.GOARCH, env)
	}
}

// What killed writes left on flash goes at the next serve start; other files stay, and so
// do other packages' files in /etc/config.
func TestSweepTemps(t *testing.T) {
	p := testPaths(t)
	os.MkdirAll(p.rules(), 0o755)
	os.MkdirAll(filepath.Dir(p.list("direct_list")), 0o755)
	left := []string{filepath.Join(p.Etc, ".nodes.json-1234"), filepath.Join(p.rules(), ".index.json-99"),
		filepath.Join(filepath.Dir(p.list("direct_list")), ".direct_list.txt-7"), filepath.Join(filepath.Dir(p.UCI), "."+filepath.Base(p.UCI)+"-5")}
	kept := []string{filepath.Join(p.Etc, "nodes.json"), filepath.Join(filepath.Dir(p.UCI), ".firewall-5")}
	for _, f := range append(left, kept...) {
		os.WriteFile(f, nil, 0o600)
	}
	p.sweepTemps()
	for _, f := range left {
		if _, err := os.Stat(f); err == nil {
			t.Error("left behind:", f)
		}
	}
	for _, f := range kept {
		if _, err := os.Stat(f); err != nil {
			t.Error("removed:", f)
		}
	}
}

func TestUpdateCode(t *testing.T) {
	for err, want := range map[error]string{
		update.ErrRepo: "update_repo_invalid",
		fmt.Errorf("%w: no ipk package for mips_24kc", update.ErrNoPackage): "update_no_package",
		fmt.Errorf("%w mismatch for x.ipk", update.ErrChecksum):             "update_checksum_failed",
		update.StatusError(403): "update_rate_limited", update.StatusError(404): "update_release_missing",
		fmt.Errorf("x.ipk: %w", update.StatusError(502)):                                               "update_server_error",
		&url.Error{Op: "Get", URL: "https://api.github.com", Err: errors.New("dial tcp: i/o timeout")}: "update_unreachable",
		errors.New("response too large"):                                                               "update_failed",
	} {
		if got := updateCode(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}
