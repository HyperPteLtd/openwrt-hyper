package netguard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// What the network script writes with DNS protection on.
const dnsInjection = "# Hyper managed upstream\nno-resolv\nserver=127.0.0.1#19764\nserver=/probe.hyper.invalid/127.0.0.1#19764\nrebind-domain-ok=/probe.hyper.invalid/\n"

func TestOwnershipDetection(t *testing.T) {
	c := collector{seen: map[Resource]bool{}, own: ownership{activeNft: true}}
	nft := `{"nftables":[{"table":{"family":"inet","name":"hyper","comment":"hyper-owned-v1"}},{"table":{"family":"inet","name":"v2raya"}},{"rule":{"family":"ip6","table":"fw4","expr":[{"jump":{"target":"PSW2_DNS"}}]}}]}`
	if e := c.nft([]byte(nft)); e != nil {
		t.Fatal(e)
	}
	c.xt("ip6tables", []byte(":OPENCLASH - [0:0]\n-A OUTPUT -j OPENCLASH\n"))
	c.routes("ipv6", []byte("32765: from all fwmark 0x4f57/0xffff lookup 100\n"))
	c.routes("ipv4", []byte("local default dev lo table 21998\n"))
	if !c.seen[Resource{"nft", "inet table v2raya", "v2raya"}] || !c.seen[Resource{"nft", "ip6 jump passwall2", "passwall2"}] || !c.seen[Resource{"xtables", "ip6tables openclash", "openclash"}] {
		t.Fatal(c.sorted())
	}
	if c.seen[Resource{"nft", "inet hyper (unknown owner)", ""}] {
		t.Fatal("mistook owned table for foreign table")
	}
	if len(c.seen) != 5 {
		t.Fatal(c.sorted())
	}
}

// fakePlugins stands in for the router: Stop takes a plugin's service and process away, and
// any other resource stays until it is removed by hand.
type fakePlugins struct {
	found           []Resource
	scans           int
	stops, disables []string
	stopErr, disErr map[string]error
	stopAfter       int // further scans a stopped plugin's process lingers for
	stoppedAt       map[string]int
}

func (f *fakePlugins) checker(t *testing.T) Checker {
	return Checker{
		Dir: t.TempDir(),
		Scan: func(context.Context) []Resource {
			f.scans++
			var out []Resource
			for _, r := range f.found {
				at, stopped := f.stoppedAt[r.Service]
				if stopped && (r.Kind == "service" || r.Kind == "process" && f.scans > at+f.stopAfter) {
					continue
				}
				out = append(out, r)
			}
			return out
		},
		Stop: func(_ context.Context, s string) error {
			f.stops = append(f.stops, s)
			if e := f.stopErr[s]; e != nil {
				return e
			}
			if f.stoppedAt == nil {
				f.stoppedAt = map[string]int{}
			}
			f.stoppedAt[s] = f.scans
			return nil
		},
		Disable: func(_ context.Context, s string) error {
			f.disables = append(f.disables, s)
			return f.disErr[s]
		},
	}
}

func TestResolveStopsAndDisablesConflictingPlugins(t *testing.T) {
	f := &fakePlugins{found: []Resource{
		{"nft", "inet table openclash", "openclash"},
		{"process", "openclash", "openclash"},
		{"service", "openclash", "openclash"},
	}}
	r, e := f.checker(t).Resolve(context.Background())
	// The nft table outlives the stop here, so it still blocks, but the plugin itself is gone.
	if e != nil || !r.Blocked || !reflect.DeepEqual(r.Resources, []Resource{{"nft", "inet table openclash", "openclash"}}) {
		t.Fatal(r, e)
	}
	if !reflect.DeepEqual(f.stops, []string{"openclash"}) || !reflect.DeepEqual(f.disables, []string{"openclash"}) || !reflect.DeepEqual(r.Stopped, []string{"openclash"}) || f.scans != 2 {
		t.Fatal(f.stops, f.disables, r.Stopped, f.scans)
	}

	f = &fakePlugins{found: []Resource{{"service", "homeproxy", "homeproxy"}, {"service", "passwall2", "passwall2"}}}
	r, e = f.checker(t).Resolve(context.Background())
	if e != nil || r.Blocked || len(r.Resources) != 0 || !reflect.DeepEqual(r.Stopped, []string{"homeproxy", "passwall2"}) || !reflect.DeepEqual(f.disables, r.Stopped) {
		t.Fatal(r, e, f.disables)
	}
}

