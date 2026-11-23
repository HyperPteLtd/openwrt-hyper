//go:build linux

package subscription

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// dialAsGroup connects with a socket of group gid. nftables takes a socket's group (meta
// skgid) from the credentials it was created with, and the file-system group belongs to one
// thread: it is set on a thread that exits with its goroutine, so no other code runs with it.
func dialAsGroup(ctx context.Context, gid int, network, address string) (net.Conn, error) {
	// A literal address keeps socket creation on this goroutine; a host name could race
	// dials on others.
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return nil, errors.New("literal_address_required")
	}
	type result struct {
		c   net.Conn
		err error
	}
	done := make(chan result, 1)
	go func() {
		// Never unlocked: the runtime ends the thread rather than reuse it.
		runtime.LockOSThread()
		unix.SetfsgidRetGid(gid)
		// An invalid group changes nothing and reports the current one.
		if current, _ := unix.SetfsgidRetGid(-1); current != gid {
			done <- result{err: errors.New("socket_group_unavailable")}
			return
		}
		c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
		done <- result{c, err}
	}()
	r := <-done
	return r.c, r.err
}
