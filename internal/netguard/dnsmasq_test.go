package netguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDNSTakeoverUsesEffectiveInstanceConfiguration(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "dnsmasq.conf.lan")
	dir := filepath.Join(base, "lan")
	nested := filepath.Join(base, "nested")
	for _, path := range []string{dir, nested} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, text string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rootConfig := "interface=br-lan\nconf-dir=" + dir + ",*.conf\n"
	write(root, rootConfig)
	write(filepath.Join(dir, "ignored.disabled"), "server=192.0.2.1\n")
	write(filepath.Join(dir, ".backup.conf"), "server=192.0.2.1\n")
	write(filepath.Join(dir, "nested.conf"), "conf-file="+filepath.Join(nested, "extra")+"\n")
	write(filepath.Join(base, "dnsmasq.conf.guest"), "interface=guest\nserver=192.0.2.1\n")
	check := func(expect string, own ownership) {
		t.Helper()
		var output bytes.Buffer
		err := dnsDirectories(filepath.Join(base, "dnsmasq.conf.*"), own, []string{"br-lan"}, &output)
		if (err != nil) != strings.HasPrefix(expect, "missing ") || strings.TrimSpace(output.String()) != expect {
			t.Fatalf("result %q, error %v, want %q", output.String(), err, expect)
		}
	}
	write(filepath.Join(nested, "extra"), "server=/corp.example/10.0.0.53\nserver=/local.example/#\n")
	check(dir, ownership{})
	for _, test := range []struct{ line, issue string }{
		{"server=10.0.0.53", "dns_default_upstream"}, {"server=/#/10.0.0.53", "dns_default_upstream"},
		{"address=/#/127.0.0.1", "dns_default_upstream"},
		{"servers-file=/tmp/private", "dns_servers_file"}, {"conf-script=/bin/false", "dns_dynamic_config"},
		{"conf-file=/nonexistent/test/location", "dns_config_unreadable"},
	} {
		write(filepath.Join(nested, "extra"), test.line+"\n")
		check("missing "+test.issue+" lan", ownership{})
	}
	write(filepath.Join(nested, "extra"), "")
	write(root, rootConfig+"interface=guest\n")
	check("missing dns_shared_instance lan", ownership{})
	write(root, rootConfig)
	write(filepath.Join(base, "dnsmasq.conf.guest"), "interface=guest\nconf-dir="+dir+"\n")
	check("missing dns_include_directory lan", ownership{})
	write(filepath.Join(base, "dnsmasq.conf.guest"), "port=0\nconf-dir="+dir+"\n")
	write(filepath.Join(dir, upstreamFile), dnsInjection)
	check("missing dns_file_collision lan", ownership{})
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	check(dir, ownership{dnsDirs: map[string]bool{real: true}})
}

func TestDNSTakeoverSetsAsideOnlyLuCIForwardings(t *testing.T) {
	// The network helper maps only the init script's default include directories back to a section.
	dir, err := os.MkdirTemp("/tmp", "dnsmasq.*.d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(dir), "dnsmasq."), ".d")
	base := t.TempDir()
	custom := filepath.Join(base, "custom")
	if err := os.Mkdir(custom, 0700); err != nil {
		t.Fatal(err)
	}
	check := func(main, include, want string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(base, "dnsmasq.conf."+id), []byte("interface=br-lan\n"+main+"conf-dir="+include+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err := dnsDirectories(filepath.Join(base, "dnsmasq.conf.*"), ownership{}, []string{"br-lan"}, &output)
		if (err != nil) != strings.HasPrefix(want, "missing ") || strings.TrimSpace(output.String()) != want {
			t.Fatalf("%q in %s: result %q, error %v, want %q", main, include, output.String(), err, want)
		}
	}
	forwardings := "server=223.5.5.5\nserver=127.0.0.1#5335\nserver=/#/192.0.2.1\nserver=/lan/192.168.1.1\n"
	check(forwardings, dir, dir)
	conflict := "missing dns_default_upstream " + id
	// The takeover moves only the server list; these stay and would answer instead of the core.
	check("address=/#/192.0.2.1\n", dir, conflict)
	check("local=/#/\n", dir, conflict)
	// Forwardings beside a custom include directory would stay beside the core.
	check(forwardings, custom, conflict)
	check("server=/lan/192.168.1.1\n", custom, custom)
	// Another plugin's default upstream cannot be set aside.
	if err := os.WriteFile(filepath.Join(dir, "plugin.conf"), []byte("server=127.0.0.1#5335\n"), 0600); err != nil {
		t.Fatal(err)
	}
	check("", dir, conflict)
}

func TestForwardingsMovableOnlyFromADirectoryThatNamesTheSection(t *testing.T) {
	for _, test := range []struct {
		dir, id   string
		instances int
		want      bool
	}{
		{"/tmp/dnsmasq.cfg01411c.d", "cfg01411c", 2, true},
		{"/tmp/dnsmasq.d", "cfg01411c", 1, true},
		// Shared by several instances, it names none of them.
		{"/tmp/dnsmasq.d", "cfg01411c", 2, false},
		{"/tmp/dnsmasq.guest.d", "cfg01411c", 1, false},
		{"/tmp/custom", "cfg01411c", 1, false},
		// Not a UCI section name.
		{"/tmp/dnsmasq.lan-2.d", "lan-2", 1, false},
		{"/tmp/dnsmasq.d", "lan-2", 1, false},
	} {
		if got := forwardingsMovable(test.dir, test.id, test.instances); got != test.want {
			t.Errorf("%s for %s among %d: %v, want %v", test.dir, test.id, test.instances, got, test.want)
		}
	}
}
