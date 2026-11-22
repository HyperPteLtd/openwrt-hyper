// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/core"
	"github.com/HyperPteLtd/openwrt-hyper/internal/update"
)

// methodsJSON is the luci.hyper signature table rpcd reads with `hyperd list`.
// It is the luci.hyper contract the web UI and the rpcd ACL rely on.
//
//go:embed methods.json
var methodsJSON []byte

var methodArgs = func() map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(methodsJSON, &m); err != nil {
		panic(err)
	}
	return m
}()

// Paths are the manager's files; tests point them into a temp dir.
type Paths struct {
	RunDir  string // tmpfs state
	Socket  string // manager API (root only)
	Lock    string
	Pulse   string // touched by serve while healthy; guard watches it
	Owned   string // exists while hyper's firewall/DNS takeover is installed
	Network string // helper that installs / releases the takeover
	Secret  string // controller secret written by serve for guard
	Control string // controller address

	UCI     string // /etc/config/hyper
	Etc     string // /etc/hyper: nodes, rules, lists, session, check-in
	CoreDir string // mihomo -d: config, rule files, device lists
	CoreBin string
	Log     string // hyperd's own log
	CoreLog string
	Leases  string // dnsmasq leases
	Update  string // software update state
	Plan    string // settings for the network helper (shell-sourceable)
	Self    string // this binary, for the detached installer
}

// DefaultPaths are the installed locations. HYPERD_ROOT prefixes all of them (the
// end-to-end test runs a whole router layout in a temp dir).
func DefaultPaths() Paths {
	root := os.Getenv("HYPERD_ROOT")
	run := root + "/var/run/hyper"
	return Paths{RunDir: run, Socket: run + "/hyperd.sock", Lock: run + "/hyperd.lock", Pulse: run + "/pulse",
		Owned: run + "/network/owned", Network: root + "/usr/libexec/hyper/network",
		Secret: run + "/control.secret", Control: "127.0.0.1:5334",
		UCI: root + "/etc/config/hyper", Etc: root + "/etc/hyper", CoreDir: run + "/core", CoreBin: root + core.DefaultBin,
		Log: run + "/hyper.log", CoreLog: run + "/core.log", Leases: root + "/tmp/dhcp.leases",
		Update: root + update.DefaultDir, Plan: run + "/network/plan.env", Self: root + "/usr/libexec/hyper/bin/hyperd"}
}

func (p Paths) nodes() string   { return filepath.Join(p.Etc, "nodes.json") }
func (p Paths) rules() string   { return filepath.Join(p.Etc, "rules") }
func (p Paths) session() string { return filepath.Join(p.Etc, "session") }
func (p Paths) checkin() string { return filepath.Join(p.Etc, "checkin.json") }

// stopping is the init script's marker for a service stop in progress (see stopping()).
func (p Paths) stopping() string { return filepath.Join(p.RunDir, "stopping") }
func (p Paths) list(kind string) string {
	return filepath.Join(p.Etc, "resources", kind+".txt")
}

// Handler answers one method; args are the call's JSON object (session removed).
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

// rpcError is returned to the UI as {"error": code}; codes are stable identifiers.
type rpcError struct{ Code string }

func (e rpcError) Error() string { return e.Code }

// codedError is a failure the UI reads by its code (errors.As finds it as an rpcError)
// while hyperd's log keeps the cause.
type codedError struct {
	code string
	err  error
}

func coded(code string, err error) error { return codedError{code, err} }

func (e codedError) Error() string { return e.err.Error() }
func (e codedError) Unwrap() error { return e.err }
func (e codedError) As(target any) bool {
	if p, ok := target.(*rpcError); ok {
		*p = rpcError{e.code}
		return true
	}
	return false
}

type manager struct {
	paths    Paths
	handlers map[string]Handler
	sys      *system
	log      *logger

	uciMu     sync.Mutex
	nodeCache nodeCache
	sessions  *account.Sessions
	client    *account.Client
	svc       *service
	tasks     *tasks
	syncer    *syncJob
	probes    *probes
	devices   *deviceLists
	cn        *cnLists
	ctx       context.Context // serve's lifetime; background work stops with it
}

