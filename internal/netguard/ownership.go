// Ownership evidence for resources reserved by the network and core launchers.
package netguard

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

type ownership struct {
	sockets   map[string]bool
	tun       bool
	routes    map[string]string
	dnsDirs   map[string]bool
	holdNft   bool
	activeNft bool
	guarded   bool
	xtRules   map[string]map[string]int
}

func networkOwnership(dir string) ownership {
	o := ownership{sockets: map[string]bool{}, routes: map[string]string{}, dnsDirs: map[string]bool{}, xtRules: map[string]map[string]int{}}
	if _, err := os.Stat(filepath.Join(dir, "hold", "fw4")); err == nil {
		o.holdNft = true
	}
	for _, f := range []string{"4", "6"} {
		o.xtRules[f] = map[string]int{}
		readXTSnapshot(o.xtRules[f], filepath.Join(dir, "hold", "fw3."+f+".active"), "mangle")
	}
	if _, err := os.Stat(filepath.Join(dir, "owned")); err != nil {
		return o
	}
	_, nftErr := os.Stat(filepath.Join(dir, "fw4"))
	o.activeNft = nftErr == nil
	for _, family := range []string{"4", "6"} {
		readXTSnapshot(o.xtRules[family], filepath.Join(dir, "legacy."+family+".active"), "")
		b, _ := os.ReadFile(filepath.Join(dir, "route"+family))
		method := strings.TrimSpace(string(b))
		if method == "tun" || method == "tproxy" {
			o.routes["-"+family] = method
		}
	}
	_, guardErr := os.Stat(filepath.Join(dir, "armed"))
	o.guarded = guardErr == nil
	b, _ := os.ReadFile(filepath.Join(dir, "dns"))
	for _, d := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !filepath.IsAbs(d) {
			continue
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			o.dnsDirs[real] = true
		}
	}
	return o
}

// The network script writes this header, then only these lines depending on the DNS and IPv6 settings.
var dnsLines = map[string]bool{"no-resolv": true, "server=127.0.0.1#19764": true, "server=/probe.hyper.invalid/127.0.0.1#19764": true,
	"rebind-domain-ok=/probe.hyper.invalid/": true, "filter-AAAA": true}

func ownDNS(content []byte) bool {
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if lines[0] != "# Hyper managed upstream" {
		return false
	}
	for _, l := range lines[1:] {
		if !dnsLines[l] {
			return false
		}
	}
	return true
}

