// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package subscriptioncli

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/checkin"
	sub "github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// The Hyper account's files: the session (0600, never shown), the check-in calendar, and the
// API addresses found, which live in RAM.
const (
	sessionPath   = "/etc/hyper/session"
	endpointCache = "/var/run/hyper/api-endpoints.json"
)

var checkinPath = "/etc/hyper/checkin.json"

var emailAddress = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// hyperAccount is the router's Hyper account: it signs in as this device (UCI
// hyper.account.device_id, created on first boot and kept across upgrades).
func hyperAccount() *account.Client {
	return &account.Client{
		Sessions: &account.Sessions{Path: sessionPath},
		Resolver: &account.Resolver{CachePath: endpointCache},
		DeviceID: func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, _ := exec.CommandContext(ctx, "uci", "-q", "get", "hyper.account.device_id").Output()
			return strings.TrimSpace(string(out))
		},
	}
}

func resultOf(r account.Result) map[string]any {
	out := map[string]any{"result": r.OK, "code": r.Code, "msg": r.Msg}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

func jsonScalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

var hyperMethods = []string{"account", "send_code", "login", "logout", "api", "checkin_status", "checkin", "catalog", "rule_toggle"}

// hyperRun runs the Hyper account's methods; handled is false for any other.
func hyperRun(ctx context.Context, store *sub.Store, method string, in input, f sub.Fetcher) (data any, changed []string, err error, handled bool) {
	if !slices.Contains(hyperMethods, method) {
		return nil, nil, nil, false
	}
	acct, _ := f.HyperClient()
	if acct == nil {
		return nil, nil, errors.New("account_unavailable"), true
	}
	switch method {
	case "account":
		s := acct.Sessions.Load()
		if s == nil {
			return map[string]any{"signed_in": false}, nil, nil, true
		}
		return map[string]any{"signed_in": true, "email": s.Email}, nil, nil, true
	case "send_code":
		email := strings.TrimSpace(in.Email)
		if !emailAddress.MatchString(email) {
			return map[string]any{"result": false, "error": "illegal email"}, nil, nil, true
		}
		r := acct.Request(ctx, "POST", "/v1/common/send_code", account.Options{NoAuth: true,
			Body: map[string]any{"email": email, "type": account.EmailCodeTypeAuth}})
		return resultOf(r), nil, nil, true
	case "login":
		email := strings.TrimSpace(in.Email)
		if !emailAddress.MatchString(email) || (in.Password == "" && in.Code == "") {
			return map[string]any{"result": false, "error": "missing credentials"}, nil, nil, true
		}
		r := acct.Login(ctx, email, in.Password, in.Code, in.CfToken, in.Invite)
		if !r.OK {
			return resultOf(r), nil, nil, true
		}
		if err := store.ConfigureHyper(store.HyperDisabled()); err != nil {
			return nil, nil, err, true
		}
		return map[string]any{"result": true, "email": email}, []string{sub.HyperSource}, nil, true
	case "logout":
		if acct.Sessions.Load() != nil {
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			acct.Request(c, "POST", "/v1/public/user/logout", account.Options{})
			cancel()
		}
		if err := acct.Sessions.Clear(); err != nil {
			return nil, nil, err, true
		}
		if err := store.Revoke(sub.HyperSource); err != nil {
			return nil, nil, err, true
		}
		return map[string]any{"result": true}, []string{sub.HyperSource}, nil, true
	case "api":
		return hyperAPI(ctx, acct, in), nil, nil, true
	case "checkin_status":
		key := checkinKey(acct)
		if key == "" {
			return map[string]any{"result": false, "error": "not signed in"}, nil, nil, true
		}
		return map[string]any{"result": true, "dates": checkin.Load(checkinPath).Dates(key)}, nil, nil, true
	case "checkin":
		cal := checkin.Load(checkinPath)
		dates, added, err := cal.Record(checkinKey(acct), in.Date, time.Now())
		if err != nil {
			return map[string]any{"result": false, "error": err.Error()}, nil, nil, true
		}
		if added && cal.Save(checkinPath) != nil {
			return map[string]any{"result": false, "error": "write failed"}, nil, nil, true
		}
		return map[string]any{"result": true, "added": added, "dates": dates}, nil, nil, true
	case "catalog":
		p, _ := store.Load(sub.HyperSource)
		disabled := store.HyperDisabled()
		items := []map[string]any{}
		for _, l := range p.Lists {
			items = append(items, map[string]any{"key": l.Key, "policy": l.Policy, "server_enabled": l.Enabled, "entries": l.Entries,
				"stale": l.Stale, "enabled": l.Enabled && !slices.Contains(disabled, l.Key)})
		}
		return map[string]any{"items": items}, nil, nil, true
	case "rule_toggle":
		if in.Enabled == nil {
			return nil, nil, errors.New("invalid_rule_toggle"), true
		}
		disabled := slices.DeleteFunc(store.HyperDisabled(), func(k string) bool { return k == in.Key })
		if !*in.Enabled {
			disabled = append(disabled, in.Key)
		}
		if err := store.ConfigureHyper(disabled); err != nil {
			return nil, nil, err, true
		}
		return map[string]any{"result": true}, []string{sub.HyperSource}, nil, true
	}
	return nil, nil, nil, false
}

func checkinKey(acct *account.Client) string {
	s := acct.Sessions.Load()
	if s == nil {
		return ""
	}
	return checkin.AccountKey(s.Email)
}

// hyperAPI is the account pages' window on the Hyper service: allowlisted paths only, the
// session added here, never shown.
func hyperAPI(ctx context.Context, acct *account.Client, in input) any {
	method := strings.ToUpper(in.APIMethod)
	if method == "" {
		method = "GET"
	}
	if !slices.Contains([]string{"GET", "POST", "PUT", "DELETE"}, method) {
		return map[string]any{"ok": false, "error": "illegal method"}
	}
	if !account.AllowedPath(in.Path) {
		return map[string]any{"ok": false, "error": "path not allowed"}
	}
	if method == "POST" && strings.HasPrefix(in.Path, "/v2/orders/") && !account.ValidIdempotencyKey(in.IdempotencyKey) {
		return map[string]any{"ok": false, "error": "idempotency key required"}
	}
	o := account.Options{IdempotencyKey: in.IdempotencyKey, Query: map[string]string{}}
	for k, v := range in.APIQuery {
		if v != nil {
			o.Query[k] = jsonScalar(v)
		}
	}
	if method != "GET" && len(in.Body) > 0 && string(in.Body) != "null" {
		o.Body = in.Body
	}
	return acct.Request(ctx, method, in.Path, o)
}
