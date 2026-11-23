// Package qualification holds host-side tests of the router shell hooks.
package qualification

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// Backend selection must reflect the live kernel rules and the actual xtables ABI.
func inspectFirewall(t *testing.T, nft, xtVersion, xtActive string) string {
	t.Helper()
	tools := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("nft", "#!/bin/sh\nif [ \"$1\" = --version ]; then echo \"nftables v1.1.1\"; else test \"$NFT_ACTIVE\" = yes; fi\n")
	write("iptables", "#!/bin/sh\nif [ \"$1\" = -V ]; then echo \"iptables v1.8.10 ($XT_VERSION)\"; "+
		"elif [ \"$XT_ACTIVE\" = yes ]; then echo '-A INPUT -m comment --comment \"!fw3\" -j ACCEPT'; fi\n")
	// The fixture excludes any host firewall executable.
	for _, name := range []string{"awk", "grep"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip(name, "unavailable")
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	network, _ := filepath.Abs(filepath.Join(root, "files/usr/libexec/hyper/network"))
	cmd := exec.Command(network, "environment")
	cmd.Env = append(os.Environ(), "PATH="+tools, "NFT_ACTIVE="+nft, "XT_VERSION="+xtVersion, "XT_ACTIVE="+xtActive)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestFirewallSelection(t *testing.T) {
	for _, c := range []struct{ nft, version, active, want string }{
		// xtables-nft is not a second firewall.
		{"yes", "nf_tables", "yes", "fw4 nftables v1.1.1"},
		{"no", "nf_tables", "yes", "unavailable"},
		// Installed but inactive is not a firewall.
		{"no", "legacy", "no", "unavailable"},
		{"yes", "legacy", "no", "fw4 nftables v1.1.1"},
		// Legacy and mixed.
		{"no", "legacy", "yes", "fw3 iptables-legacy v1.8.10"},
		{"yes", "legacy", "yes", "ambiguous"},
	} {
		if got := inspectFirewall(t, c.nft, c.version, c.active); got != c.want {
			t.Errorf("nft=%s xtables=%s/%s: %q, want %q", c.nft, c.version, c.active, got, c.want)
		}
	}
}

// A stateful stand-in for xtables-legacy-multi: every table is a file of the rules restore
// committed, kept verbatim, so the helper's own snapshots decide what it verifies and removes.
const fakeXtables = `#!/bin/sh
name=${0##*/}
dir=$XT_STATE/${name%%-*}
mkdir -p "$dir"
if [ "$1" = -V ]; then echo "${name%%-*} v1.8.10 (legacy)"; exit 0; fi
case "$name" in *-restore)
 if [ "$1" = --test ]; then cat >/dev/null; exit 0; fi
 exec awk -v dir="$dir" -f "${0%/*}/restore.awk" ;;
esac
table=filter chain=
while [ $# -gt 0 ]; do
 case "$1" in -w) shift ;; -t) table=$2; shift ;; -S) ;; *) chain=$1 ;; esac
 shift
done
touch "$dir/$table"
if [ -z "$chain" ]; then cat "$dir/$table"; exit 0; fi
case "$chain" in INPUT|FORWARD|OUTPUT|PREROUTING|POSTROUTING) ;;
 *) grep -qx -- "-N $chain" "$dir/$table" || exit 1; echo "-N $chain" ;; esac
grep -- "^-A $chain " "$dir/$table" || :
`

const fakeRestore = `
function load(t,  line, f) { n = 0; f = dir "/" t; while ((getline line < f) > 0) r[++n] = line; close(f) }
function save(t,  i, f) { f = dir "/" t; printf "" > f; for (i = 1; i <= n; i++) if (r[i] != "") print r[i] > f; close(f) }
function find(want,  i) { for (i = 1; i <= n; i++) if (r[i] == want) return i; return 0 }
{ $1 = $1 }
NF == 0 { next }
/^\*/ { table = substr($0, 2); load(table); next }
$0 == "COMMIT" { save(table); next }
$1 == "-N" { if (find($0)) exit 1; r[++n] = $0; next }
$1 == "-X" { i = find("-N " $2); if (!i) exit 1; r[i] = ""; next }
$1 == "-A" { r[++n] = $0; next }
$1 == "-D" { i = find("-A" substr($0, 3)); if (!i) exit 1; r[i] = ""; next }
$1 == "-I" {
 pos = 1; rule = $0
 if ($3 ~ /^[0-9]+$/) { pos = $3; sub(/^-I [^ ]+ [0-9]+/, "-I " $2, rule) }
 sub(/^-I/, "-A", rule)
 at = n + 1; k = 0
 for (i = 1; i <= n; i++) if (index(r[i], "-A " $2 " ") == 1 && ++k == pos) { at = i; break }
 for (i = n; i >= at; i--) r[i + 1] = r[i]
 r[at] = rule; n++
 next
}
{ exit 1 }
`

// Takes the fw3 path over and back with the helper's own functions, deleting each family's
// listener guard once on the way: verify must notice, and release must leave no owned rule.
const legacyCycle = `set -eu
. "$BACKEND"
state=$STATE note=hyper-owned-v1 port=19762 mark=0x4f57 interfaces=br-lan
method=$METHOD v6=$V6 legacy_families=$FAMILIES
legacy_prepare
legacy_apply
legacy_verify
for fam in $legacy_families; do
 xt=$(legacy_tool "$fam")
 echo "applied $fam"; "$xt" -S INPUT
 guard=$("$xt" -S INPUT | grep -F -e '--dport 19762' | head -n 1)
 [ -n "$guard" ] || continue
 printf '*filter\n-D%s\nCOMMIT\n' "${guard#-A}" | "$xt-restore" --noflush
 if legacy_verify; then echo "verify missed the listener guard deleted from $xt" >&2; exit 1; fi
 printf '*filter\n-I%s\nCOMMIT\n' "${guard#-A}" | "$xt-restore" --noflush
 legacy_verify
done
for fam in $legacy_families; do
 legacy_remove "$fam"
 [ ! -e "$state/fw3.$fam" ] && [ ! -e "$state/legacy.$fam.active" ]
 xt=$(legacy_tool "$fam")
 echo "released $fam"; "$xt" -S INPUT
done
`

// On fw3 only packets the TProxy takeover marked reach the core's listener, as fw4's
// listener_guard chain has it: the guard heads INPUT in each family the takeover covers, and
// it is verified and removed with the takeover's other rules.
func TestLegacyListenerGuard(t *testing.T) {
	backend, _ := filepath.Abs(filepath.Join(root, "files/usr/libexec/hyper/firewall-backend"))
	const (
		fw3Loopback = `-A INPUT -i lo -m comment --comment "!fw3" -j ACCEPT`
		fw3Zone     = `-A INPUT -i br-lan -m comment --comment "!fw3" -j zone_lan_input`
		allowance   = "-A INPUT -i br-lan -m mark --mark 0x4f57/0xffff -m comment --comment hyper-owned-v1 -j ACCEPT"
	)
	guard := func(proto string) string {
		return "-A INPUT ! -i lo -p " + proto + " --dport 19762 -m mark ! --mark 0x4f57/0xffff -m comment --comment hyper-owned-v1 -j REJECT"
	}
	guarded := []string{guard("udp"), guard("tcp"), allowance, fw3Loopback, fw3Zone}
	untouched := []string{fw3Loopback, fw3Zone}
	for _, c := range []struct {
		method, v6, families string
		want                 map[string][]string
	}{
		{"tproxy", "1", "4 6", map[string][]string{"applied 4": guarded, "applied 6": guarded}},
		// Without the IPv6 takeover the core listens on IPv4 alone; an IPv6 Telegram
		// allowance still brings the IPv6 filter table in.
		{"tproxy", "0", "4 6", map[string][]string{"applied 4": guarded, "applied 6": untouched}},
		// A TUN core has no listener; its allowances are in FORWARD.
		{"tun", "1", "4 6", map[string][]string{"applied 4": untouched, "applied 6": untouched}},
	} {
		t.Run(c.method+"/ipv6="+c.v6, func(t *testing.T) {
			tools, work := t.TempDir(), t.TempDir()
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"iptables-legacy", "iptables-legacy-restore", "ip6tables-legacy", "ip6tables-legacy-restore"} {
				write(name, fakeXtables)
			}
			write("restore.awk", fakeRestore)
			// No attached networks: local_prefixes reads routes through ip.
			write("ip", "#!/bin/sh\n")
			for _, family := range []string{"iptables", "ip6tables"} {
				if err := os.MkdirAll(filepath.Join(work, "xt", family), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, "xt", family, "filter"), []byte(fw3Loopback+"\n"+fw3Zone+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(work, "state"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", legacyCycle)
			cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
				"XT_STATE="+filepath.Join(work, "xt"), "BACKEND="+backend, "STATE="+filepath.Join(work, "state"),
				"METHOD="+c.method, "V6="+c.v6, "FAMILIES="+c.families)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v\n%s%s", err, out, stderr.String())
			}
			got := map[string][]string{}
			section := ""
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, "applied ") || strings.HasPrefix(line, "released ") {
					section = line
					got[section] = []string{}
					continue
				}
				got[section] = append(got[section], line)
			}
			c.want["released 4"], c.want["released 6"] = untouched, untouched
			for section, want := range c.want {
				if strings.Join(got[section], "\n") != strings.Join(want, "\n") {
					t.Errorf("%s INPUT:\n%s\nwant:\n%s", section, strings.Join(got[section], "\n"), strings.Join(want, "\n"))
				}
			}
		})
	}
}

