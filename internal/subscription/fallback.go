package subscription

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/coregroup"
)

// A request that fails in transit is retried once over the router's other way out, as
// OpenClash retries without its agent. While the takeover captures the router's own
// traffic (fw4) the usual path runs through the core, whose nodes may all be dead after a
// plan expired: the retry goes direct. Where it does not (fw3, or no takeover) the usual
// path is direct, which a blocked domain defeats: the retry goes through the running core.

type lookupFunc func(context.Context, string) ([]net.IPAddr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

type route struct {
	lookup lookupFunc
	dial   dialFunc
	// byName: dial takes a host name and the far end (the core) resolves it.
	byName bool
	// tlsOnly: only requests whose TLS authenticates the server end to end take this route.
	tlsOnly bool
}

// The group, paths and port are those of the package, the network helper and the core
// (router.MixedPort). The core runs as root in coreGID's group (coregroup: nogroup);
// coreOwner is the account its sockets are listed under in /proc/net/tcp (tests stand in
// their own).
var (
	coreGID       = coregroup.GID
	coreOwner     = "0"
	takeoverState = "/var/run/hyper/network"
	wanResolvers  = "/tmp/resolv.conf.d/resolv.conf.auto"
	procTCP       = "/proc/net/tcp"
	mixedProxy    = "127.0.0.1:19763"
	fallbackRoute = routerFallback
)

func routerFallback() *route {
	backend, _ := os.ReadFile(takeoverState + "/backend")
	if _, err := os.Stat(takeoverState + "/owned"); err == nil && strings.TrimSpace(string(backend)) == "fw4" {
		// The firewall returns sockets of the core's group (meta skgid) before capturing.
		gid, err := coreGID()
		if err != nil {
			return nil
		}
		return directRoute(gid)
	}
	if coreListening() {
		return &route{lookup: net.DefaultResolver.LookupIPAddr, dial: dialMixed, byName: true, tlsOnly: true}
	}
	return nil
}

// directRoute leaves as if the takeover were off: sockets carry the core's group, and names
// resolve at the WAN's DNS servers because the router's resolver answers through the core,
// with fake addresses under fake-ip.
func directRoute(gid int) *route {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialAsGroup(ctx, gid, network, address)
	}
	servers := wanServers()
	lookup := func(ctx context.Context, host string) ([]net.IPAddr, error) {
		var err error
		for _, server := range servers {
			r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dial(ctx, network, server)
			}}
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			var ips []net.IPAddr
			ips, err = r.LookupIPAddr(c, host)
			cancel()
			if err == nil {
				return ips, nil
			}
		}
		return nil, err
	}
	return &route{lookup: lookup, dial: dial}
}

// wanServers lists the WAN's DNS servers, then the public ones the core also falls back to.
func wanServers() []string {
	var out []string
	b, _ := os.ReadFile(wanResolvers)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != "nameserver" {
			continue
		}
		if a, err := netip.ParseAddr(f[1]); err == nil && a.IsGlobalUnicast() {
			out = append(out, netip.AddrPortFrom(a, 53).String())
		}
	}
	return append(out, "223.5.5.5:53", "119.29.29.29:53")
}

// procAddr is an IPv4 address and port as /proc/net/tcp prints them.
func procAddr(ap netip.AddrPort) string {
	ip := ap.Addr().Unmap().As4()
	return fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32(ip[:]), ap.Port())
}

var anyPeer = netip.AddrPortFrom(netip.IPv4Unspecified(), 0)

// mixedOwner is the account and inode /proc/net/tcp lists for the mixed-port socket whose
// peer is remote (anyPeer: the listener), or "" for none. The kernel takes both from the
// socket's file: a connection not yet accepted has none, so it is listed as root with inode
// 0, then as the account that accepted it.
func mixedOwner(remote netip.AddrPort) (uid, inode string) {
	b, err := os.ReadFile(procTCP)
	if err != nil {
		return "", ""
	}
	local, peer := procAddr(netip.MustParseAddrPort(mixedProxy)), procAddr(remote)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 9 && f[1] == local && f[2] == peer {
			return f[7], f[9]
		}
	}
	return "", ""
}

// coreListening reports whether the core's account holds the mixed port, so that no other
// local program is taken for the proxy: an account other than root cannot list as root.
func coreListening() bool {
	uid, inode := mixedOwner(anyPeer)
	return uid == coreOwner && inode != "" && inode != "0"
}

// coreAccepted waits briefly for the core's account to accept the connection from local.
// The core may have restarted since the route was chosen, and another program may hold
// the port: only this very connection's owner counts, once it has a file (inode).
func coreAccepted(ctx context.Context, local netip.AddrPort) bool {
	for deadline := time.Now().Add(time.Second); ctx.Err() == nil && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		switch uid, inode := mixedOwner(local); {
		case uid == coreOwner && inode != "" && inode != "0":
			return true
		case uid != "" && uid != "0" && uid != coreOwner:
			return false
		}
	}
	return false
}

// dialMixed tunnels to address through the core's mixed port. TLS still runs end to end
// with the original host name.
func dialMixed(ctx context.Context, _, address string) (net.Conn, error) {
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", mixedProxy)
	if err != nil {
		return nil, err
	}
	if local, ok := c.LocalAddr().(*net.TCPAddr); !ok || !coreAccepted(ctx, local.AddrPort()) {
		c.Close()
		return nil, errors.New("proxy_not_core")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Unix(1, 0)) })
	_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", address, address)
	if err == nil {
		br := bufio.NewReader(c)
		var res *http.Response
		res, err = http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err == nil && (res.StatusCode != http.StatusOK || br.Buffered() > 0) {
			err = errors.New("proxy_connect_refused")
		}
	}
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = c.SetDeadline(time.Time{})
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// client is c over this route. Redirect policy, time limit and TLS settings stay. Through the
// core the core resolves the name; otherwise the route resolves it and dials what it found.
func (r *route) client(c http.Client) *http.Client {
	tr := http.DefaultTransport.(*http.Transport)
	if base, ok := c.Transport.(*http.Transport); ok {
		tr = base
	}
	tr = tr.Clone()
	tr.Proxy = nil
	tr.DisableKeepAlives = true
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if r.byName {
			return r.dial(ctx, network, address)
		}
		return dialResolved(ctx, network, address, r.lookup, r.dial, false)
	}
	c.Transport = tr
	return &c
}

// publicTransport dials only public addresses, which it resolved itself.
func publicTransport() *http.Transport {
	lookup, dial := net.DefaultResolver.LookupIPAddr, (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialResolved(ctx, network, address, lookup, dial, true)
	}
	return tr
}

// dialResolved dials the addresses it resolved itself. With public, a host with any
// address outside the public internet is refused before anything is dialed.
func dialResolved(ctx context.Context, network, address string, lookup lookupFunc, dial dialFunc, public bool) (net.Conn, error) {
	h, p, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := lookup(ctx, h)
	if e != nil {
		return nil, e
	}
	for _, ip := range ips {
		if public && (!ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.To4() != nil && ip.IP.To4()[0] == 100 && (ip.IP.To4()[1]&192) == 64) {
			return nil, errors.New("private_provider_address")
		}
	}
	for _, ip := range ips {
		c, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), p))
		e = err
		if err == nil {
			return c, nil
		}
	}
	if e != nil {
		return nil, e
	}
	return nil, errors.New("provider_unreachable")
}
