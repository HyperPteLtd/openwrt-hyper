// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mihomo"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
)

// env is a router in a temp dir: UCI from the golden fixture, nodes, rule lists, a fake
// core and a fake network helper.
type env struct {
	t     *testing.T
	p     Paths
	m     *manager
	sys   *system
	neigh string
	wan   string // WAN resolver netifd reports
	v6    bool   // the router has an IPv6 route to the internet
	// The firewall refuses the tproxy rule / the kernel has no TUN device. Read only by a
	// start, after the call that requested it.
	noTProxy, noTUN bool
	core            string // fake core log
	net             string // network helper log
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// fixtureUCI renders tests/fixtures/uci-basic.json (the golden recorder's UCI, nodes dropped) as UCI.
func fixtureUCI(t *testing.T, port int, edit func(map[string]map[string]any)) []byte {
	b, err := os.ReadFile("../../tests/fixtures/uci-basic.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]any
	json.Unmarshal(b, &raw)
	if h, ok := raw["hyper"]; ok && h[".type"] == nil {
		raw = map[string]map[string]any{}
		for k, v := range h {
			raw[k] = v.(map[string]any)
		}
	}
	raw["infra"]["controller_port"] = fmt.Sprint(port)
	raw["infra"]["mixed_port"] = fmt.Sprint(port + 1)
	if edit != nil {
		edit(raw)
	}
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		sec := raw[name]
		if sec[".type"] == "node" {
			continue
		}
		fmt.Fprintf(&out, "config %s '%s'\n", sec[".type"], name)
		for k, v := range sec {
			switch x := v.(type) {
			case string:
				if k != ".type" {
					fmt.Fprintf(&out, "\toption %s '%s'\n", k, x)
				}
			case []any:
				for _, e := range x {
					fmt.Fprintf(&out, "\tlist %s '%s'\n", k, e)
				}
			}
		}
		out.WriteString("\n")
	}
	return []byte(out.String())
}

// The golden fixture's nodes (ids, labels, countries), as the AnyTLS proxies the service sends.
func fixtureNodes() *nodes.Store {
	anytls := func(server string, port int64) map[string]any {
		return map[string]any{"type": "anytls", "server": server, "port": port, "password": "x", "sni": server, "udp": true}
	}
	return &nodes.Store{Nodes: []nodes.Node{
		{ID: "pp_tokyo", Label: "日本 · 东京 01", Country: "JP", Sort: 10, Proxy: anytls("jp1.example.com", 443)},
		{ID: "pp_hk", Label: "中国香港 01", Country: "HK", Sort: 9, Proxy: anytls("203.0.113.7", 8443)},
		{ID: "pp_sg", Label: "新加坡 02", Country: "SG", Sort: 8, Proxy: anytls("sg2.example.com", 443)},
		{ID: "pp_anytls", Label: "日本 · 东京 AnyTLS", Country: "JP", Sort: 10, Proxy: anytls("jp9.example.com", 8443)},
	}}
}

func copyDir(t *testing.T, src, dst string) {
	os.MkdirAll(dst, 0o755)
	list, _ := os.ReadDir(src)
	for _, f := range list {
		b, _ := os.ReadFile(filepath.Join(src, f.Name()))
		os.WriteFile(filepath.Join(dst, f.Name()), b, 0o644)
	}
}

func newEnv(t *testing.T, edit func(map[string]map[string]any)) *env {
	e := &env{t: t, p: testPaths(t), wan: "192.168.1.1"}
	port := freePort(t)
	os.MkdirAll(e.p.Etc, 0o755)
	os.WriteFile(e.p.UCI, fixtureUCI(t, port, edit), 0o600)
	if err := fixtureNodes().Save(e.p.nodes()); err != nil {
		t.Fatal(err)
	}
	copyDir(t, "../../tests/fixtures/rules", e.p.rules())
	os.MkdirAll(filepath.Join(e.p.Etc, "rule-set"), 0o755)
	os.WriteFile(e.p.Leases, []byte("1700000000 00:d9:d1:8b:33:e2 192.168.5.3 phone *\n1700000000 aa:aa:aa:aa:aa:01 192.168.5.4 tv 01:aa\n"), 0o644)
	e.neigh = "192.168.5.2 dev br-lan lladdr 5a:94:ef:e4:0c:dd REACHABLE\n192.168.5.9 dev br-lan  FAILED\nfe80::1 dev br-lan lladdr 5a:94:ef:e4:0c:dd STALE\n"
	self, _ := os.Executable()
	e.core, e.net = e.p.RunDir+"/core-calls", e.p.RunDir+"/net-calls"
	os.WriteFile(e.p.CoreBin, []byte(fmt.Sprintf("#!/bin/sh\nHYPERD_FAKE_CORE=1 HYPERD_FAKE_LOG=%s exec %s \"$@\"\n", e.core, self)), 0o755)
	os.WriteFile(e.p.Network, []byte(fmt.Sprintf(`#!/bin/sh
echo "$1 $(grep METHOD %s)" >> %s
case "$1" in up) touch %s ;; down|fault) rm -f %s ;; missing-lans) cat %[5]s/missing-lans 2>/dev/null ;;
repair) [ ! -e %[5]s/repair-fails ] || exit 1; echo repaired ;; esac
`, e.p.Plan, e.net, e.p.Owned, e.p.Owned, e.p.RunDir)), 0o755)
	e.sys = defaultSystem()
	real := e.sys.Run
	exists := e.sys.Exists
	e.sys.Exists = func(p string) bool {
		if p == "/dev/net/tun" {
			return !e.noTUN
		}
		return exists(p)
	}
	e.sys.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "nft": // only the capture method's check runs nft
			if e.noTProxy {
				return []byte("Error: Could not process rule: No such file or directory"), errors.New("exit status 1")
			}
			return nil, nil
		case "ubus":
			return []byte(`{"dns-server":["` + e.wan + `"]}`), nil
		case "ip":
			if len(args) > 1 && args[0] == "-6" && args[1] == "route" {
				if !e.v6 {
					return []byte("RTNETLINK answers: Network is unreachable"), errors.New("exit status 2")
				}
				return []byte("2400:3200::1 from :: via fe80::1 dev pppoe-wan"), nil
			}
			return []byte(e.neigh), nil
		case "nslookup", "ping", "traceroute", "curl":
			return []byte(name + " " + strings.Join(args, " ")), nil
		}
		return real(ctx, name, args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		e.m.shutdown()
	})
	e.m = newManagerWith(ctx, e.p, e.sys)
	e.m.client.Resolver.Builtin = []string{"http://127.0.0.1:1"}
	e.m.client.Resolver.DoH = []string{"http://127.0.0.1:1/dns"}
	e.m.svc.every = 50 * time.Millisecond
	// The fake core serves only its controller; the start's checks run quickly.
	e.m.svc.ready = func(mihomo.Settings) bool { return true }
	e.m.svc.confirm = 10 * time.Millisecond
	return e
}