func TestResolveLeavesWhatItCannotRemove(t *testing.T) {
	f := &fakePlugins{found: []Resource{
		{"dnsmasq", "nikki", "nikki"},
		{"listener", "tcp/19761", ""},
		{"process", "mihomo", ""},
		{"service", "openclash", "openclash"},
		{"service", "passwall", "passwall"},
		{"service", "v2raya", "v2raya"},
		{"unverified", "ubus", ""},
	}}
	f.stopErr = map[string]error{"nikki": errNotInstalled, "passwall": errors.New("exit status 1")}
	f.disErr = map[string]error{"v2raya": errors.New("read-only")}
	r, e := f.checker(t).Resolve(context.Background())
	if e != nil || !r.Blocked {
		t.Fatal(r, e)
	}
	// An uninstalled plugin is neither disabled nor reported; a failed stop is judged by the
	// rescan; a plugin that stays enabled at boot blocks the start.
	if !reflect.DeepEqual(f.stops, []string{"nikki", "openclash", "passwall", "v2raya"}) || !reflect.DeepEqual(f.disables, []string{"openclash", "passwall", "v2raya"}) {
		t.Fatal(f.stops, f.disables)
	}
	want := []Resource{
		{"dnsmasq", "nikki", "nikki"},
		{"listener", "tcp/19761", ""},
		{"process", "mihomo", ""},
		{"service", "passwall", "passwall"},
		{"unverified", "ubus", ""},
		{"autostart", "v2raya", "v2raya"},
	}
	if !reflect.DeepEqual(r.Resources, want) || !reflect.DeepEqual(r.Stopped, []string{"openclash"}) {
		t.Fatal(r)
	}

	f = &fakePlugins{found: []Resource{{"process", "mihomo", ""}, {"nft", "inet hyper (unknown owner)", ""}}}
	r, e = f.checker(t).Resolve(context.Background())
	if e != nil || !r.Blocked || len(r.Resources) != 2 || r.Stopped != nil || f.stops != nil || f.scans != 1 {
		t.Fatal(r, e, f.stops, f.scans)
	}
}

func TestResolveWaitsForStoppedProcesses(t *testing.T) {
	f := &fakePlugins{found: []Resource{{"process", "openclash", "openclash"}, {"service", "openclash", "openclash"}}, stopAfter: 1}
	c := f.checker(t)
	c.Settle = 5 * time.Second
	r, e := c.Resolve(context.Background())
	if e != nil || r.Blocked || f.scans != 3 || !reflect.DeepEqual(r.Stopped, []string{"openclash"}) {
		t.Fatal(r, e, f.scans)
	}

	// Without time to settle, a lingering process blocks and the plugin is not reported stopped.
	f = &fakePlugins{found: f.found, stopAfter: 1}
	r, e = f.checker(t).Resolve(context.Background())
	if e != nil || !r.Blocked || f.scans != 2 || r.Stopped != nil || !reflect.DeepEqual(r.Resources, []Resource{{"process", "openclash", "openclash"}}) {
		t.Fatal(r, e, f.scans)
	}
}

func TestInitScriptsOnlyForKnownPlugins(t *testing.T) {
	for _, name := range []string{"firewall", "dnsmasq", "hyper", "../../sbin/reboot", ""} {
		if e := initScript(context.Background(), name, "disable"); e == nil || errors.Is(e, errNotInstalled) {
			t.Fatal(name, e)
		}
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"conflicts", "--confirm", "token"}, {"conflicts", "--resolve", "x"}} {
		var out bytes.Buffer
		if e := Run(context.Background(), args, &out); e == nil || out.Len() != 0 {
			t.Fatal(args, e, out.String())
		}
	}
}