// Offline-image and failure recovery coverage for the native package hook.

func elf(tail string, machine ...byte) []byte {
	if machine == nil {
		machine = []byte{0xb7, 0x00}
	}
	b := append([]byte("\x7fELF\x02\x01"), make([]byte, 12)...)
	b = append(b, machine...)
	b = append(b, make([]byte, 44)...)
	return append(b, tail...)
}

func entry(content []byte, name string) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func gz(t *testing.T, data []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type activation struct {
	t                      *testing.T
	root, runtime, payload string
	manager                string
	binary                 []byte
	env                    []string
}

func newActivation(t *testing.T) *activation {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("the flock command is unavailable")
	}
	a := &activation{t: t, root: t.TempDir()}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(a.root, "bin"), 0o755))
	must(os.WriteFile(filepath.Join(a.root, "bin/busybox"), elf("fixture"), 0o644))
	a.env = append(os.Environ(), "IPKG_INSTROOT="+a.root)
	a.runtime = filepath.Join(a.root, "usr/libexec/hyper")
	must(os.MkdirAll(a.runtime, 0o755))
	abi, err := os.ReadFile(filepath.Join(root, "packaging/openwrt/runtime-abi"))
	must(err)
	must(os.WriteFile(filepath.Join(a.runtime, "runtime-abi"), abi, 0o755))
	must(os.MkdirAll(filepath.Join(a.root, "etc"), 0o755))
	must(os.WriteFile(filepath.Join(a.root, "etc/openwrt_release"), []byte("DISTRIB_ARCH='aarch64_generic'\n"), 0o644))
	a.payload = filepath.Join(a.root, "tmp/hyper-package-payload")
	must(os.MkdirAll(a.payload, 0o700))
	must(os.Chmod(a.payload, 0o700))
	a.manager = filepath.Join(a.runtime, "manager")
	a.binary = elf("new manager")
	a.stage(a.binary)
	return a
}

