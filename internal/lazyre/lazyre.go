// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package lazyre compiles package-level regular expressions on first use. rpcd starts a
// `hyperd call` process for every UI request (the status poll runs every few seconds);
// compiling dozens of patterns it never uses was most of that process's startup.
package lazyre

import (
	"regexp"
	"sync"
)

// New returns a function that compiles expr once, on its first call, and panics on a bad
// pattern exactly as regexp.MustCompile would.
func New(expr string) func() *regexp.Regexp {
	return sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(expr) })
}
