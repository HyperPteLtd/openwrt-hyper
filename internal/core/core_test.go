// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeBin writes a shell script standing in for mihomo.
func fakeBin(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mihomo")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheck(t *testing.T) {
	ok := Core{Bin: fakeBin(t, `[ "$1" = -t ] && [ "$2" = -d ] && [ "$4" = -f ] && echo "test is successful"`), Dir: "/d", Config: "/c.yaml"}
	if err := ok.Check(context.Background()); err != nil {
		t.Fatalf("Check ok: %v", err)
	}
	bad := Core{Bin: fakeBin(t, `echo noise; echo "proxy 0: unsupport proxy type: naive" >&2; exit 1`), Dir: "/d", Config: "/c.yaml"}
	err := bad.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupport proxy type: naive") {
		t.Fatalf("Check bad: %v", err)
	}
}

func TestSupervisorRestartsAndStops(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	// Crash twice, then stay up.
	bin := fakeBin(t, `echo x >> "`+count+`"; [ "$(wc -l < "`+count+`")" -ge 3 ] && exec sleep 60; exit 3`)
	var exits atomic.Int32
	s := &Supervisor{Core: Core{Bin: bin, Dir: "/d", Config: "/c"}, Backoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		OnExit: func(error) { exits.Add(1) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for !s.State().Running || s.State().Restarts < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("core never stabilised: %+v", s.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := s.State(); exits.Load() != 2 || !strings.Contains(st.LastExit, "exit status 3") || st.PID == 0 {
		t.Fatalf("state %+v, exits %d", st, exits.Load())
	}
	pid := s.State().PID
	cancel()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	if s.State().Running {
		t.Fatal("still marked running")
	}
	if err := syscallKill0(pid); err == nil {
		t.Fatal("core process survived shutdown")
	}
}

func TestCredential(t *testing.T) {
	if c := (Core{}).credential(); c != nil {
		t.Fatal("no group, yet another identity:", c)
	}
	c := (Core{Group: 991}).credential()
	if c == nil || c.Uid != 0 || c.Gid != 991 || c.Groups == nil || len(c.Groups) != 0 || c.NoSetGroups {
		t.Fatalf("not root in group 991 alone: %+v", c)
	}
}

// A supervisor that dies (SIGKILL, no cleanup) takes its core with it.
func TestCoreDiesWithSupervisor(t *testing.T) {
	if os.Getenv("HYPER_CORE_ORPHAN_TEST") == "1" {
		bin := os.Args[len(os.Args)-1]
		s := &Supervisor{Core: Core{Bin: bin}}
		s.Run(context.Background())
		return
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	bin := fakeBin(t, "echo $$ > "+pidFile+"; exec sleep 60")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoreDiesWithSupervisor$", "--", bin)
	cmd.Env = append(os.Environ(), "HYPER_CORE_ORPHAN_TEST=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		cmd.Process.Kill()
		t.Fatal("core never started")
	}
	cmd.Process.Kill() // the supervisor crashes
	cmd.Wait()
	for i := 0; i < 100; i++ {
		if syscallKill0(pid) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("core outlived its supervisor")
}
