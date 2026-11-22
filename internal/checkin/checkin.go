// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package checkin keeps the daily check-in calendar on the router.
//
// Hyper service has no check-in API; the app keeps a list of local dates on the phone.
// Here the list is per account, so a router shared by two accounts keeps two calendars.
//
//	/etc/hyper/checkin.json  {"version":1,"accounts":{"<md5(email)[:16]>":["2026-11-01",...]}}
package checkin

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	MaxDates    = 1100 // about three years per account
	MaxAccounts = 16
)

// Until NTP syncs, a router without an RTC boots with an old clock (2025-01-01).
const saneClock = 1735689600

var (
	ErrNotSignedIn = errors.New("not signed in")
	ErrDate        = errors.New("illegal date")
	accountKeyRe   = lazyre.New(`^[0-9a-f]{16}$`)
)

// Store is the calendar file.
type Store struct {
	Version  int                 `json:"version"`
	Accounts map[string][]string `json:"accounts"`
}

// ValidDate is a real calendar date written YYYY-MM-DD, year 2000 or later.
func ValidDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Year() >= 2000
}

// AccountKey is a stable, non-reversible key for an email.
func AccountKey(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return ""
	}
	sum := md5.Sum([]byte(e))
	return hex.EncodeToString(sum[:])[:16]
}

// Plausible rejects dates more than a day from the router clock once it is set
// (the browser sends its local date; time zones span a day either side of UTC).
func Plausible(date string, now time.Time) bool {
	if !ValidDate(date) {
		return false
	}
	if now.Unix() < saneClock {
		return true
	}
	t, _ := time.Parse("2006-01-02", date)
	diff := t.Unix()/86400 - now.Unix()/86400
	return diff >= -1 && diff <= 1
}

func clean(list []string) []string {
	var out []string
	for _, d := range list {
		if ValidDate(d) && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	if len(out) > MaxDates {
		out = out[len(out)-MaxDates:]
	}
	return out
}

func normalize(s *Store) *Store {
	out := &Store{Version: 1, Accounts: map[string][]string{}}
	if s == nil {
		return out
	}
	for k, v := range s.Accounts {
		if accountKeyRe().MatchString(k) {
			if d := clean(v); len(d) > 0 {
				out.Accounts[k] = d
			}
		}
	}
	return out
}

// Load reads the calendar; anything unreadable is an empty calendar.
func Load(path string) *Store {
	var s Store
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return normalize(&s)
}

// Dates returns an account's dates.
func (s *Store) Dates(key string) []string {
	if d := s.Accounts[key]; d != nil {
		return d
	}
	return []string{}
}

// Record checks key in on date; added is false when it was already checked in.
func (s *Store) Record(key, date string, now time.Time) (dates []string, added bool, err error) {
	if key == "" {
		return nil, false, ErrNotSignedIn
	}
	if !Plausible(date, now) {
		return nil, false, ErrDate
	}
	if slices.Contains(s.Accounts[key], date) {
		return s.Accounts[key], false, nil
	}
	s.Accounts[key] = clean(append(slices.Clone(s.Accounts[key]), date))
	if len(s.Accounts) > MaxAccounts {
		// Keep the accounts that checked in most recently.
		var others []string
		for k := range s.Accounts {
			if k != key {
				others = append(others, k)
			}
		}
		last := func(k string) string { d := s.Accounts[k]; return d[len(d)-1] }
		slices.SortFunc(others, func(a, b string) int { return strings.Compare(last(a), last(b)) })
		for _, k := range others[:len(s.Accounts)-MaxAccounts] {
			delete(s.Accounts, k)
		}
	}
	return s.Accounts[key], true, nil
}

// Save writes the calendar atomically.
func (s *Store) Save(path string) error {
	b, err := json.Marshal(normalize(s))
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o600)
}
