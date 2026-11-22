// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

type guardTiming struct {
	Tick   time.Duration
	Misses int
	Probe  time.Duration
	Stale  time.Duration // pulse older than this means serve is hung or gone
}

// guard runs beside serve. While hyper's takeover is installed (Owned exists) it
// expects a fresh pulse from serve and a live core controller. After Misses failed
// checks in a row it runs `network fault`, which removes the interception so LAN
// traffic flows directly instead of into a dead core.
func guard(ctx context.Context, p Paths, t guardTiming) error {
	if t.Probe <= 0 {
		t.Probe = 700 * time.Millisecond
	}
	if t.Stale <= 0 {
		t.Stale = 5 * time.Second
	}
	tick := time.NewTicker(t.Tick)
	defer tick.Stop()
	miss := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if _, err := os.Stat(p.Owned); err != nil {
			miss = 0
			continue
		}
		if healthy(ctx, p, t) {
			miss = 0
			continue
		}
		if miss++; miss < t.Misses {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := exec.CommandContext(c, p.Network, "fault").Run()
		cancel()
		if err == nil {
			miss = 0
		}
	}
}

func healthy(ctx context.Context, p Paths, t guardTiming) bool {
	st, err := os.Stat(p.Pulse)
	if err != nil || time.Since(st.ModTime()) > t.Stale {
		return false
	}
	secret, err := os.ReadFile(p.Secret)
	if err != nil {
		return false
	}
	// serve records the controller address it configured (infra.controller_port).
	addr := p.Control
	if b, err := os.ReadFile(p.Secret + ".addr"); err == nil && strings.TrimSpace(string(b)) != "" {
		addr = strings.TrimSpace(string(b))
	}
	c, cancel := context.WithTimeout(ctx, t.Probe)
	defer cancel()
	_, err = probeClient(addr, strings.TrimSpace(string(secret))).Version(c)
	return err == nil
}

// The guard probes every two seconds: keep one client (and its connection) until serve
// starts a core with a new secret or address.
var probe struct {
	sync.Mutex
	key string
	c   *control.Client
}

func probeClient(addr, secret string) *control.Client {
	probe.Lock()
	defer probe.Unlock()
	if key := addr + "\x00" + secret; probe.c == nil || probe.key != key {
		if probe.c != nil {
			probe.c.HTTP.CloseIdleConnections()
		}
		probe.key, probe.c = key, control.New(addr, secret)
	}
	return probe.c
}
