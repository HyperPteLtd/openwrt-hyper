// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package checkin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecord(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "checkin.json")
	s := Load(path)
	key := AccountKey(" A@B.c ")
	if key != AccountKey("a@b.c") || len(key) != 16 {
		t.Fatal(key)
	}
	if _, _, err := s.Record("", "2026-11-21", now); err != ErrNotSignedIn {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-11-25", "2026-02-30", "2026-11-1", "1999-01-01"} {
		if _, _, err := s.Record(key, d, now); err != ErrDate {
			t.Errorf("%s: %v", d, err)
		}
	}
	d, added, err := s.Record(key, "2026-11-22", now)
	if err != nil || !added || len(d) != 1 {
		t.Fatal(d, added, err)
	}
	if _, added, _ = s.Record(key, "2026-11-22", now); added {
		t.Fatal("added twice")
	}
	s.Record(key, "2026-11-20", now)
	if err = s.Save(path); err != nil {
		t.Fatal(err)
	}
	back := Load(path)
	if got := back.Dates(key); len(got) != 2 || got[0] != "2026-11-20" {
		t.Fatal(got)
	}
	if len(Load(path+".missing").Dates(key)) != 0 {
		t.Fatal("missing file")
	}
	// An unset clock accepts any valid date.
	if !Plausible("2030-01-01", time.Unix(0, 0)) {
		t.Fatal("old clock")
	}
}

func TestAccountLimit(t *testing.T) {
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	s := Load("")
	for i := 0; i < MaxAccounts; i++ {
		s.Accounts[fmt.Sprintf("%016x", i)] = []string{fmt.Sprintf("2026-01-%02d", i+1)}
	}
	s.Record(AccountKey("new@x.y"), "2026-11-21", now)
	if len(s.Accounts) != MaxAccounts {
		t.Fatal(len(s.Accounts))
	}
	if _, ok := s.Accounts[fmt.Sprintf("%016x", 0)]; ok {
		t.Fatal("oldest kept")
	}
	os.Remove("")
}
