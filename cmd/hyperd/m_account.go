// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/checkin"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

var emailRe = lazyre.New(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (m *manager) syncStatePath() string { return filepath.Join(m.paths.RunDir, "sync.json") }

func (m *manager) syncState() any {
	var v any
	if readJSONFile(m.syncStatePath(), &v) {
		return v
	}
	return nil
}

func resultOf(r account.Result) map[string]any {
	out := map[string]any{"result": r.OK, "code": r.Code, "msg": r.Msg}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

func (m *manager) registerAccount() {
	m.handlers["send_code"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct{ Email string }
		_ = jsonArgs(args, &a)
		email := strings.TrimSpace(a.Email)
		if !emailRe().MatchString(email) {
			return map[string]any{"result": false, "error": "illegal email"}, nil
		}
		return resultOf(m.client.Request(ctx, "POST", "/v1/common/send_code", account.Options{NoAuth: true,
			Body: map[string]any{"email": email, "type": account.EmailCodeTypeAuth}})), nil
	}
	m.handlers["login"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct {
			Email, Password, Code, Invite string
			CfToken                       string `json:"cf_token"`
		}
		_ = jsonArgs(args, &a)
		if a.Email == "" || (a.Password == "" && a.Code == "") {
			return map[string]any{"result": false, "error": "missing credentials"}, nil
		}
		r := m.client.Login(ctx, a.Email, a.Password, a.Code, a.CfToken, a.Invite)
		if r.OK {
			m.syncer.start()
		}
		out := resultOf(r)
		out["sync"] = m.syncState()
		return out, nil
	}
	m.handlers["register"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct {
			Email, Password, Code, Invite string
			CfToken                       string `json:"cf_token"`
		}
		_ = jsonArgs(args, &a)
		if a.Email == "" || a.Password == "" {
			return map[string]any{"result": false, "error": "missing credentials"}, nil
		}
		return resultOf(m.client.Register(ctx, a.Email, a.Password, a.Code, a.Invite, a.CfToken)), nil
	}
	m.handlers["logout"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		if m.sessions.Load() != nil {
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			m.client.Request(c, "POST", "/v1/public/user/logout", account.Options{})
			cancel()
		}
		if err := m.sessions.Clear(); err != nil {
			return nil, err
		}
		_ = m.edit(func(c *uciConfig) bool { c.Ensure("config", "hyper").Set("enabled", "0"); return true })
		m.svc.request("stop")
		return map[string]any{"result": true}, nil
	}
	m.handlers["sync_subscription"] = func(context.Context, json.RawMessage) (any, error) { return m.syncer.start(), nil }
	m.handlers["sync_status"] = func(context.Context, json.RawMessage) (any, error) { return m.syncer.status(), nil }

	m.handlers["checkin_status"] = func(context.Context, json.RawMessage) (any, error) {
		key := m.checkinKey()
		if key == "" {
			return map[string]any{"result": false, "error": "not signed in"}, nil
		}
		return map[string]any{"result": true, "dates": checkin.Load(m.paths.checkin()).Dates(key)}, nil
	}
	m.handlers["checkin"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ Date string }
		_ = jsonArgs(args, &a)
		store := checkin.Load(m.paths.checkin())
		dates, added, err := store.Record(m.checkinKey(), a.Date, m.sys.Now())
		if err != nil {
			return map[string]any{"result": false, "error": err.Error()}, nil
		}
		if added && store.Save(m.paths.checkin()) != nil {
			return map[string]any{"result": false, "error": "write failed"}, nil
		}
		return map[string]any{"result": true, "added": added, "dates": dates}, nil
	}

	m.handlers["api"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct {
			Method         string
			Path           string
			Query          map[string]any
			Body           json.RawMessage
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = jsonArgs(args, &a)
		method := strings.ToUpper(a.Method)
		if method == "" {
			method = "GET"
		}
		if !contains([]string{"GET", "POST", "PUT", "DELETE"}, method) {
			return map[string]any{"ok": false, "error": "illegal method"}, nil
		}
		if !account.AllowedPath(a.Path) {
			return map[string]any{"ok": false, "error": "path not allowed"}, nil
		}
		if method == "POST" && strings.HasPrefix(a.Path, "/v2/orders/") && !account.ValidIdempotencyKey(a.IdempotencyKey) {
			return map[string]any{"ok": false, "error": "idempotency key required"}, nil
		}
		o := account.Options{IdempotencyKey: a.IdempotencyKey, Query: map[string]string{}}
		for k, v := range a.Query {
			if v != nil {
				o.Query[k] = jsonScalar(v)
			}
		}
		if method != "GET" && len(a.Body) > 0 && string(a.Body) != "null" {
			o.Body = a.Body
		}
		return m.client.Request(ctx, method, a.Path, o), nil
	}
}

func jsonScalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func (m *manager) checkinKey() string {
	s := m.sessions.Load()
	if s == nil {
		return ""
	}
	return checkin.AccountKey(s.Email)
}

// syncJob runs subscription syncs in the background, one at a time.
type syncJob struct {
	m       *manager
	mu      sync.Mutex
	id      string
	pending bool
	failed  bool
	started time.Time
	session string
}

func (j *syncJob) status() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	phase := "done"
	switch {
	case j.pending:
		phase = "running"
	case j.failed:
		phase = "failed"
	}
	var id any
	if j.id != "" {
		id = j.id
	}
	return map[string]any{"result": phase != "failed", "id": id, "pending": j.pending, "phase": phase, "state": j.m.syncState()}
}

func (j *syncJob) start() map[string]any {
	sess := j.m.sessions.Load().Identity()
	j.mu.Lock()
	if j.pending && j.session == sess {
		j.mu.Unlock()
		out := j.status()
		out["accepted"] = true
		return out
	}
	j.id, j.pending, j.failed, j.started, j.session = uuid(), true, false, j.m.sys.Now(), sess
	id := j.id
	j.mu.Unlock()
	go j.run(id)
	out := j.status()
	out["accepted"] = true
	return out
}

func (j *syncJob) run(id string) {
	m := j.m
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Minute)
	defer cancel()
	c, _ := m.config()
	s := &subscription.Syncer{Client: m.client, NodesPath: m.paths.nodes(), RulesDir: m.paths.rules(),
		Proxy: m.svc.proxyURL, InsecureDefault: c.Get("subscription", "allow_insecure") != "0", HTTP: m.sys.HTTP, Now: m.sys.Now,
		Log: func(f string, a ...any) { m.log.Printf("SYNC", f, a...) }}
	out, err := s.Run(ctx)
	failed := err != nil || !out.State.OK
	if err == nil {
		b, _ := json.Marshal(out.State)
		_ = os.WriteFile(m.syncStatePath(), append(b, '\n'), 0o600)
	}
	if err == nil && out.Nodes != nil {
		m.keepMainNode(out.Nodes)
		if (out.NodesChanged || out.RulesChanged) && m.enabled() {
			m.log.Printf("SYNC", "Restarting service...")
			m.svc.request("restart")
		}
	}
	if err != nil && !errors.Is(err, subscription.ErrSessionChanged) {
		m.log.Errorf("SYNC", "%v", err)
	}
	j.mu.Lock()
	if j.id == id {
		j.pending, j.failed = false, failed
	}
	j.mu.Unlock()
}

// keepMainNode points main_node at a node that still exists (first of the top country).
func (m *manager) keepMainNode(store *nodes.Store) {
	_ = m.edit(func(c *uciConfig) bool {
		cur := c.Get("config", "main_node")
		if _, ok := store.Find(cur); ok {
			return false
		}
		first := "nil"
		best := -1 << 31
		for _, n := range store.Nodes {
			if n.Sort > best {
				first, best = n.ID, n.Sort
			}
		}
		c.Ensure("config", "hyper").Set("main_node", first)
		if first == "nil" {
			m.log.Printf("SYNC", "No node available.")
		} else if n, ok := store.Find(first); ok {
			m.log.Printf("SYNC", "Main node is gone, switching to %s.", n.Label)
		}
		return true
	})
}
