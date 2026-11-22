// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestFileLimitFallbacks(t *testing.T) {
	for _, c := range []struct {
		nrOpen string
		hard   uint64
		want   string
	}{
		{"1048576\n", 4096, "[4096]"},       // nr_open above n: what is held
		{"524288\n", 4096, "[524288 4096]"}, // nr_open below n: the most root may hold, then what is held
		{"524288\n", 524288, "[524288]"},    // never twice
		{"", 4096, "[4096]"},                // nr_open unreadable
		{"x", 0, "[]"},                      // a hard limit of 0 would leave no descriptor
		{"0\n", 2000000, "[]"},              // neither below n
	} {
		if got := fmt.Sprint(fileLimitFallbacks(coreFiles, []byte(c.nrOpen), c.hard)); got != c.want {
			t.Errorf("nr_open %q, hard %d: %s, want %s", c.nrOpen, c.hard, got, c.want)
		}
	}
}

// Whatever limit serve ends up with, the core inherits it: Go no longer hands children the
// soft limit the process started with.
func TestSetFileLimitReachesChildren(t *testing.T) {
	err := setFileLimit(coreFiles)
	var held syscall.Rlimit
	if e := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &held); e != nil {
		t.Fatal(e)
	}
	if err == nil && held.Cur < coreFiles {
		// Only fs.nr_open may hold it below coreFiles without an error.
		t.Logf("held %d (fs.nr_open)", held.Cur)
	}
	if held.Cur != held.Max {
		t.Fatalf("soft %d, hard %d: the soft limit was not raised to the hard one", held.Cur, held.Max)
	}
	out, e := exec.Command("sh", "-c", "ulimit -Sn; ulimit -Hn").Output()
	if e != nil {
		t.Skip("no sh:", e)
	}
	if got, want := strings.Fields(string(out)), []string{fmt.Sprint(held.Cur), fmt.Sprint(held.Max)}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("child holds %v, serve %v", got, want)
	}
}