func (e *env) call(method string, args any) map[string]any {
	e.t.Helper()
	b, _ := json.Marshal(args)
	if args == nil {
		b = []byte("{}")
	}
	out, err := e.m.handlers[method](context.Background(), b)
	if err != nil {
		e.t.Fatalf("%s: %v", method, err)
	}
	var v map[string]any
	raw, _ := json.Marshal(out)
	json.Unmarshal(raw, &v)
	return v
}

func (e *env) signIn() {
	if err := e.m.sessions.Save(&account.Session{JWT: "jwt", Email: "a@b.c"}); err != nil {
		e.t.Fatal(err)
	}
}

func golden(t *testing.T, name string) map[string]any {
	b, err := os.ReadFile("../../tests/golden/rpc/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(b, &v)
	return v
}

func sameJSON(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("%s differs from golden\n got: %s\nwant: %s", name, g, w)
	}
}

// keysAndTypes compares the shape: same keys, same JSON types (null matches anything).
func sameShape(t *testing.T, name string, got, want map[string]any) {
	t.Helper()
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s: missing key %s", name, k)
			continue
		}
		if w != nil && g != nil && reflect.TypeOf(w) != reflect.TypeOf(g) {
			t.Errorf("%s.%s: %T, want %T", name, k, g, w)
		}
	}
}

func TestGoldenReadOnly(t *testing.T) {
	e := newEnv(t, nil)
	sameJSON(t, "nodes", e.call("nodes", nil), golden(t, "nodes"))
	sameJSON(t, "rules_list", e.call("rules_list", nil), golden(t, "rules_list"))
	sameJSON(t, "lan_clients", e.call("lan_clients", nil), map[string]any{
		"clients": []any{
			map[string]any{"mac": "00:d9:d1:8b:33:e2", "ip": "192.168.5.3", "name": "phone"},
			map[string]any{"mac": "aa:aa:aa:aa:aa:01", "ip": "192.168.5.4", "name": "tv"},
			map[string]any{"mac": "5a:94:ef:e4:0c:dd", "ip": "192.168.5.2", "name": nil, "online": true},
		},
		"lan_proxy_mode": "except_listed", "direct_macs": []any{"00:d9:d1:8b:33:e2"}})
	sameJSON(t, "latency_status", e.call("latency_status", map[string]any{"id": "none"}), golden(t, "latency_status"))
	sameJSON(t, "logs", e.call("logs", map[string]any{"type": "combined"}), golden(t, "logs"))
	sameJSON(t, "checkin_status", e.call("checkin_status", nil), golden(t, "checkin_status"))
	sameJSON(t, "task_status", e.call("task_status", map[string]any{"id": "x"}), golden(t, "task_status"))
	sameJSON(t, "acllist_read", e.call("acllist_read", map[string]any{"type": "direct"}), golden(t, "acllist_read-invalid")) // the list types are direct_list / proxy_list
	sameJSON(t, "sync_status", e.call("sync_status", nil), golden(t, "sync_status"))

	// get_config: the capture method, IPv6 and fake-ip are automatic, not settings.
	want := golden(t, "get_config")
	want["api_base"] = "http://127.0.0.1:1" // the test's API
	sameJSON(t, "get_config", e.call("get_config", nil), want)

	st := e.call("status", nil)
	sameShape(t, "status", st, golden(t, "status"))
	if st["main_node_label"] != "日本 · 东京 01" || st["mode"] != "smart" || st["enabled"] != true || st["running"] != false || st["signed_in"] != false {
		t.Errorf("status %v", st)
	}
	if st["core_version"] != "Mihomo Meta fake linux arm64" {
		t.Errorf("core version %v", st["core_version"])
	}
}

