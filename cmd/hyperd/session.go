// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// Internal setsid replacement for older router builds without that applet.
// Exec preserves our PID so the caller can cancel the complete process group.
func cmdRunSession(args []string, stderr io.Writer) int {
	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "run-session:", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := syscall.Setsid(); err != nil {
		fmt.Fprintln(stderr, "run-session:", err)
		return 126
	}
	if err := syscall.Exec(path, args, os.Environ()); err != nil {
		fmt.Fprintln(stderr, "run-session:", err)
		if errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	return 0 // Successful exec never returns.
}