func newManagerWith(ctx context.Context, paths Paths, sys *system) *manager {
	m := &manager{paths: paths, handlers: map[string]Handler{}, sys: sys, ctx: ctx}
	m.log = &logger{path: paths.Log, now: sys.Now}
	m.sessions = &account.Sessions{Path: paths.session()}
	m.client = &account.Client{
		Sessions: m.sessions,
		Resolver: &account.Resolver{CachePath: filepath.Join(paths.RunDir, "api-endpoints.json")},
		DeviceID: func() string { c, _ := m.config(); return c.Get("account", "device_id") },
		HTTP:     sys.HTTP,
	}
	m.svc = newService(m)
	m.tasks = newTasks(m)
	m.syncer = &syncJob{m: m}
	m.probes = &probes{m: m}
	m.devices = &deviceLists{m: m}
	m.cn = &cnLists{m: m}
	m.register()
	return m
}

// register wires the luci.hyper methods; TestEveryMethodHasAHandler keeps it in step
// with methods.json (an unknown method is refused before it reaches a handler).
func (m *manager) register() {
	m.registerService()
	m.registerNodes()
	m.registerAccount()
	m.registerSettings()
	m.registerDiag()
	m.registerUpdate()
	m.tasks.wrap("api", "diag", "health_check", "check_update", "install_update")
}

func (m *manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	h, ok := m.handlers[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.Error(w, "unknown method", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 12<<20))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) || body[0] != '{' {
		http.Error(w, "expected a JSON object", http.StatusBadRequest)
		return
	}
	out, err := h(r.Context(), body)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		code := "internal_error"
		var re rpcError
		if errors.As(err, &re) {
			code = re.Code
		}
		out = map[string]string{"error": code}
	}
	if out == nil {
		out = map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func serve(ctx context.Context, p Paths) error {
	// A small, long-lived heap on routers with 128 MB or less: collect sooner and keep a soft
	// ceiling (it only makes the collector work harder, never fails an allocation).
	// GOGC / GOMEMLIMIT in the environment still win.
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(50)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(16 << 20)
	}
	if err := os.MkdirAll(p.RunDir, 0o711); err != nil {
		return err
	}
	lock, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("manager already running")
	}
	p.sweepTemps()
	if err = os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	l, err := net.Listen("unix", p.Socket)
	if err != nil {
		return err
	}
	if err = os.Chmod(p.Socket, 0o600); err != nil {
		l.Close()
		return err
	}
	m := newManagerWith(ctx, p, defaultSystem())
	srv := &http.Server{Handler: m, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second}
	go pulse(ctx, p.Pulse)
	go m.run(ctx)
	go func() { <-ctx.Done(); _ = srv.Close() }()
	err = srv.Serve(l)
	m.shutdown()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// sweepTemps removes what writes killed before their rename (a stop that ran out of time, a
// power cut) left on flash: only serve writes there, and it has not
// started yet. /etc/config is shared, so only the hyper config's own are removed there.
func (p Paths) sweepTemps() {
	for _, dir := range []string{p.Etc, p.rules(), filepath.Dir(p.list("direct_list"))} {
		atomicfile.Sweep(dir)
	}
	atomicfile.Sweep(filepath.Dir(p.UCI), filepath.Base(p.UCI))
}

// pulse touches the pulse file every second so guard can tell serve is alive.
func pulse(ctx context.Context, path string) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		now := time.Now()
		if err := os.Chtimes(path, now, now); os.IsNotExist(err) {
			_ = os.WriteFile(path, nil, 0o600)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// rpcdCall forwards one rpcd exec call to serve. rpcd has already authorised the
// method; its session token is dropped so it never reaches the manager or its logs.
func rpcdCall(ctx context.Context, socket, method string, stdin io.Reader) ([]byte, error) {
	if _, ok := methodArgs[method]; !ok {
		return nil, errors.New("unknown method " + method)
	}
	return socketCall(ctx, socket, method, stdin)
}

// socketCall posts one call to serve (rpcdCall checks the method table first).
func socketCall(ctx context.Context, socket, method string, stdin io.Reader) ([]byte, error) {
	in, err := io.ReadAll(io.LimitReader(stdin, 12<<20+1))
	if err != nil {
		return nil, err
	}
	if len(in) > 12<<20 {
		return nil, errors.New("request too large")
	}
	if len(bytes.TrimSpace(in)) > 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(in, &fields) != nil || fields == nil {
			return nil, errors.New("expected a JSON object")
		}
		delete(fields, "ubus_rpc_session")
		if in, err = json.Marshal(fields); err != nil {
			return nil, err
		}
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://hyperd/"+method, bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		// Same shape the UI already handles when the service is down.
		return []byte(`{"error":"service_unavailable"}`), nil
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(strings.TrimSpace(string(out)))
	}
	return out, nil
}
