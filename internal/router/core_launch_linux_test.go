//go:build linux

package router

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/coregroup"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
)

func TestCoreKeepsRootInItsGroupAlone(t *testing.T) {
	a := coreCommand(kernel.Launch{Path: "/core", Config: "/core.yaml"}, "/work", 4242).SysProcAttr
	if a == nil || a.Credential == nil || a.Pdeathsig != syscall.SIGTERM || !a.Setpgid || a.Setsid {
		t.Fatal(a)
	}
	// A non-nil empty list makes setgroups(0) run, so the core inherits no group of the manager.
	if c := a.Credential; c.Uid != 0 || c.Gid != 4242 || c.Groups == nil || len(c.Groups) != 0 || c.NoSetGroups {
		t.Fatal(c)
	}
	// Nothing narrows what root holds: no namespace, root directory or capability list.
	if a.Cloneflags != 0 || a.Unshareflags != 0 || a.Chroot != "" || a.AmbientCaps != nil || a.UidMappings != nil || a.GidMappings != nil {
		t.Fatal(a)
	}
	if coreFiles != 1000000 {
		t.Fatal(coreFiles)
	}
}

// procStatus reads the "Name: value" fields of /proc/<pid>/status.
func procStatus(b []byte) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok {
			fields[name] = strings.Join(strings.Fields(value), " ")
		}
	}
	return fields
}

// openFiles reads the soft and hard limit from /proc/<pid>/limits.
func openFiles(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Max open files"); ok {
			if f := strings.Fields(rest); len(f) >= 2 {
				return f[0] + " " + f[1]
			}
		}
	}
	return ""
}

// Go puts its original soft limit back in each child unless the program set the limit.
func TestSetFileLimitReachesChildren(t *testing.T) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
	hard := strconv.FormatUint(lim.Max, 10)
	child := func() string {
		out, err := exec.Command("/bin/cat", "/proc/self/limits").Output()
		if err != nil {
			t.Fatal(err)
		}
		return openFiles(out)
	}
	// Without privilege the hard limit can only fall, and stays fallen: keep it.
	if err := setFileLimit(lim.Max); err != nil {
		t.Fatal(err)
	}
	if got := child(); got != hard+" "+hard {
		t.Fatal(got, hard)
	}
	if os.Geteuid() != 0 {
		// Refused: children still get the soft limit raised to the hard one.
		if err := setFileLimit(lim.Max + 1); !errors.Is(err, syscall.EPERM) {
			t.Fatal(err)
		}
		if got := child(); got != hard+" "+hard {
			t.Fatal(got, hard)
		}
		return
	}
	// Root may hold up to fs.nr_open but no more: asked for more, children get that ceiling.
	b, err := os.ReadFile("/proc/sys/fs/nr_open")
	if err != nil {
		t.Fatal(err)
	}
	nrOpen, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// Later tests start processes of their own: hand them the limit this test began with.
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim) })
	if setFileLimit(nrOpen) != nil {
		t.Skip("this root cannot raise its hard limit (no CAP_SYS_RESOURCE)")
	}
	// Start from procd's default hard limit, which it leaves when its own setrlimit fails.
	if err := setFileLimit(min(nrOpen, 4096)); err != nil {
		t.Fatal(err)
	}
	// The ceiling is the most anyone may hold, so reaching it is no failure.
	if err := setFileLimit(nrOpen + 1); err != nil {
		t.Fatal(err)
	}
	if got, want := child(), strings.TrimSpace(string(b)); got != want+" "+want {
		t.Fatal(got, want)
	}
}

func TestFileLimitFallbacks(t *testing.T) {
	for _, c := range []struct {
		name   string
		nrOpen string
		hard   uint64
		want   []uint64
	}{
		// fs.nr_open below n: root can still set both limits to it, before keeping procd's 4096.
		{"nr_open below n", "524288\n", 4096, []uint64{524288, 4096}},
		{"nr_open at hard", "65536\n", 65536, []uint64{65536}},
		// n was refused for another reason (no CAP_SYS_RESOURCE): retrying it is pointless.
		{"nr_open above n", "1048576\n", 4096, []uint64{4096}},
		{"nr_open equal to n", "1000000\n", 4096, []uint64{4096}},
		{"nr_open unreadable", "", 4096, []uint64{4096}},
		{"nr_open malformed", "lots\n", 4096, []uint64{4096}},
		{"nr_open zero", "0\n", 4096, []uint64{4096}},
		// A hard limit of 0 would leave the core no descriptor; one at n or above was refused.
		{"hard unknown", "524288\n", 0, []uint64{524288}},
		{"hard above n", "", 2000000, nil},
		{"nothing to try", "", 0, nil},
	} {
		got := fileLimitFallbacks(coreFiles, []byte(c.nrOpen), c.hard)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// As root, start a shell the way the core is started and read what it holds. The fixture
// scripts/kernel/check-credentials.sh runs this on OpenWrt's own userland.
func TestCoreLaunchHoldsEveryCapability(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can start the core as root in another group")
	}
	gid, err := coregroup.GID()
	if err != nil {
		t.Fatal(err)
	}
	fixture := os.Getenv("HYPER_CORE_FIXTURE") == "1"
	// A container's root may lack CAP_SYS_RESOURCE; procd's root on a router does not.
	limited := setFileLimit(coreFiles) == nil
	if !limited && fixture {
		t.Fatal("cannot set the core's open-file limit")
	}
	dir := t.TempDir()
	cmd := coreCommand(kernel.Launch{Path: "/bin/sh", Config: "/dev/null"}, dir, gid)
	cmd.Args = []string{"sh", "-c", "exec cat /proc/self/status /proc/self/limits"}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	child := procStatus(out.Bytes())
	self, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	parent := procStatus(self)
	g := strconv.Itoa(gid)
	if child["Uid"] != "0 0 0 0" || child["Gid"] != strings.Repeat(g+" ", 3)+g || child["Groups"] != "" {
		t.Fatal(child["Uid"], child["Gid"], child["Groups"])
	}
	// Root's execve fills the permitted and effective sets from the bounding set, which the
	// launch leaves as the manager's; no_new_privs stays as the manager has it (unset on a router).
	bounding := child["CapBnd"]
	if bounding == "" || bounding != parent["CapBnd"] || child["CapPrm"] != bounding || child["CapEff"] != bounding {
		t.Fatal(child["CapBnd"], child["CapPrm"], child["CapEff"], parent["CapBnd"])
	}
	if child["NoNewPrivs"] != parent["NoNewPrivs"] {
		t.Fatal(child["NoNewPrivs"], parent["NoNewPrivs"])
	}
	if fixture && child["NoNewPrivs"] != "0" {
		t.Fatal("no_new_privs set", child["NoNewPrivs"])
	}
	if got := openFiles(out.Bytes()); limited && got != "1000000 1000000" {
		t.Fatal("open files", got)
	}
}