func TestEveryMethodHasAHandler(t *testing.T) {
	e := newEnv(t, nil)
	for name := range methodArgs {
		if e.m.handlers[name] == nil {
			t.Errorf("%s has no handler", name)
		}
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) opDone(id float64) func() bool {
	return func() bool {
		op, _ := e.call("status", nil)["operation"].(map[string]any)
		return op != nil && op["id"].(float64) >= id && (op["state"] == "done" || op["state"] == "failed")
	}
}

func readFile(path string) string { b, _ := os.ReadFile(path); return string(b) }

func TestServiceLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	e.m.run(e.m.ctx) // enabled in the fixture: starts on boot
	waitFor(t, "boot start", e.opDone(1))
	st := e.call("status", nil)
	if op := st["operation"].(map[string]any); op["state"] != "done" {
		t.Fatalf("start failed: %v\n%s", op, readFile(e.p.Log))
	}
	if st["running"] != true || st["health"].(map[string]any)["state"] != "ok" || st["uptime"] == nil {
		t.Fatalf("%v", st)
	}
	if !strings.Contains(readFile(e.net), "up HYPER_METHOD='tproxy'") {
		t.Fatalf("network helper: %q", readFile(e.net))
	}
	if _, err := os.Stat(e.p.Owned); err != nil {
		t.Fatal("takeover not installed")
	}
	cfg := readFile(filepath.Join(e.p.CoreDir, "config.yaml"))
	if !strings.Contains(cfg, "default-selected: cfg-pp_tokyo-out") || !strings.Contains(cfg, "hyper-device-00d9d18b33e2") {
		t.Fatalf("config:\n%s", cfg)
	}
	if got := readFile(filepath.Join(e.p.CoreDir, "devices/hyper-device-00d9d18b33e2.txt")); got != "" {
		t.Fatalf("device list %q", got)
	}
	if !strings.Contains(readFile(e.p.Plan), "HYPER_TPROXY_PORT='5332'") {
		t.Fatal(readFile(e.p.Plan))
	}

	// Live node switch, no restart.
	if r := e.call("select_node", map[string]any{"node": "pp_hk"}); r["result"] != true {
		t.Fatal(r)
	}
	waitFor(t, "select", func() bool { return strings.Contains(readFile(e.core), "select main-out cfg-pp_hk-out") })
	if r := e.call("select_node", map[string]any{"node": "../x"}); r["result"] != false {
		t.Fatal(r)
	}
	if c, _ := e.m.config(); c.Get("config", "main_node") != "pp_hk" {
		t.Fatal("selection not saved")
	}

	// Latency through the live core.
	r := e.call("latency", map[string]any{"nodes": []string{"pp_tokyo", "pp_sg", "bogus"}})
	id := r["id"].(string)
	waitFor(t, "latency", func() bool { return e.call("latency_status", map[string]any{"id": id})["state"] == "done" })
	ls := e.call("latency_status", map[string]any{"id": id})
	if ls["total"].(float64) != 2 || ls["latency"].(map[string]any)["pp_tokyo"].(float64) != 42 {
		t.Fatal(ls)
	}

	tr := e.call("traffic", nil)
	src := tr["per_source"].(map[string]any)["192.168.1.20"].(map[string]any)
	if tr["running"] != true || tr["upload_total"].(float64) != 100 || src["connections"].(float64) != 2 || src["download"].(float64) != 25 {
		t.Fatal(tr)
	}

	if tot := e.call("traffic", map[string]any{"per_source": false}); tot["upload_total"].(float64) != 100 || tot["per_source"] != nil {
		t.Fatal(tot)
	}

	// One controller client per running core: polling does not pile up connections.
	fds := func() int { d, _ := os.ReadDir("/proc/self/fd"); return len(d) }
	before := fds()
	for i := 0; i < 30; i++ {
		e.call("traffic", nil)
	}
	if after := fds(); after > before+3 {
		t.Fatalf("traffic polling opened %d descriptors", after-before)
	}

	// Device lists follow the neighbour table without a restart.
	e.neigh += "192.168.5.3 dev br-lan lladdr 00:d9:d1:8b:33:e2 REACHABLE\n"
	e.m.devices.refresh(context.Background())
	if got := readFile(filepath.Join(e.p.CoreDir, "devices/hyper-device-00d9d18b33e2.txt")); got != "192.168.5.3/32\n" {
		t.Fatalf("device list %q", got)
	}
	if !strings.Contains(readFile(e.core), "refresh hyper-device-00d9d18b33e2") {
		t.Fatal("provider not refreshed")
	}

	// Guard failed open: the monitor restores the takeover once the core answers.
	os.Remove(e.p.Owned)
	waitFor(t, "recovery", func() bool { _, err := os.Stat(e.p.Owned); return err == nil })
	if !strings.Contains(readFile(e.p.Log), "restoring proxy rules") {
		t.Fatal(readFile(e.p.Log))
	}

	// A settings change restarts, and the restart picks the capture method again: this
	// firewall refuses the tproxy rule now, so TUN is passed to the helper.
	e.noTProxy = true
	if r := e.call("set_config", map[string]any{"values": map[string]any{"config.log_level": "info"}}); r["result"] != true {
		t.Fatal(r)
	}
	opID := e.call("status", nil)["operation"].(map[string]any)["id"].(float64)
	waitFor(t, "restart", e.opDone(opID))
	if !strings.Contains(readFile(e.net), "up HYPER_METHOD='tun'") || !strings.Contains(readFile(filepath.Join(e.p.CoreDir, "config.yaml")), "device: hypertun0") {
		t.Fatal(readFile(e.net))
	}

	// Stop releases the takeover and ends the core.
	r = e.call("set_enabled", map[string]any{"enabled": false})
	waitFor(t, "stop", e.opDone(r["operation"].(map[string]any)["id"].(float64)))
	st = e.call("status", nil)
	if st["running"] != false || st["enabled"] != false || st["health"] != nil {
		t.Fatal(st)
	}
	if _, err := os.Stat(e.p.Owned); err == nil {
		t.Fatal("takeover left installed")
	}
	if e.call("traffic", nil)["running"] != false {
		t.Fatal("traffic while stopped")
	}
	// Restart is ignored while disabled (an explicit Stop wins).
	if op := e.m.svc.request("restart"); op != nil {
		t.Fatal("restart while disabled")
	}
}

