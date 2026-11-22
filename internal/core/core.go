// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package core runs the bundled mihomo binary: config checks and a supervised process.
package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DefaultBin = "/usr/libexec/hyper/bin/mihomo"

// Core describes one mihomo instance: binary, working dir (-d) and config file (-f).
type Core struct {
	Bin    string
	Dir    string
	Config string
	Env    []string
	// Log receives the core's stdout/stderr; nil discards.
	Log io.Writer
	// Group, when not 0, runs mihomo (runs and checks) as OpenClash runs its core:
	// root's user ID with every capability, no sandbox and no no_new_privs, but
	// Group as its only group, real, effective, saved and file-system. The firewall lets
	// that group's sockets out uncaptured.
	Group uint32
}

func (c Core) args() []string { return []string{"-d", c.Dir, "-f", c.Config} }

// credential runs the core as root in Group alone (nil: with this process's identity). A
// non-nil empty Groups runs setgroups(0).
func (c Core) credential() *syscall.Credential {
	if c.Group == 0 {
		return nil
	}
	return &syscall.Credential{Uid: 0, Gid: c.Group, Groups: []uint32{}}
}

// Check runs `mihomo -t` and returns the core's own message on failure.
func (c Core) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Checked with the identity that will run it.
	cmd := exec.CommandContext(ctx, c.Bin, append([]string{"-t"}, c.args()...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: c.credential()}
	cmd.Env = append(os.Environ(), c.Env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(lastLines(out, 5)))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("config check failed: %s", msg)
}

func lastLines(b []byte, n int) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return bytes.Join(lines, []byte("\n"))
}

// State of a supervised core.
type State struct {
	Running  bool      `json:"running"`
	PID      int       `json:"pid,omitempty"`
	Since    time.Time `json:"since,omitempty"`
	Restarts int       `json:"restarts"`
	LastExit string    `json:"last_exit,omitempty"`
}

// Supervisor keeps one core process running until its context ends.
// Crashes are restarted with exponential backoff (Backoff..MaxBackoff); a run that
// stays up longer than Stable resets the backoff.
type Supervisor struct {
	Core       Core
	Backoff    time.Duration
	MaxBackoff time.Duration
	Stable     time.Duration
	// OnExit is called after each unexpected exit (e.g. to release firewall rules).
	OnExit func(error)

	mu    sync.Mutex
	state State
}

func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Supervisor) set(f func(*State)) {
	s.mu.Lock()
	f(&s.state)
	s.mu.Unlock()
}

// Run blocks until ctx is cancelled; the core gets SIGTERM, then SIGKILL after 5s.
func (s *Supervisor) Run(ctx context.Context) error {
	backoff, maxBackoff, stable := s.Backoff, s.MaxBackoff, s.Stable
	if backoff <= 0 {
		backoff = time.Second
	}
	if maxBackoff < backoff {
		maxBackoff = 30 * time.Second
	}
	if stable <= 0 {
		stable = time.Minute
	}
	wait := backoff
	for {
		started := time.Now()
		err := s.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		s.set(func(st *State) {
			st.Running, st.PID = false, 0
			st.Restarts++
			if err != nil {
				st.LastExit = err.Error()
			} else {
				st.LastExit = "exited"
			}
		})
		if s.OnExit != nil {
			s.OnExit(err)
		}
		if time.Since(started) >= stable {
			wait = backoff
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, maxBackoff)
	}
}

func (s *Supervisor) once(ctx context.Context) error {
	// The core dies with its supervisor: a crashed manager must not leave it holding the
	// controller and listener ports (the next start would talk to the stale core). Go arms
	// the parent-death signal after the group change, which would clear it. The signal
	// follows the thread that started the core, not the process, so that thread stays this
	// goroutine's, and alive, until the core has exited. Its own process
	// group lets the supervisor stop the core as a whole.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := exec.Command(s.Core.Bin, s.Core.args()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL, Credential: s.Core.credential()}
	cmd.Env = append(os.Environ(), s.Core.Env...)
	cmd.Stdout, cmd.Stderr = s.Core.Log, s.Core.Log
	if err := cmd.Start(); err != nil {
		return err
	}
	s.set(func(st *State) { st.Running, st.PID, st.Since = true, cmd.Process.Pid, time.Now() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		s.set(func(st *State) { st.Running, st.PID = false, 0 })
		return nil
	}
}
