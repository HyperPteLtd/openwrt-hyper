// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/uci"
)

type uciConfig = uci.Config

// jsonArgs decodes call arguments; unknown or mistyped fields are left at zero values.
func jsonArgs(args json.RawMessage, v any) error {
	if len(args) == 0 {
		return nil
	}
	return json.Unmarshal(args, v)
}

// tasks runs slow methods (network I/O) in the background. The call returns
// {_hyper_task, _hyper_timeout} and the UI polls task_status.
type tasks struct {
	m  *manager
	mu sync.Mutex
	by map[string]*task
}

type task struct {
	identity string
	started  time.Time
	timeout  time.Duration
	result   any
	done     bool
}

func newTasks(m *manager) *tasks { return &tasks{m: m, by: map[string]*task{}} }

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (t *tasks) identity() string { return t.m.sessions.Load().Identity() }

// wrap turns methods into background tasks.
func (t *tasks) wrap(names ...string) {
	for _, name := range names {
		h := t.m.handlers[name]
		timeout := 180 * time.Second
		if name == "install_update" {
			timeout = 480 * time.Second
		}
		t.m.handlers[name] = func(_ context.Context, args json.RawMessage) (any, error) {
			id := uuid()
			tk := &task{identity: t.identity(), started: t.m.sys.Now(), timeout: timeout}
			t.mu.Lock()
			t.gc()
			t.by[id] = tk
			t.mu.Unlock()
			go func() {
				ctx, cancel := context.WithTimeout(t.m.ctx, timeout)
				defer cancel()
				out, err := h(ctx, args)
				if err != nil {
					out = map[string]any{"result": false, "error": errCode(err)}
				}
				t.mu.Lock()
				tk.result, tk.done = out, true
				t.mu.Unlock()
			}()
			return map[string]any{"_hyper_task": id, "_hyper_timeout": int(timeout / time.Second)}, nil
		}
	}
}

// gc drops finished tasks after ten minutes (caller holds mu).
func (t *tasks) gc() {
	for id, tk := range t.by {
		if t.m.sys.Now().Sub(tk.started) > tk.timeout+10*time.Minute {
			delete(t.by, id)
		}
	}
}

var taskID = lazyre.New(`^[0-9a-f-]{36}$`)

func (t *tasks) status(_ context.Context, args json.RawMessage) (any, error) {
	var a struct{ ID string }
	_ = jsonArgs(args, &a)
	if !taskID().MatchString(a.ID) {
		return map[string]any{"state": "failed", "error": "illegal task"}, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tk := t.by[a.ID]
	// Results of another account's task are never shown.
	if tk == nil || tk.identity != t.identity() {
		return map[string]any{"state": "failed", "error": "session_changed"}, nil
	}
	if tk.done {
		return tk.result, nil
	}
	if t.m.sys.Now().Sub(tk.started) > tk.timeout {
		return map[string]any{"state": "failed", "error": "timeout"}, nil
	}
	return map[string]any{"state": "running"}, nil
}

// schedule runs the daily subscription sync (at subscription.auto_update_time), the
// daily software update check and the weekly mainland list update (no cron).
func (m *manager) schedule(ctx context.Context) {
	lastSync := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		c, err := m.config()
		if err != nil {
			continue
		}
		now := m.sys.Now()
		hour := c.Get("subscription", "auto_update_time")
		if hour == "" {
			hour = "4"
		}
		day := now.Format("2006-01-02")
		if c.Get("subscription", "auto_update") != "0" && fmt.Sprint(now.Hour()) == hour && lastSync != day && m.sessions.Load() != nil {
			lastSync = day
			m.syncer.start()
		}
		m.autoUpdate(ctx, false)
		// Only the running core reads the mainland lists: nothing is fetched while hyper is off.
		if c.Get("config", "enabled") == "1" {
			m.cn.maybeUpdate(ctx)
		}
	}
}

func contains[T comparable](s []T, v T) bool { return slices.Contains(s, v) }