// firewallEnv starts the service with the takeover's table reported gone on demand.
func firewallEnv(t *testing.T) (*env, *atomic.Bool) {
	e := newEnv(t, nil)
	gone := new(atomic.Bool)
	e.m.svc.tableGone = func(name string) bool { return name == "hyper" && gone.Load() }
	e.signIn()
	e.m.run(e.m.ctx)
	waitFor(t, "boot start", e.opDone(1))
	return e, gone
}

// A firewall restart that wipes the takeover's table is repaired while the core answers;
// when repairs keep coming, the LAN goes direct for a while instead of fighting.
func TestFirewallRepair(t *testing.T) {
	e, gone := firewallEnv(t)
	gone.Store(true)
	waitFor(t, "repair", func() bool { return strings.Contains(readFile(e.p.Log), "restored them") })
	waitFor(t, "repair limit", func() bool { return strings.Contains(readFile(e.p.Log), "keep being removed") })
	if n := strings.Count(readFile(e.net), "repair "); n != repairLimit {
		t.Fatalf("%d repairs, want %d", n, repairLimit)
	}
	waitFor(t, "fail open", func() bool { _, err := os.Stat(e.p.Owned); return err != nil })
	// Held direct: the monitor does not take the LAN over again meanwhile.
	time.Sleep(5 * e.m.svc.every)
	if _, err := os.Stat(e.p.Owned); err == nil || e.call("status", nil)["health"].(map[string]any)["state"] != "failopen" {
		t.Fatal("taken over again during the hold")
	}
}

// An init script stop releases the takeover, then signals serve. Until then the monitor
// neither restores nor repairs, and a restart queued meanwhile starts nothing.
func TestInitStopHoldsTheTakeover(t *testing.T) {
	e, gone := firewallEnv(t)
	waitFor(t, "settled", func() bool { op := e.m.svc.operation(); return op.State == "done" && e.m.svc.isRunning() })
	os.WriteFile(e.p.stopping(), nil, 0o600)
	if out, err := exec.Command(e.p.Network, "down").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	before := len(readFile(e.net))
	gone.Store(true)
	time.Sleep(5 * e.m.svc.every)
	if _, err := os.Stat(e.p.Owned); err == nil {
		t.Fatal("taken over again while stopping")
	}
	if calls := readFile(e.net)[before:]; calls != "" {
		t.Fatalf("calls while stopping:\n%s", calls)
	}
	op := e.m.svc.request("restart")
	waitFor(t, "restart", e.opDone(float64(op.ID)))
	if calls := readFile(e.net)[before:]; e.m.svc.isRunning() || strings.Contains(calls, "up ") {
		t.Fatalf("restarted while stopping:\n%s", calls)
	}
}

// A repair that fails releases the takeover at once.
func TestFirewallRepairFails(t *testing.T) {
	e, gone := firewallEnv(t)
	os.WriteFile(e.p.RunDir+"/repair-fails", nil, 0o600)
	gone.Store(true)
	waitFor(t, "fail open", func() bool { _, err := os.Stat(e.p.Owned); return err != nil })
	if n := strings.Count(readFile(e.net), "repair "); n != 1 || !strings.Contains(readFile(e.p.Log), "Restoring the removed firewall rules failed") {
		t.Fatalf("%d repairs\n%s", n, readFile(e.p.Log))
	}
}

func TestStartFailures(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	os.MkdirAll(filepath.Dir(e.p.list("direct_list")), 0o755)
	os.WriteFile(e.p.list("direct_list"), []byte("FAIL-CHECK\n"), 0o644)
	e.m.run(e.m.ctx)
	waitFor(t, "start", e.opDone(1))
	op := e.call("status", nil)["operation"].(map[string]any)
	// The page reads a code; the log keeps the core's words.
	if op["state"] != "failed" || op["error"] != "core_config_invalid" || !strings.Contains(readFile(e.p.Log), "FAIL-CHECK") {
		t.Fatal(op, readFile(e.p.Log))
	}
	if readFile(e.net) != "" && !strings.HasPrefix(readFile(e.net), "down") {
		t.Fatalf("takeover attempted after a failed check: %q", readFile(e.net))
	}
	os.Remove(e.p.list("direct_list"))
	os.Remove(e.p.nodes())
	e.call("restart", nil)
	waitFor(t, "restart", e.opDone(2))
	if op := e.call("status", nil)["operation"].(map[string]any); op["error"] != "no_nodes" {
		t.Fatal(op)
	}
}

func TestLatencyWithoutService(t *testing.T) {
	e := newEnv(t, func(u map[string]map[string]any) { u["config"]["enabled"] = "0" })
	r := e.call("latency", nil)
	id := r["id"].(string)
	waitFor(t, "probe", func() bool {
		s := e.call("latency_status", map[string]any{"id": id})["state"]
		return s == "done" || s == "failed"
	})
	ls := e.call("latency_status", map[string]any{"id": id})
	if ls["state"] != "done" || ls["completed"].(float64) != 4 {
		t.Fatal(ls)
	}
	if !strings.Contains(readFile(e.core), "/probe/config.yaml 4") {
		t.Fatal(readFile(e.core))
	}
	if _, err := os.Stat(filepath.Join(e.p.RunDir, "probe")); err == nil {
		t.Fatal("probe dir left behind")
	}
	r = e.call("latency", nil)
	e.call("latency_cancel", map[string]any{"id": r["id"]})
	if s := e.call("latency_status", map[string]any{"id": r["id"]}); s["state"] != "cancelled" {
		t.Fatal(s)
	}
}

