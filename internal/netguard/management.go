package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

const managementState = "/var/run/hyper/network"

func presentLAN() (map[netip.Addr]bool, error) {
	names, e := os.ReadFile(filepath.Join(managementState, "interfaces"))
	if e != nil {
		return nil, e
	}
	targets := map[netip.Addr]bool{}
	for _, name := range strings.Fields(string(names)) {
		iface, e := net.InterfaceByName(name)
		if e != nil {
			continue
		}
		addrs, e := iface.Addrs()
		if e != nil {
			return nil, e
		}
		for _, v := range addrs {
			p, e := netip.ParsePrefix(v.String())
			if e == nil && p.Addr().IsGlobalUnicast() {
				targets[p.Addr()] = true
			}
		}
	}
	return targets, nil
}
func managementListeners(settings string, hosts map[netip.Addr]bool) []netip.AddrPort {
	found := map[netip.AddrPort]bool{}
	for _, line := range strings.Split(settings, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(key, "uhttpd.") || !(strings.HasSuffix(key, ".listen_http") || strings.HasSuffix(key, ".listen_https")) {
			continue
		}
		for _, t := range strings.Fields(value) {
			a, e := netip.ParseAddrPort(strings.Trim(t, "'\""))
			if e != nil || a.Port() == 0 {
				continue
			}
			for h := range hosts {
				if a.Addr() == h || a.Addr().IsUnspecified() && a.Addr().Is4() == h.Is4() {
					found[netip.AddrPortFrom(h, a.Port())] = true
				}
			}
		}
	}
	out := make([]netip.AddrPort, 0, len(found))
	for a := range found {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Compare(out[j]) < 0 })
	return out
}
func responding(ctx context.Context, targets []netip.AddrPort) ([]netip.AddrPort, error) {
	if len(targets) > 256 {
		return nil, errors.New("too many configured management listeners")
	}
	var mx sync.Mutex
	var wg sync.WaitGroup
	out := []netip.AddrPort{}
	limit := make(chan struct{}, 8)
	for _, a := range targets {
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			conn, e := (&net.Dialer{Timeout: 400 * time.Millisecond}).DialContext(ctx, "tcp", a.String())
			if e == nil {
				conn.Close()
				mx.Lock()
				out = append(out, a)
				mx.Unlock()
			}
		}()
	}
	wg.Wait()
	return out, ctx.Err()
}

// Management compares TCP reachability for the LuCI listeners that accepted a
// connection before takeover. A moved/disconnected VLAN is handled by netifd.
func Management(ctx context.Context, record bool) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	hosts, e := presentLAN()
	if e != nil {
		return e
	}
	path := filepath.Join(managementState, "management.json")
	if record {
		var settings []byte
		if _, e = os.Stat("/etc/config/uhttpd"); e == nil {
			settings, e = exec.CommandContext(ctx, "uci", "-q", "show", "uhttpd").Output()
			if e != nil {
				return errors.New("cannot inspect management listeners")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		up, e := responding(ctx, managementListeners(string(settings), hosts))
		if e != nil {
			return e
		}
		b, e := json.Marshal(up)
		if e != nil {
			return e
		}
		return atomicfile.WriteFile(path, b, 0600, ".management-")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var expected []netip.AddrPort
	if len(b) > 65536 || json.Unmarshal(b, &expected) != nil {
		return errors.New("invalid management recovery record")
	}
	expected = slices.DeleteFunc(expected, func(a netip.AddrPort) bool { return !hosts[a.Addr()] })
	up, e := responding(ctx, expected)
	if e != nil {
		return e
	}
	if len(up) != len(expected) {
		return errors.New("previously reachable LuCI listener unavailable")
	}
	return nil
}
