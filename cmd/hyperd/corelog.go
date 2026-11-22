// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ordinaryCore marks core warnings that normal operation produces all the time (OpenClash
// shows them only in its raw core log): one connection or packet
// failing, which the core retries and reports up to ten times per connection, a group
// starting its health check and the core stopping. Matched in lower case.
var ordinaryCore = [][]byte{[]byte("[tcp] dial "), []byte("[udp] dial "), []byte("[udp] resolve ip error"),
	[]byte("[udp] destination ip not valid"), []byte("[udp] dosniff error"), []byte("[udp] parse metadata failed"),
	[]byte("[metadata] "), []byte("[icmp] failed to connect"), []byte("isn't a *net.udpaddr"), []byte("nil *net.udpaddr"),
	[]byte("activate health check"), []byte("mihomo shutting down")}

// Kept even when ordinary: the system refusing the core is never routine, nor is an error
// that takes the core down, nor running out of descriptors (EMFILE for the core, ENFILE for
// the whole system): the core then can neither accept nor open
// connections, which browsers only report as connections closed unexpectedly.
var keptCore = [][]byte{[]byte("permission denied"), []byte("operation not permitted"), []byte("too many open files"),
	[]byte("level=fatal"), []byte("level=error")}

// crashKept is how many lines after a Go crash's first line are kept: enough for the
// failing goroutine's stack, which ends at its first blank line.
const crashKept = 40

// coreLog writes the core's output line by line, leaving out ordinary warnings.
//
// A Go crash (panic:, fatal error:, or a signal such as SIGSEGV: segmentation violation)
// is written as a dated fatal entry, so the Logs page shows it on its own line instead of
// folding it into the entry before, followed by the failing goroutine's stack; the rest of
// the dump (every other goroutine, often hundreds) is left out until the core logs again,
// so a crash is logged once. The runtime's own runtime: lines (out of memory, thread
// exhaustion) are dated errors too.
type coreLog struct {
	mu      sync.Mutex
	w       io.Writer
	now     func() time.Time // crash entries' time; time.Now when nil
	partial []byte
	crash   int  // crash lines still kept (> 0), or -1 while leaving out the rest of a dump
	stack   bool // the kept crash lines reached a goroutine's stack
	last    string
}

func (c *coreLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.partial = append(c.partial, p...)
	var out []byte
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			break
		}
		out = append(out, c.line(c.partial[:i+1])...)
		c.partial = c.partial[i+1:]
	}
	// A line longer than any the core writes is passed on as it is.
	if len(c.partial) > 64<<10 {
		out, c.partial = append(out, c.partial...), nil
	}
	c.partial = append([]byte(nil), c.partial...)
	if len(out) > 0 {
		if _, err := c.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close writes what is left of an unfinished last line.
func (c *coreLog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.partial) > 0 {
		if out := c.line(c.partial); len(out) > 0 {
			_, _ = c.w.Write(out)
		}
	}
	c.partial = nil
	return nil
}

// terminal is the last error the core wrote (an error or fatal line, or a crash's first
// line): why a core that just ended did. Empty without a log.
func (c *coreLog) terminal() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// line returns what to write of one line of the core's output.
func (c *coreLog) line(line []byte) []byte {
	text := bytes.TrimRight(line, "\r\n")
	if bytes.HasPrefix(text, []byte("time=")) {
		c.crash, c.stack = 0, false
		lower := bytes.ToLower(text)
		if bytes.Contains(lower, []byte("level=error")) || bytes.Contains(lower, []byte("level=fatal")) {
			c.note(logfmtMessage(string(text)))
		}
		if ordinaryLine(line) {
			return nil
		}
		return line
	}
	if crashStart(text) {
		c.crash, c.stack = crashKept, false
		c.note(string(text))
		return c.dated("fatal", text)
	}
	switch {
	case c.crash > 0:
		blank := len(bytes.TrimSpace(text)) == 0
		if blank && c.stack {
			c.crash = -1
			return nil
		}
		c.stack = c.stack || bytes.HasPrefix(text, []byte("goroutine "))
		if c.crash--; c.crash == 0 {
			c.crash = -1
		}
		return line
	case c.crash < 0:
		return nil
	case bytes.HasPrefix(text, []byte("runtime: ")) || bytes.HasPrefix(text, []byte("runtime/cgo: ")):
		c.note(string(text))
		return c.dated("error", text)
	}
	if ordinaryLine(line) {
		return nil
	}
	return line
}

func (c *coreLog) note(msg string) {
	if msg = strings.TrimSpace(msg); msg != "" {
		if len(msg) > 300 {
			msg = msg[:300]
		}
		c.last = msg
	}
}

// dated writes a line the way the core logs, so the Logs page reads its time and severity.
func (c *coreLog) dated(level string, text []byte) []byte {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	return fmt.Appendf(nil, "time=%q level=%s msg=%s\n", now().Format(time.RFC3339Nano), level, strconv.Quote(string(text)))
}

// crashStart reports the first line of a Go crash report.
func crashStart(text []byte) bool {
	if bytes.HasPrefix(text, []byte("panic: ")) || bytes.HasPrefix(text, []byte("fatal error: ")) {
		return true
	}
	// SIGSEGV: segmentation violation, SIGABRT: abort, ...
	name, _, ok := bytes.Cut(text, []byte(": "))
	if !ok || len(name) < 4 || !bytes.HasPrefix(name, []byte("SIG")) {
		return false
	}
	for _, b := range name[3:] {
		if b < 'A' || b > 'Z' {
			return false
		}
	}
	return true
}

// logfmtMessage is the msg= field of a core log line.
func logfmtMessage(line string) string {
	for _, m := range logfmtField().FindAllStringSubmatch(line, -1) {
		if m[1] == "msg" {
			if u, err := strconv.Unquote(m[2]); err == nil {
				return u
			}
			return m[2]
		}
	}
	return ""
}

func ordinaryLine(line []byte) bool {
	lower := bytes.ToLower(line)
	for _, k := range keptCore {
		if bytes.Contains(lower, k) {
			return false
		}
	}
	for _, o := range ordinaryCore {
		if bytes.Contains(lower, o) {
			return true
		}
	}
	return false
}