func (o ownership) dnsFile(path string, content []byte) bool {
	if filepath.Base(path) != "99-hyper-upstream.conf" || !ownDNS(content) {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	return err == nil && o.dnsDirs[dir]
}

func launchID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func parentPID(path string) string {
	b, _ := os.ReadFile(filepath.Join(path, "status"))
	for _, line := range strings.Split(string(b), "\n") {
		if s, ok := strings.CutPrefix(line, "PPid:"); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// The signed slot alone is insufficient: the same binary may be run manually.
// Require the exact core invocation and its manager's executable inode/role.
func supervisedCore(proc, pid, path string, argv []byte, manager string) bool {
	args := strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")
	if len(args) != 5 || args[0] != path || args[1] != "-d" || args[2] != "/var/run/hyper/mihomo" || args[3] != "-f" ||
		filepath.Dir(args[4]) != "/etc/hyper/mihomo-kernel/configs" || !strings.HasSuffix(args[4], ".yaml") ||
		!launchID(strings.TrimSuffix(filepath.Base(args[4]), ".yaml")) {
		return false
	}
	parts := strings.SplitN(path, "/mihomo-kernel/slots/", 2)
	if len(parts) != 2 || (parts[0] != "/etc/hyper" && parts[0] != "/tmp/hyper") {
		return false
	}
	slot, _, ok := strings.Cut(parts[1], "/")
	if !ok || !launchID(slot) || filepath.Base(path) != "mihomo" {
		return false
	}
	expected, err := os.Stat(manager)
	if err != nil {
		return false
	}
	for range 4 {
		pid = parentPID(filepath.Join(proc, pid))
		if pid == "" || pid == "1" || pid == "0" {
			break
		}
		dir := filepath.Join(proc, pid)
		info, err := os.Stat(filepath.Join(dir, "exe"))
		if err != nil || !os.SameFile(expected, info) {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		return bytes.Equal(b, []byte(manager+"\x00serve\x00"))
	}
	return false
}

func (o *ownership) collectDescriptors(pidDir string) {
	entries, _ := os.ReadDir(filepath.Join(pidDir, "fd"))
	for _, fd := range entries {
		path := filepath.Join(pidDir, "fd", fd.Name())
		dest, err := os.Readlink(path)
		if err != nil {
			continue
		}
		if inode, ok := strings.CutPrefix(dest, "socket:["); ok && strings.HasSuffix(inode, "]") {
			o.sockets[strings.TrimSuffix(inode, "]")] = true
		}
		if dest != "/dev/net/tun" {
			continue
		}
		info, _ := os.ReadFile(filepath.Join(pidDir, "fdinfo", fd.Name()))
		for _, line := range strings.Split(string(info), "\n") {
			if dev, ok := strings.CutPrefix(line, "iff:"); ok && strings.TrimSpace(dev) == "owtun0" {
				o.tun = true
			}
		}
	}
}

// Match exactly the preference, mask, table, protocol and nexthop installed by
// the helper. Extra selectors (iif, source, via, etc.) are independent conflicts.
func (o ownership) reservedRoute(family string, fields []string) bool {
	method := o.routes[family]
	if method == "" {
		return false
	}
	line := strings.Join(fields, " ")
	if fields[0] == "21998:" {
		return line == "21998: from all fwmark 0x4f57/0xffff lookup 21998 proto 199"
	}
	// ip -N renders RTN_LOCAL/RTN_PROHIBIT and scopes as numeric values.
	switch line {
	case "2 default dev lo table 21998 proto 199 scope 254":
		return method == "tproxy" && family == "-4"
	case "2 default dev lo table 21998 proto 199 metric 1024 pref medium":
		return method == "tproxy" && family == "-6"
	case "default dev owtun0 table 21998 proto 199 scope 253", "default dev owtun0 table 21998 proto 199 scope 253 linkdown":
		return method == "tun" && o.tun && family == "-4"
	case "default dev owtun0 table 21998 proto 199 metric 1024 pref medium", "default dev owtun0 table 21998 proto 199 metric 1024 linkdown pref medium":
		return method == "tun" && o.tun && family == "-6"
	case "8 default table 21998 proto 199 metric 32760":
		return method == "tun" && o.guarded && family == "-4"
	case "8 default dev lo table 21998 proto 199 metric 32760 pref medium":
		return method == "tun" && o.guarded && family == "-6"
	}
	return false
}

// Both files contain the canonical rules returned by xtables -S at commit.
// iptables-save quotes comment arguments and uses :CHAIN for declarations.
func readXTSnapshot(rules map[string]int, path, table string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if strings.HasPrefix(f[0], "*") {
			table = strings.TrimPrefix(f[0], "*")
			continue
		}
		if table != "mangle" || len(f) < 2 {
			continue
		}
		if f[0] == "-A" && f[1] == "HYPER_LAN" {
			rules[":HYPER_LAN"] = 1
		}
		if f[0] == "-N" {
			rules[":"+f[1]] = 1
			continue
		}
		if f[0] == "-A" {
			rules[xtKey(f)]++
		}
	}
}

func xtKey(f []string) string {
	cp := append([]string(nil), f...)
	for i := 1; i < len(cp); i++ {
		if cp[i-1] == "--comment" {
			cp[i] = strings.Trim(cp[i], "\"")
		}
	}
	return strings.Join(cp, " ")
}

func reservedChain(s string) bool {
	switch s {
	case "HYPER_LAN", "OW_HOLD", "OW_HOLD_DEV", "OW_HOLD_BLOCK":
		return true
	}
	return false
}
