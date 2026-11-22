// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunSessionProcess(t *testing.T) {
	if os.Getenv("HYPER_SESSION_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(cmdRunSession(os.Args[i+1:], os.Stderr))
		}
	}
	os.Exit(99)
}

func sessionCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestRunSessionProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "HYPER_SESSION_TEST_PROCESS=1")
	return command
}

func TestRunSessionPreservesArgumentsEnvironmentAndExitCode(t *testing.T) {
	command := sessionCommand(t, "sh", "-c", "printf '%s:%s' \"$1\" \"$SESSION_VALUE\"; exit 37", "sh", "space argument")
	command.Env = append(command.Env, "SESSION_VALUE=present")
	output, err := command.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 37 || string(output) != "space argument:present" {
		t.Fatalf("output=%q, error=%v", output, err)
	}
}

func TestRunSessionUsesCallerPIDAndTerminatesDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	command := sessionCommand(t, "sh", "-c", `echo "$$" > "$1"; sleep 30 & wait`, "sh", marker)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	deadline := time.Now().Add(3 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		content, _ = os.ReadFile(marker)
		if len(content) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid := command.Process.Pid
	if strings.TrimSpace(string(content)) != strconv.Itoa(pid) {
		t.Fatalf("exec changed PID or never ran: marker=%q, parent PID=%d, stderr=%s", content, pid, &stderr)
	}
	group, err := syscall.Getpgid(pid)
	if err != nil || group != pid {
		t.Fatalf("group=%d, want=%d, error=%v", group, pid, err)
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	select {
	case err := <-finished:
		var status *exec.ExitError
		if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Fatalf("expected SIGTERM, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session group did not terminate")
	}
}

func TestRunSessionRejectsMissingOrNonExecutableCommands(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	denied := filepath.Join(t.TempDir(), "denied")
	if err := os.WriteFile(denied, []byte("#!/bin/sh\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		code int
	}{{missing, 127}, {denied, 126}} {
		output, err := sessionCommand(t, tc.path).CombinedOutput()
		var status *exec.ExitError
		if !errors.As(err, &status) || status.ExitCode() != tc.code || !strings.Contains(string(output), "run-session:") {
			t.Errorf("%s: output=%q, error=%v, want=%d", tc.path, output, err, tc.code)
		}
	}
}