func (a *activation) write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		a.t.Fatal(err)
	}
}

func (a *activation) read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		a.t.Fatal(err)
	}
	return string(data)
}

func (a *activation) stage(binary []byte) {
	compressed := gz(a.t, binary)
	foreign := gz(a.t, elf("foreign CPU", 0x3e, 0x00))
	a.write(filepath.Join(a.payload, "arm64.gz"), compressed)
	a.write(filepath.Join(a.payload, "amd64.gz"), foreign)
	a.write(filepath.Join(a.payload, "SHA256SUMS"), []byte(entry(compressed, "arm64.gz")+entry(foreign, "amd64.gz")))
	a.write(filepath.Join(a.runtime, "runtime.sha256"), []byte(entry(binary, "arm64")))
}

func (a *activation) activate(success bool) string {
	a.t.Helper()
	script, _ := filepath.Abs(filepath.Join(root, "packaging/openwrt/activate-runtime"))
	cmd := exec.Command("sh", script)
	cmd.Env = a.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if (err == nil) != success {
		a.t.Fatalf("activate-runtime: %v\n%s", err, stderr.String())
	}
	return stderr.String()
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestOfflineSelectsCPUAndReconfiguresAfterPayloadCleanup(t *testing.T) {
	a := newActivation(t)
	a.activate(true)
	if a.read(a.manager) != string(a.binary) {
		t.Fatal("wrong manager")
	}
	if info, _ := os.Stat(a.manager); info.Mode().Perm() != 0o755 {
		t.Fatal(info.Mode())
	}
	if exists(a.payload) {
		t.Fatal("payload kept")
	}
	a.activate(true)
}

func TestBadArchiveAndWrongCPUPreservePreviousManager(t *testing.T) {
	a := newActivation(t)
	old := elf("old manager")
	a.write(a.manager, old)
	a.write(filepath.Join(a.payload, "arm64.gz"), []byte("incomplete"))
	if !strings.Contains(a.activate(false), "archive checksum mismatch") || a.read(a.manager) != string(old) {
		t.Fatal("bad archive accepted")
	}
	a.stage(elf("wrong CPU", 0x3e, 0x00))
	if !strings.Contains(a.activate(false), "architecture mismatch") || a.read(a.manager) != string(old) {
		t.Fatal("wrong CPU accepted")
	}
}

func TestFirstBootKernelRequestIsPrivateAndConsumedOnce(t *testing.T) {
	a := newActivation(t)
	a.activate(true)
	dir := filepath.Join(a.root, "etc/hyper/mihomo-kernel")
	pending := filepath.Join(dir, ".first-install-pending")
	info, err := os.Stat(pending)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	if err := os.Rename(pending, filepath.Join(dir, ".first-install-attempted")); err != nil {
		t.Fatal(err)
	}
	a.activate(true)
	if exists(pending) {
		t.Fatal("request repeated")
	}
}

func TestInterruptedVerifiedCandidateRecoversOffline(t *testing.T) {
	a := newActivation(t)
	os.RemoveAll(a.payload)
	a.write(a.manager, elf("old manager"))
	if err := os.Link(a.manager, filepath.Join(a.runtime, ".manager.previous")); err != nil {
		t.Fatal(err)
	}
	a.write(filepath.Join(a.runtime, ".manager.next"), a.binary)
	a.activate(true)
	if a.read(a.manager) != string(a.binary) || exists(filepath.Join(a.runtime, ".manager.previous")) ||
		exists(filepath.Join(a.runtime, ".manager.next")) {
		t.Fatal("candidate not recovered")
	}
}

func TestInterruptedPartialWriteKeepsOldManagerWithBoundedStorage(t *testing.T) {
	a := newActivation(t)
	os.RemoveAll(a.payload)
	old := elf("old manager")
	for range 2 {
		a.write(filepath.Join(a.runtime, ".manager.previous"), old)
		a.write(a.manager, []byte("damaged replacement"))
		a.write(filepath.Join(a.runtime, ".manager.next"), []byte("partial"))
		a.activate(false)
		if a.read(a.manager) != string(old) || exists(filepath.Join(a.runtime, ".manager.previous")) ||
			exists(filepath.Join(a.runtime, ".manager.next")) {
			t.Fatal("old manager not restored")
		}
	}
}

func TestUntrustedStageOrCandidateLinkIsRejectedBeforeWriting(t *testing.T) {
	a := newActivation(t)
	os.Chmod(a.payload, 0o777)
	if !strings.Contains(a.activate(false), "unsafe package payload directory") {
		t.Fatal("unsafe payload accepted")
	}
	os.Chmod(a.payload, 0o700)
	victim := filepath.Join(a.root, "unrelated")
	a.write(victim, []byte("preserve"))
	if err := os.Symlink(victim, filepath.Join(a.runtime, ".manager.next")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.activate(false), "invalid installer state") || a.read(victim) != "preserve" {
		t.Fatal("candidate link followed")
	}
}