func TestSettingsMethods(t *testing.T) {
	e := newEnv(t, func(u map[string]map[string]any) { u["config"]["enabled"] = "0" })
	if r := e.call("set_config", map[string]any{"values": map[string]any{"config.log_level": "verbose"}}); r["result"] != false {
		t.Fatal(r)
	}
	if r := e.call("set_config", map[string]any{"values": map[string]any{"bogus.key": "1"}}); r["result"] != false {
		t.Fatal(r)
	}
	// The capture method is picked at each start, not set.
	if r := e.call("set_config", map[string]any{"values": map[string]any{"config.proxy_mode": "tun"}}); r["result"] != false {
		t.Fatal(r)
	}
	r := e.call("set_config", map[string]any{"values": map[string]any{"subscription.auto_update_time": 5}})
	if r["result"] != true {
		t.Fatal(r)
	}
	cfg := e.call("get_config", nil)["config"].(map[string]any)
	if _, ok := cfg["config.proxy_mode"]; ok || cfg["subscription.auto_update_time"] != "5" {
		t.Fatal(cfg)
	}

	for mode, want := range map[string][2]string{"dns": {"smart", "1"}, "global": {"global", "0"}, "smart": {"smart", "0"}} {
		if r := e.call("set_mode", map[string]any{"mode": mode}); r["result"] != true {
			t.Fatal(r)
		}
		c, _ := e.m.config()
		if c.Get("config", "routing_mode") != want[0] || c.Get("config", "dns_only") != want[1] || e.call("status", nil)["mode"] != mode {
			t.Fatal(mode)
		}
	}
	if r := e.call("set_mode", map[string]any{"mode": "custom"}); r["result"] != false {
		t.Fatal(r)
	}

	if r := e.call("set_rule_enabled", map[string]any{"key": "streaming", "enabled": false}); r["result"] != true {
		t.Fatal(r)
	}
	rules := e.call("rules_list", nil)["rules"].([]any)
	if rules[1].(map[string]any)["enabled"] != false || rules[1].(map[string]any)["server_enabled"] != true {
		t.Fatal(rules)
	}
	e.call("set_rule_enabled", map[string]any{"key": "streaming", "enabled": true})
	if c, _ := e.m.config(); len(c.Section("rules").List("disabled")) != 0 {
		t.Fatal("disabled list kept")
	}

	if r := e.call("acllist_write", map[string]any{"type": "proxy_list", "content": "google\r\nyoutube\n\ngoogle\n"}); r["count"].(float64) != 2 {
		t.Fatal(r)
	}
	if r := e.call("acllist_read", map[string]any{"type": "proxy_list"}); r["content"] != "google\nyoutube\n" {
		t.Fatal(r)
	}
	if r := e.call("acllist_write", map[string]any{"type": "proxy_list", "content": "ok\nbad entry;rm"}); r["result"] != false {
		t.Fatal(r)
	}

	e.call("set_access_control", map[string]any{"mode": "pick", "direct_macs": []string{"AA:BB:CC:DD:EE:FF", "junk"}})
	lc := e.call("lan_clients", nil)
	if lc["lan_proxy_mode"] != "except_listed" || !reflect.DeepEqual(lc["direct_macs"], []any{"aa:bb:cc:dd:ee:ff"}) {
		t.Fatal(lc)
	}
	e.call("set_access_control", map[string]any{"mode": "all"})
	if lc := e.call("lan_clients", nil); lc["lan_proxy_mode"] != "disabled" || len(lc["direct_macs"].([]any)) != 0 {
		t.Fatal(lc)
	}

	if r := e.call("set_enabled", map[string]any{"enabled": true}); r["result"] != false || r["error"] != "not signed in" {
		t.Fatal(r)
	}
}

func TestAccountMethods(t *testing.T) {
	e := newEnv(t, nil)
	if r := e.call("login", map[string]any{"email": "a@b.c"}); r["error"] != "missing credentials" {
		t.Fatal(r)
	}
	if r := e.call("send_code", map[string]any{"email": "nope"}); r["error"] != "illegal email" {
		t.Fatal(r)
	}
	e.signIn()
	today := time.Now().Format("2006-01-02")
	if r := e.call("checkin", map[string]any{"date": today}); r["result"] != true || r["added"] != true {
		t.Fatal(r)
	}
	if r := e.call("checkin_status", nil); len(r["dates"].([]any)) != 1 {
		t.Fatal(r)
	}
	if r := e.call("checkin", map[string]any{"date": "2001-01-01"}); r["error"] != "illegal date" {
		t.Fatal(r)
	}

	// api runs as a task; refused paths fail without network I/O.
	r := e.call("api", map[string]any{"path": "/v1/admin/x"})
	id, _ := r["_hyper_task"].(string)
	if id == "" || r["_hyper_timeout"].(float64) != 180 {
		t.Fatal(r)
	}
	waitFor(t, "task", func() bool { return e.call("task_status", map[string]any{"id": id})["state"] != "running" })
	if r := e.call("task_status", map[string]any{"id": id}); r["error"] != "path not allowed" {
		t.Fatal(r)
	}
	r = e.call("api", map[string]any{"method": "POST", "path": "/v2/orders/purchase"})
	waitFor(t, "task", func() bool {
		return e.call("task_status", map[string]any{"id": r["_hyper_task"]})["state"] != "running"
	})
	if got := e.call("task_status", map[string]any{"id": r["_hyper_task"]}); got["error"] != "idempotency key required" {
		t.Fatal(got)
	}
	// Another account never sees the result.
	e.m.sessions.Save(&account.Session{JWT: "other"})
	if got := e.call("task_status", map[string]any{"id": r["_hyper_task"]}); got["error"] != "session_changed" {
		t.Fatal(got)
	}

	e.call("logout", nil)
	if e.m.sessions.Load() != nil || e.m.enabled() {
		t.Fatal("logout")
	}
}