func TestOwnedRouteRequiresJournalAndExactSelectors(t *testing.T) {
	o := ownership{routes: map[string]string{"-4": "tproxy", "-6": "tproxy"}}
	for _, tc := range []struct {
		family, line string
		expected     bool
	}{
		{"-4", "21998: from all fwmark 0x4f57/0xffff lookup 21998 proto 199", true},
		{"-4", "2 default dev lo table 21998 proto 199 scope 254", true},
		{"-6", "2 default dev lo table 21998 proto 199 metric 1024 pref medium", true},
		{"-4", "21998: from all fwmark 0x4f57/0xffff iif br-other lookup 21998 proto 199", false},
		{"-4", "21998: from all fwmark 0x4f57/0xffff lookup 21998 proto 198", false},
		{"-4", "2 default dev lo table 21998 proto 199 scope 254 src 192.0.2.1", false},
		{"-4", "local default dev tun0 table 21998 proto 199 scope host", false},
	} {
		if got := o.reservedRoute(tc.family, strings.Fields(tc.line)); got != tc.expected {
			t.Errorf("%s: %v", tc.line, got)
		}
	}
	journal := t.TempDir()
	dns := t.TempDir()
	dir := filepath.Join(journal, "route4")
	if err := os.WriteFile(dir, []byte("tproxy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journal, "dns"), []byte(dns+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dns, "99-hyper-upstream.conf")
	o = networkOwnership(journal)
	if o.dnsFile(path, []byte(dnsInjection)) || len(o.routes) != 0 {
		t.Fatal("accepted an inactive journal")
	}
	if err := os.WriteFile(filepath.Join(journal, "owned"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	o = networkOwnership(journal)
	if !o.dnsFile(path, []byte(dnsInjection)) || o.dnsFile(path, []byte(dnsInjection+"server=127.0.0.1#80\n")) || o.dnsFile(filepath.Join(t.TempDir(), filepath.Base(path)), []byte(dnsInjection)) || o.dnsFile(filepath.Join(dns, "other.conf"), []byte(dnsInjection)) {
		t.Fatal("incorrect DNS ownership")
	}
	if o.routes["-4"] != "tproxy" || o.routes["-6"] != "" {
		t.Fatal("cross-family ownership", o.routes)
	}
}

func TestSupervisedCoreAndOwnedDescriptors(t *testing.T) {
	proc := t.TempDir()
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	manager := filepath.Join(proc, "hyperd")
	write(manager, "manager")
	link(manager, filepath.Join(proc, "100", "exe"))
	write(filepath.Join(proc, "100", "cmdline"), manager+"\x00serve\x00")
	write(filepath.Join(proc, "101", "status"), "PPid:\t100\n")
	write(filepath.Join(proc, "102", "status"), "PPid:\t101\n")
	id := strings.Repeat("af", 16)
	core := "/etc/hyper/mihomo-kernel/slots/" + id + "/mihomo-linux-arm64/mihomo"
	cfg := "/etc/hyper/mihomo-kernel/configs/" + id + ".yaml"
	args := []byte(core + "\x00-d\x00/var/run/hyper/mihomo\x00-f\x00" + cfg + "\x00")
	if !supervisedCore(proc, "102", core, args, manager) {
		t.Fatal("supervised core lost")
	}
	if supervisedCore(proc, "103", core, args, manager) || supervisedCore(proc, "102", core, bytes.ReplaceAll(args, []byte(cfg), []byte("/tmp/other.json")), manager) {
		t.Fatal("accepted foreign launch")
	}
	write(filepath.Join(proc, "100", "cmdline"), manager+"\x00guard\x00")
	if supervisedCore(proc, "102", core, args, manager) {
		t.Fatal("wrong supervisor role")
	}
	o := networkOwnership(t.TempDir())
	dir := filepath.Join(proc, "102")
	link("socket:[1234]", filepath.Join(dir, "fd", "3"))
	link("/dev/net/tun", filepath.Join(dir, "fd", "4"))
	write(filepath.Join(dir, "fdinfo", "4"), "iff:\tother0\n")
	o.collectDescriptors(dir)
	if o.tun || !o.sockets["1234"] {
		t.Fatal("mistook another TUN device", o)
	}
	write(filepath.Join(dir, "fdinfo", "4"), "iff:\towtun0\n")
	o.collectDescriptors(dir)
	if !o.tun {
		t.Fatal("owned TUN fd unrecognized")
	}
	write(filepath.Join(proc, "net", "tcp"), "1: 0100007F:4D31 00:0 0A 0:0 0:0 0 1000 0 1234\n")
	write(filepath.Join(proc, "net", "udp"), "")
	c := collector{map[Resource]bool{}, o}
	c.ports(proc)
	if len(c.seen) != 0 {
		t.Fatal(c.sorted())
	}
	write(filepath.Join(proc, "net", "tcp"), "1: 0100007F:4D31 00:0 0A 0:0 0:0 0 1000 0 1234\n2: 0200007F:4D31 00:0 0A 0:0 0:0 0 1000 0 5678\n")
	c.ports(proc)
	if !c.seen[Resource{"listener", "tcp/19761", ""}] {
		t.Fatal("concurrent listener was ignored", c.sorted())
	}
}

func TestFirewallOwnershipChecksForeignRuleInsideOwnChain(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "hold"), 0700); err != nil {
		t.Fatal(err)
	}
	holdFile := filepath.Join(root, "hold", "fw3.4.active")
	snapshot := "-N OW_HOLD\n-A OW_HOLD -i br-lan -p tcp -m comment --comment hyper-fault-v1 -j DROP\n"
	if err := os.WriteFile(holdFile, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	c := collector{seen: map[Resource]bool{}, own: networkOwnership(root)}
	save := "*mangle\n:OW_HOLD - [0:0]\n-A OW_HOLD -i br-lan -p tcp -m comment --comment \"hyper-fault-v1\" -j DROP\n"
	c.xt("iptables-save", []byte(save))
	if len(c.seen) != 0 {
		t.Fatal(c.sorted())
	}
	c.xt("iptables-save", []byte(save+"-A OW_HOLD -i br-guest -p tcp -m comment --comment hyper-fault-v1 -j DROP\n"))
	if !c.seen[Resource{"xtables", "iptables-save OW_HOLD (unknown owner)", ""}] {
		t.Fatal("changed rule ignored", c.sorted())
	}
	c.seen = map[Resource]bool{}
	c.xt("ip6tables-save", []byte(save))
	if len(c.seen) == 0 {
		t.Fatal("wrong address family")
	}
	c.seen = map[Resource]bool{}
	c.nft([]byte(`{"nftables":[{"table":{"name":"hyper","family":"inet","comment":"hyper-owned-v1"}},{"table":{"name":"hyper_hold","family":"inet","comment":"hyper-fault-v1"}}]}`))
	if len(c.seen) != 2 {
		t.Fatal("owner comment without matching journal", c.sorted())
	}
}

func TestTUNAndHoldRoutesNeedTheirIndependentOwners(t *testing.T) {
	o := ownership{routes: map[string]string{"-4": "tun", "-6": "tun"}}
	for _, test := range []struct{ family, iface, fallback string }{
		{"-4", "default dev owtun0 table 21998 proto 199 scope 253", "8 default table 21998 proto 199 metric 32760"},
		{"-6", "default dev owtun0 table 21998 proto 199 metric 1024 pref medium", "8 default dev lo table 21998 proto 199 metric 32760 pref medium"},
	} {
		for _, tun := range []bool{false, true} {
			for _, guarded := range []bool{false, true} {
				o.tun, o.guarded = tun, guarded
				if o.reservedRoute(test.family, strings.Fields(test.iface)) != tun || o.reservedRoute(test.family, strings.Fields(test.fallback)) != guarded {
					t.Fatal(test, tun, guarded)
				}
			}
		}
	}
	rule := "21998: from all fwmark 0x4f57/0xffff lookup 21998 proto 199\n"
	c := collector{seen: map[Resource]bool{}, own: o}
	c.routes("-4", []byte(rule+rule))
	if len(c.seen) != 1 {
		t.Fatal("duplicate RPDB entry was ignored")
	}
}

func TestOwnDNSVariants(t *testing.T) {
	for _, s := range []string{dnsInjection, dnsInjection + "filter-AAAA\n", "# Hyper managed upstream\nfilter-AAAA\n"} {
		if !ownDNS([]byte(s)) {
			t.Fatalf("own file rejected: %q", s)
		}
	}
	for _, s := range []string{"no-resolv\n", dnsInjection + "server=127.0.0.1#80\n", "# Hyper managed upstream\nfilter-aaaa\n"} {
		if ownDNS([]byte(s)) {
			t.Fatalf("foreign file accepted: %q", s)
		}
	}
}
