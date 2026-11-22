// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package coreproxy

import (
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"testing"
	"time"
)

func listen(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return l.Addr().String()
}

func as(t *testing.T, uid string) {
	old := Owner
	Owner = uid
	t.Cleanup(func() { Owner = old })
}

func TestDial(t *testing.T) {
	if _, err := os.Stat(procTCP); err != nil {
		t.Skip("no /proc/net/tcp")
	}
	me, err := user.Current()
	if err != nil || me.Uid == "0" {
		t.Skip("needs an unprivileged account")
	}
	addr := listen(t)
	ctx := context.Background()

	// The listener runs as this test's account: standing in for the core's (root), it is
	// trusted once it has accepted.
	as(t, me.Uid)
	c, err := Dial(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	// The core is root: this account's listener is not the core, refused at once.
	as(t, "0")
	if _, err := Dial(ctx, "tcp", addr); !errors.Is(err, ErrNotCore) {
		t.Fatalf("foreign listener trusted: %v", err)
	}
	as(t, me.Uid)
	if _, err := Dial(ctx, "tcp", "192.0.2.1:7890"); !errors.Is(err, ErrNotCore) {
		t.Fatal(err)
	}
}

// A connection still waiting in the listener's queue is listed as root with inode 0: not
// yet the core's, however long it waits.
func TestDialWaitsForAccept(t *testing.T) {
	if _, err := os.Stat(procTCP); err != nil {
		t.Skip("no /proc/net/tcp")
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0") // never accepts
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, uid := range []string{"0", me.Uid} {
		as(t, uid)
		start := time.Now()
		if _, err := Dial(context.Background(), "tcp", l.Addr().String()); !errors.Is(err, ErrNotCore) {
			t.Fatalf("owner %s: never accepted, yet trusted: %v", uid, err)
		}
		if time.Since(start) < 900*time.Millisecond {
			t.Fatalf("owner %s: refused before the wait ran out", uid)
		}
	}
}