func TestDiagAndLogs(t *testing.T) {
	e := newEnv(t, nil)
	r := e.call("diag", map[string]any{"type": "ping", "target": "example.com"})
	waitFor(t, "diag", func() bool {
		return e.call("task_status", map[string]any{"id": r["_hyper_task"]})["state"] != "running"
	})
	if got := e.call("task_status", map[string]any{"id": r["_hyper_task"]}); got["output"] != "ping -c 4 -W 2 example.com" {
		t.Fatal(got)
	}
	r = e.call("diag", map[string]any{"type": "http", "target": "-x;rm"})
	waitFor(t, "diag", func() bool {
		return e.call("task_status", map[string]any{"id": r["_hyper_task"]})["state"] != "running"
	})
	if got := e.call("task_status", map[string]any{"id": r["_hyper_task"]}); got["error"] != "illegal target" {
		t.Fatal(got)
	}

	os.WriteFile(e.p.Log, []byte("2026-11-21 10:00:02 [SYNC] b\n2026-11-21 10:00:00 [DAEMON] a\n"), 0o600)
	os.WriteFile(e.p.CoreLog, []byte("time=\"2026-11-21T10:00:01Z\" level=warning msg=core\n  continued\n"), 0o600)
	got := e.call("logs", map[string]any{"type": "combined", "lines": 50})["content"]
	want := "2026-11-21 10:00:00 [DAEMON] a\n2026-11-21 10:00:01 WARN core\n  continued\n2026-11-21 10:00:02 [SYNC] b"
	if got != want {
		t.Fatalf("%q", got)
	}
	if r := e.call("logs", map[string]any{"type": "../etc"}); r["error"] != "illegal type" {
		t.Fatal(r)
	}
	e.call("log_clean", map[string]any{"type": "core"})
	if readFile(e.p.CoreLog) != "" || readFile(e.p.Log) == "" {
		t.Fatal("log_clean")
	}
}

func TestKeepMainNode(t *testing.T) {
	e := newEnv(t, nil)
	store := fixtureNodes()
	store.Nodes = store.Nodes[1:] // pp_tokyo gone: first node of the top country (JP)
	e.m.keepMainNode(store)
	if c, _ := e.m.config(); c.Get("config", "main_node") != "pp_anytls" {
		t.Fatal(c.Get("config", "main_node"))
	}
}

func TestInstallUpdateHelperIsDetached(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	e := newEnv(t, nil)
	if r := e.m.installUpdate(context.Background(), false, true); r["error"] != "automatic_update_disabled" {
		t.Fatal(r)
	}
}

// An interface notice restarts the core only when it changed the rendered config.
func TestNotifyNetwork(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	if r := e.call("notify_network", nil); r["restarted"] != false {
		t.Fatal("restarted while stopped", r)
	}
	e.m.run(e.m.ctx)
	waitFor(t, "start", e.opDone(1))
	if r := e.call("notify_network", nil); r["result"] != true || r["restarted"] != false {
		t.Fatal("restart without a change", r)
	}
	if op := e.call("status", nil)["operation"].(map[string]any); op["id"].(float64) != 1 {
		t.Fatal("a restart was queued", op)
	}
	// A guest LAN that was down at the start comes up: the takeover must cover it.
	os.WriteFile(e.p.RunDir+"/missing-lans", []byte("br-guest\n"), 0o600)
	if r := e.call("notify_network", nil); r["restarted"] != true {
		t.Fatal("a LAN that came up did not restart", r)
	}
	waitFor(t, "restart", e.opDone(2))
	os.WriteFile(e.p.RunDir+"/missing-lans", []byte("bad name; rm\n"), 0o600)
	if r := e.call("notify_network", nil); r["restarted"] != false {
		t.Fatal("restarted on malformed helper output", r)
	}
	os.Remove(e.p.RunDir + "/missing-lans")
	e.wan = "192.168.1.254" // PPPoE redial with a new resolver
	if r := e.call("notify_network", nil); r["restarted"] != true {
		t.Fatal("new resolver did not restart", r)
	}
	waitFor(t, "restart", e.opDone(3))
	if !strings.Contains(readFile(filepath.Join(e.p.CoreDir, "config.yaml")), "192.168.1.254") {
		t.Fatal("restart did not use the new resolver")
	}
	if _, err := rpcdCall(context.Background(), e.p.Socket, "notify_network", strings.NewReader("{}")); err == nil {
		t.Fatal("rpcd may not reach notify_network")
	}
}

