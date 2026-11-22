// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
)

// system is everything hyperd asks of the router; tests replace it.
type system struct {
	Now func() time.Time
	// Run executes a command and returns stdout+stderr.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// HTTP builds an HTTP client, through proxy when non-empty.
	HTTP func(proxy string) *http.Client
	// Exists reports whether a path exists.
	Exists func(string) bool
}

func defaultSystem() *system {
	return &system{
		Now: time.Now,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		HTTP:   account.DefaultHTTP,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

// wanResolver is the WAN interface's first DNS server, from netifd.
func (m *manager) wanResolver(ctx context.Context, global bool) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if out, err := m.sys.Run(ctx, "ubus", "call", "network.interface.wan", "status"); err == nil {
		var st struct {
			DNS []string `json:"dns-server"`
		}
		if json.Unmarshal(out, &st) == nil {
			for _, s := range st.DNS {
				if a, err := netip.ParseAddr(s); err == nil && !a.IsLoopback() && !a.IsUnspecified() {
					return a.String()
				}
			}
		}
	}
	if global {
		return "8.8.8.8"
	}
	return "223.5.5.5"
}

// neighbors reads the kernel neighbour table: MAC -> addresses (both families).
func (m *manager) neighbors(ctx context.Context) map[string][]netip.Addr {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, _ := m.sys.Run(ctx, "ip", "neigh", "show")
	return parseNeighbors(out)
}

func parseNeighbors(out []byte) map[string][]netip.Addr {
	res := map[string][]netip.Addr{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		state := f[len(f)-1]
		if state == "FAILED" || state == "INCOMPLETE" {
			continue
		}
		addr, err := netip.ParseAddr(f[0])
		if err != nil {
			continue
		}
		for i := 1; i+1 < len(f); i++ {
			if f[i] == "lladdr" {
				if mac, err := net.ParseMAC(f[i+1]); err == nil && len(mac) == 6 {
					k := mac.String()
					res[k] = append(res[k], addr.WithZone(""))
				}
			}
		}
	}
	return res
}

// selfAddrs are the router's own addresses (exempted from listed_only).
func selfAddrs() []netip.Addr {
	var out []netip.Addr
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && !p.Addr().IsLoopback() {
			out = append(out, p.Addr())
		}
	}
	return out
}

// logger appends timestamped lines to hyperd's log (shown on the Logs page).
type logger struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func (l *logger) Printf(tag, format string, args ...any) {
	if l == nil || l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s [%s] %s\n", l.now().Format("2006-01-02 15:04:05"), tag, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

// Errorf logs a failure; the Logs page reads the ERROR after the tag as its severity.
func (l *logger) Errorf(tag, format string, args ...any) {
	l.Printf(tag, "ERROR "+format, args...)
}

// trimLog keeps the last half of a log that grew past limit bytes.
func trimLog(path string, limit int64) {
	st, err := os.Stat(path)
	if err != nil || st.Size() <= limit {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	b = b[len(b)-int(limit/2):]
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	_ = os.WriteFile(path, b, 0o600)
}

// coreGroupName is the group the core runs in, created by uci-defaults (HYPERD_CORE_GROUP
// stands in for it in tests). The firewall lets its sockets out uncaptured (meta skgid), so
// it must not be root's.
func coreGroupName() string {
	if v := os.Getenv("HYPERD_CORE_GROUP"); v != "" {
		return v
	}
	return "hyper"
}

// coreGroup is the group serve runs mihomo in, as root with every capability, as OpenClash
// runs its core: the firewall lets the group's sockets out uncaptured, so it
// may not be root's. A hyperd that is not the router's root (development, tests, a user
// namespace) runs the core as itself: group 0.
func coreGroup() (uint32, error) {
	if !hostRoot() {
		return 0, nil
	}
	name := coreGroupName()
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("core group %s missing", name)
	}
	id, err := strconv.ParseUint(g.Gid, 10, 32)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid core group %s", name)
	}
	return uint32(id), nil
}

// coreEnv is the core's extra environment. On MIPS its TLS (the AnyTLS nodes set no client
// fingerprint, so Go's defaults apply) keeps off the post-quantum key share, as hyperd's own
// does (godebug_mipsx.go): the same unaligned SHA-3 reads would trap on every handshake.
func coreEnv() []string {
	if runtime.GOARCH == "mips" || runtime.GOARCH == "mipsle" {
		return []string{"GODEBUG=tlsmlkem=0"}
	}
	return nil
}

// hostRoot reports root of the initial user namespace: inside another (unshare -r in tests)
// root cannot take on other groups.
func hostRoot() bool {
	if os.Geteuid() != 0 {
		return false
	}
	b, err := os.ReadFile("/proc/self/uid_map")
	f := strings.Fields(string(b))
	return err == nil && len(f) == 3 && f[0] == "0" && f[1] == "0" && f[2] == "4294967295"
}
