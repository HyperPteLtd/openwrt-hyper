// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
)

// cnLists keeps the mainland lists the core reads (geosite-cn, geoip-cn) current: a weekly
// download replaces the packaged copy once it passes cnlist.Check.
type cnLists struct {
	m       *manager
	mu      sync.Mutex
	updater *cnlist.Updater
	started time.Time // the first call: the first check waits for the network and the clock
	checked time.Time // the last check that reached the source
	attempt time.Time
	fails   int
	running bool
}

func (m *manager) lists() cnlist.Lists {
	return cnlist.Lists{Dir: filepath.Join(m.paths.Etc, "cnlists"), Packaged: filepath.Join(m.paths.Etc, "rule-set")}
}

// linkLists points the core's builtin/<list>.mrs at the copy of each list in use. It
// returns the lists whose link changed.
func (m *manager) linkLists() ([]string, error) {
	builtin := filepath.Join(m.paths.CoreDir, "builtin")
	// Earlier builds linked builtin itself to the packaged directory.
	if fi, err := os.Lstat(builtin); err == nil && !fi.IsDir() {
		if err = os.Remove(builtin); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(builtin, 0o755); err != nil {
		return nil, err
	}
	lists := m.lists()
	var moved []string
	for _, list := range cnlist.Names {
		link, target := filepath.Join(builtin, list+".mrs"), lists.Path(list)
		if cur, err := os.Readlink(link); err == nil && cur == target {
			continue
		}
		tmp := link + ".new"
		os.Remove(tmp)
		if err := os.Symlink(target, tmp); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, link); err != nil {
			os.Remove(tmp)
			return nil, err
		}
		moved = append(moved, list)
	}
	return moved, nil
}

// maybeUpdate runs a check when one is due (called every minute by schedule while hyper is
// enabled). The first waits five minutes after that first call, then cnlist.Due decides.
func (c *cnLists) maybeUpdate(ctx context.Context) {
	now := c.m.sys.Now()
	c.mu.Lock()
	if c.started.IsZero() {
		c.started = now
	}
	due := !c.running && now.Sub(c.started) >= 5*time.Minute &&
		cnlist.Due(now, c.m.lists().Updated(), c.checked, c.attempt, c.fails)
	if due {
		c.running, c.attempt = true, now
	}
	c.mu.Unlock()
	if due {
		go c.update(ctx)
	}
}

func (c *cnLists) update(ctx context.Context) {
	m := c.m
	c.mu.Lock()
	if c.updater == nil {
		c.updater = &cnlist.Updater{Lists: m.lists(), HTTP: m.sys.HTTP, Proxy: m.svc.proxyURL}
	}
	u := c.updater
	c.mu.Unlock()
	changed, err := u.Update(ctx)
	c.mu.Lock()
	c.running = false
	if err == nil {
		c.checked, c.fails = m.sys.Now(), 0
	} else {
		c.fails++
	}
	fails := c.fails
	c.mu.Unlock()
	// One failure is routine (no network yet, GitHub unreachable); only a run of them is news.
	if err != nil && fails == 3 {
		m.log.Errorf("RULES", "Updating the mainland lists keeps failing: %v", err)
	}
	if len(changed) > 0 {
		m.log.Printf("RULES", "Mainland lists updated: %s.", strings.Join(changed, ", "))
	}
	m.reloadLists(ctx, changed)
}

// reloadLists repoints the running core's lists and makes it read the ones that changed.
// A file replaced in place keeps its link; a list that moved between the packaged copy
// and the download gets a new one.
func (m *manager) reloadLists(ctx context.Context, changed []string) {
	api := m.svc.controller()
	if api == nil {
		return
	}
	moved, err := m.linkLists()
	if err != nil {
		m.log.Errorf("RULES", "Linking the mainland lists failed: %v", err)
		return
	}
	for _, list := range cnlist.Names {
		if !contains(changed, list) && !contains(moved, list) {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		// Global and DNS-only configs may not use the list; nothing to reload there.
		_ = api.RefreshRules(c, list)
		cancel()
	}
}
