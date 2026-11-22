// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package coreproxy dials the core's loopback mixed port and trusts a connection only once
// the core has accepted it. The port is checked per connection, not once:
// the core may restart between a sync choosing the route and a dial, and a local program
// could hold 127.0.0.1:<mixed_port> meanwhile.
package coreproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Owner is the account /proc/net/tcp lists the core's sockets under: it runs as root (in
// the hyper group, which the listing does not show). Tests stand in their own.
var Owner = "0"

// procTCP lists the IPv4 TCP sockets with their owners.
var procTCP = "/proc/net/tcp"

// ErrNotCore means the listener is not the core's.
var ErrNotCore = errors.New("proxy_not_core")

// procAddr is an IPv4 address and port as /proc/net/tcp prints them.
func procAddr(ap netip.AddrPort) string {
	ip := ap.Addr().Unmap().As4()
	return fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32(ip[:]), ap.Port())
}

// owner is the account and inode /proc/net/tcp lists for the socket at local whose peer is
// remote. The kernel takes both from the socket's file: a connection not yet accepted has
// none, so it is listed as root with inode 0, then as the account that accepted it.
func owner(local, remote netip.AddrPort) (uid, inode string) {
	b, err := os.ReadFile(procTCP)
	if err != nil {
		return "", ""
	}
	l, r := procAddr(local), procAddr(remote)
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 9 && f[1] == l && f[2] == r {
			return f[7], f[9]
		}
	}
	return "", ""
}

// Dial connects to the core's listener at addr (an IPv4 loopback address) and returns the
// connection once the core has accepted it: listed as Owner (root) with a file (inode).
// An account other than root cannot be listed as root, so a foreign listener is refused as
// soon as it accepts, and one that never accepts within a second.
func Dial(ctx context.Context, _, addr string) (net.Conn, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().Unmap().IsLoopback() || !ap.Addr().Unmap().Is4() {
		return nil, ErrNotCore
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return nil, err
	}
	local := c.LocalAddr().(*net.TCPAddr).AddrPort()
	for deadline := time.Now().Add(time.Second); ctx.Err() == nil && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		switch uid, inode := owner(ap, local); {
		case uid == Owner && inode != "" && inode != "0":
			return c, nil
		case uid != "" && uid != "0" && uid != Owner:
			c.Close()
			return nil, ErrNotCore
		}
	}
	c.Close()
	return nil, ErrNotCore
}
