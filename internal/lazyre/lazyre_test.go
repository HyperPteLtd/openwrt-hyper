// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package lazyre

import "testing"

func TestNew(t *testing.T) {
	compiled := 0
	re := New(`^a+$`)
	for i := 0; i < 3; i++ {
		if r := re(); r.MatchString("aaa") {
			compiled++
		}
	}
	if compiled != 3 || re() != re() {
		t.Fatal("not compiled once")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("bad pattern accepted")
		}
	}()
	New(`(`)()
}