// Multi-word arguments (cf_token, ...) reach the API under their snake_case names.
func TestLoginForwardsArguments(t *testing.T) {
	e := newEnv(t, nil)
	var body map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/common/site/config":
			w.Write([]byte(`{"code":200,"msg":"","data":{}}`))
		case "/v1/auth/login":
			json.NewDecoder(r.Body).Decode(&body)
			w.Write([]byte(`{"code":200,"msg":"","data":{"token":"jwt"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	e.m.client.Resolver.Builtin = []string{api.URL}
	r := e.call("login", map[string]any{"email": "a@b.c", "code": "123456", "cf_token": "turnstile", "invite": "INV"})
	if r["result"] != true || body["cf_token"] != "turnstile" || body["invite"] != "INV" || body["password"] != "123456" {
		t.Fatalf("%v %v", r, body)
	}
}

func TestCoreLogText(t *testing.T) {
	in := `time="2026-11-21T02:24:54.334905759-08:00" level=error msg="[TCP] dial cfg-pp_x-out (match RuleSet/hyper-rule-a) 192.168.1.20:5000 --> x.com:443 error: \"timeout\""` +
		"\npanic: boom\ntime=bad level=info msg=x"
	got := coreLogText(in)
	want := `2026-11-21 02:24:54 ERROR [TCP] dial cfg-pp_x-out (match RuleSet/hyper-rule-a) 192.168.1.20:5000 --> x.com:443 error: "timeout"` +
		"\npanic: boom\ntime=bad level=info msg=x"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

// The capture method is TProxy where the firewall accepts the helper's rule, TUN where only
// the kernel's TUN device is there, and TProxy again with neither (the start then reports
// what is missing); the check leaves nothing behind.
func TestCaptureMethod(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	for _, c := range []struct {
		noTProxy, noTUN bool
		want            string
	}{{false, false, "tproxy"}, {false, true, "tproxy"}, {true, false, "tun"}, {true, true, "tproxy"}} {
		e.noTProxy, e.noTUN = c.noTProxy, c.noTUN
		if got := e.m.captureMethod(ctx); got != c.want {
			t.Errorf("tproxy refused %v, no TUN %v: %s, want %s", c.noTProxy, c.noTUN, got, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(e.p.RunDir, "tproxy-probe.nft")); !os.IsNotExist(err) {
		t.Fatal("the probe ruleset was left behind:", err)
	}
	t.Setenv("HYPERD_METHOD", "tun")
	e.noTProxy, e.noTUN = false, false
	if got := e.m.captureMethod(ctx); got != "tun" {
		t.Fatal("HYPERD_METHOD:", got)
	}
}

// IPv6 interception follows the router's IPv6 uplink; there is no switch.
func TestIPv6FollowsUplink(t *testing.T) {
	e := newEnv(t, nil)
	s, err := e.m.runtimeSettings(context.Background())
	if err != nil || s.IPv6 {
		t.Fatalf("IPv6 without a route: %v %v", s.IPv6, err)
	}
	e.v6 = true
	if s, _ = e.m.runtimeSettings(context.Background()); !s.IPv6 || !s.FakeIP {
		t.Fatalf("%+v", s)
	}
	e.signIn()
	e.m.run(e.m.ctx)
	waitFor(t, "start", e.opDone(1))
	if !strings.Contains(readFile(e.p.Plan), "HYPER_IPV6='1'") || !strings.Contains(readFile(filepath.Join(e.p.CoreDir, "config.yaml")), "ipv6: true") {
		t.Fatal(readFile(e.p.Plan))
	}
}

// The nodes file is parsed once per version, and a sync's rename is always seen, even within
// the same second and at the same size.
func TestNodeCache(t *testing.T) {
	e := newEnv(t, nil)
	a, err := e.m.nodes()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := e.m.nodes(); b != a {
		t.Fatal("parsed again without a change")
	}
	next := fixtureNodes()
	next.Nodes[0].Label = "日本 · 东京 02" // same length in bytes
	if err := next.Save(e.p.nodes()); err != nil {
		t.Fatal(err)
	}
	c, _ := e.m.nodes()
	if c == a || c.Nodes[0].Label != "日本 · 东京 02" {
		t.Fatal("stale nodes after a sync")
	}
	if st := e.call("status", nil); st["main_node_label"] != "日本 · 东京 02" {
		t.Fatal(st["main_node_label"])
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The checkup probes sites on both sides of the rules: through the core while it runs.
func TestHealthCheckSites(t *testing.T) {
	e := newEnv(t, nil)
	var mu sync.Mutex
	via := map[string]string{}
	e.sys.HTTP = func(proxy string) *http.Client {
		return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			via[r.URL.Host] = proxy
			mu.Unlock()
			if r.Method != http.MethodHead || r.URL.Host == "github.com" {
				return nil, errors.New("unreachable")
			}
			return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
		})}
	}
	check := func(running bool, proxy string) {
		t.Helper()
		id := e.call("health_check", nil)["_hyper_task"]
		var r map[string]any
		waitFor(t, "health check", func() bool {
			r = e.call("task_status", map[string]any{"id": id})
			return r["state"] != "running"
		})
		sites, _ := r["sites"].(map[string]any)
		if r["via_core"] != running || len(sites) != 5 {
			t.Fatal(r)
		}
		for name, s := range sites {
			if ok := s.(map[string]any)["ok"]; ok != (name != "github") {
				t.Errorf("%s: ok %v", name, ok)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for host, p := range via {
			if p != proxy {
				t.Errorf("%s via %q, want %q", host, p, proxy)
			}
		}
	}
	check(false, "")
	e.signIn()
	e.m.run(e.m.ctx)
	waitFor(t, "start", e.opDone(1))
	check(true, e.m.svc.proxyURL())
}

// The core reads each mainland list through builtin/<list>.mrs: the packaged copy until a
// valid download exists, then the download, which the running core is told to reload.
func TestMainlandListLinks(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	e.m.run(e.m.ctx)
	waitFor(t, "start", e.opDone(1))
	link := func(list string) string {
		s, _ := os.Readlink(filepath.Join(e.p.CoreDir, "builtin", list+".mrs"))
		return s
	}
	for _, list := range cnlist.Names {
		if link(list) != filepath.Join(e.p.Etc, "rule-set", list+".mrs") {
			t.Fatalf("%s -> %q", list, link(list))
		}
	}
	data, err := os.ReadFile("../../luci-app-hyper/root/etc/hyper/rule-set/geoip-cn.mrs")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(e.p.Etc, "cnlists"), 0o755)
	os.WriteFile(filepath.Join(e.p.Etc, "cnlists", "geoip-cn.mrs"), data, 0o644)
	e.m.reloadLists(context.Background(), nil)
	if link(cnlist.Addresses) != filepath.Join(e.p.Etc, "cnlists", "geoip-cn.mrs") || link(cnlist.Domains) != filepath.Join(e.p.Etc, "rule-set", "geosite-cn.mrs") {
		t.Fatal(link(cnlist.Addresses), link(cnlist.Domains))
	}
	if calls := readFile(e.core); !strings.Contains(calls, "refresh geoip-cn") || strings.Contains(calls, "refresh geosite-cn") {
		t.Fatal(calls)
	}
}

// A takeover that falls over at once fails the start, instead of the page showing it
// connected, and the core does not stay behind.
func TestStartConfirmsHealth(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	// up succeeds but leaves nothing installed, as a firewall reload right after it would.
	os.WriteFile(e.p.Network, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	e.m.run(e.m.ctx)
	waitFor(t, "boot start", e.opDone(1))
	st := e.call("status", nil)
	if op := st["operation"].(map[string]any); op["state"] != "failed" || op["error"] != "takeover_lost_on_start" {
		t.Fatalf("start counted: %v", op)
	}
	if st["running"] == true {
		t.Fatal("core kept running after a failed start")
	}
}

// One or two failed checks only start the healthy streak again: a takeover that settles
// within the confirmation still counts, and the monitor leaves it to the start meanwhile.
func TestStartConfirmationRecovers(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	// The first up leaves nothing installed; the check after it puts the takeover in place.
	e.m.svc.confirm = 50 * time.Millisecond
	os.WriteFile(e.p.Network, []byte(fmt.Sprintf("#!/bin/sh\necho \"$1\" >> %s\n[ \"$1\" != down ] || rm -f %s\n", e.net, e.p.Owned)), 0o755)
	go func() {
		waitFor(t, "takeover", func() bool { return strings.Contains(readFile(e.net), "up") })
		time.Sleep(60 * time.Millisecond)
		os.WriteFile(e.p.Owned, nil, 0o600)
	}()
	e.m.run(e.m.ctx)
	waitFor(t, "boot start", e.opDone(1))
	if op := e.call("status", nil)["operation"].(map[string]any); op["state"] != "done" {
		t.Fatalf("a takeover that settled failed the start: %v\n%s", op, readFile(e.p.Log))
	}
	// The monitor left the unconfirmed start alone instead of taking over itself.
	if n := strings.Count(readFile(e.net), "up"); n != 1 {
		t.Fatalf("%d takeovers\n%s", n, readFile(e.p.Log))
	}
}

// A core that exits during the confirmation fails the start at once, with the reason its
// last error line gave.
func TestStartConfirmationEndsWithTheCore(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	e.m.svc.confirm = time.Second
	os.WriteFile(e.p.Network, []byte(fmt.Sprintf(`#!/bin/sh
case "$1" in up) touch %s; echo "Start TProxy server error: bind: address already in use" > %s/core-exit ;; down) rm -f %[1]s ;; esac
`, e.p.Owned, e.p.RunDir)), 0o755)
	e.m.run(e.m.ctx)
	start := time.Now()
	waitFor(t, "boot start", e.opDone(1))
	op := e.call("status", nil)["operation"].(map[string]any)
	if op["state"] != "failed" || op["error"] != "core_exited_on_start" ||
		!strings.Contains(readFile(e.p.Log), "mihomo exited right after starting: Start TProxy server error") {
		t.Fatalf("%v\n%s", op, readFile(e.p.Log))
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the start ended %v after the core", d)
	}
}

// A failed start is tried again on its own once its cause went away.
func TestFailedStartRetries(t *testing.T) {
	e := newEnv(t, nil)
	e.signIn()
	e.m.svc.retryAfter = 50 * time.Millisecond
	good, _ := os.ReadFile(e.p.Network)
	os.WriteFile(e.p.Network, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	e.m.run(e.m.ctx)
	waitFor(t, "boot start", e.opDone(1))
	if op := e.call("status", nil)["operation"].(map[string]any); op["state"] != "failed" {
		t.Fatal(op)
	}
	os.WriteFile(e.p.Network, good, 0o755)
	waitFor(t, "retried start", func() bool {
		st := e.call("status", nil)
		op := st["operation"].(map[string]any)
		return st["running"] == true && op["state"] == "done" && op["id"].(float64) == 1
	})
	if !strings.Contains(readFile(e.p.Log), "Trying the failed start again") {
		t.Fatal(readFile(e.p.Log))
	}
	// A stop cancels the next retry.
	os.WriteFile(e.p.Network, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	e.call("restart", nil)
	waitFor(t, "failed restart", e.opDone(2))
	e.call("set_enabled", map[string]any{"enabled": false})
	waitFor(t, "stop", e.opDone(3))
	os.WriteFile(e.p.Network, good, 0o755)
	time.Sleep(300 * time.Millisecond)
	if st := e.call("status", nil); st["running"] == true {
		t.Fatal("a stopped service started again")
	}
}

// The listener check reads the host's sockets: a bound UDP port and a listening TCP port
// count, a TCP socket that only dialled out does not.
func TestListening(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc/net")
	}
	u, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.Dial("tcp4", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	port := func(a net.Addr) int { _, p, _ := net.SplitHostPort(a.String()); n, _ := strconv.Atoi(p); return n }
	if !listening("udp", port(u.LocalAddr())) || !listening("tcp", port(l.Addr())) {
		t.Fatal("bound sockets not found")
	}
	if listening("tcp", port(c.LocalAddr())) || listening("tcp", port(u.LocalAddr())) {
		t.Fatal("a socket that does not listen counted")
	}
}
