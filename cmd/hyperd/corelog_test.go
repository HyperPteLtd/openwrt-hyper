// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCoreLogLeavesOutOrdinaryWarnings(t *testing.T) {
	var out bytes.Buffer
	c := &coreLog{w: &out}
	for _, chunk := range []string{
		`time="2026-11-21T10:00:00Z" level=info msg="Start initial configuration"` + "\n" +
			`time="2026-11-21T10:00:01Z" level=warning msg="[TCP] dial main-out (match RuleSet/geosite-cn) 192.168.1.2:50`,
		`00 --> example.com:443 error: connect failed"` + "\n" +
			`time="2026-11-21T10:00:02Z" level=warning msg="[TCP] dial DIRECT 192.168.1.2:5001 --> 1.1.1.1:443 error: dial tcp: operation not permitted"` + "\n" +
			`time="2026-11-21T10:00:02Z" level=warning msg="[TCP] dial DIRECT 192.168.1.2:5002 --> 1.0.0.1:443 error: dial tcp 1.0.0.1:443: socket: too many open files"` + "\n" +
			`time="2026-11-21T10:00:03Z" level=info msg="Proxy group main-out activate health check"` + "\n" +
			`time="2026-11-21T10:00:04Z" level=error msg="tproxy listener failed"`,
	} {
		if n, err := c.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatal(n, err)
		}
	}
	c.Close()
	got := out.String()
	for _, want := range []string{"Start initial configuration", "operation not permitted", "too many open files", "tproxy listener failed"} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	for _, gone := range []string{"example.com", "activate health check"} {
		if bytes.Contains([]byte(got), []byte(gone)) {
			t.Fatalf("kept %q in\n%s", gone, got)
		}
	}
}

// A Go crash is one dated fatal entry with the failing goroutine's stack; the rest of the
// dump stays out until the core logs again, and the crash is the core's last words.
func TestCoreLogCrash(t *testing.T) {
	var out bytes.Buffer
	at := time.Date(2026, 11, 22, 10, 0, 5, 0, time.UTC)
	c := &coreLog{w: &out, now: func() time.Time { return at }}
	dump := `time="2026-11-22T10:00:00Z" level=info msg="Start initial configuration"
runtime: out of memory: cannot allocate 4194304-byte block (3997696 in use)
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x4b1c2d]

goroutine 41 [running]:
github.com/metacubex/mihomo/dns.(*Resolver).exchange(0x0)
	/build/mihomo/dns/resolver.go:123 +0x2d
created by github.com/metacubex/mihomo/dns.NewResolver in goroutine 1
	/build/mihomo/dns/resolver.go:88 +0x1a5

goroutine 1 [select]:
github.com/metacubex/mihomo/tunnel.process()
	/build/mihomo/tunnel/tunnel.go:200 +0x99
time="2026-11-22T10:00:09Z" level=info msg="Start initial configuration"
`
	if _, err := c.Write([]byte(dump)); err != nil {
		t.Fatal(err)
	}
	c.Close()
	got := out.String()
	for _, want := range []string{
		`time="2026-11-22T10:00:05Z" level=error msg="runtime: out of memory: cannot allocate 4194304-byte block (3997696 in use)"`,
		`time="2026-11-22T10:00:05Z" level=fatal msg="panic: runtime error: invalid memory address or nil pointer dereference"`,
		"goroutine 41 [running]:", "resolver.go:88", `time="2026-11-22T10:00:09Z"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, gone := range []string{"goroutine 1 [select]", "tunnel.go:200"} {
		if strings.Contains(got, gone) {
			t.Errorf("kept %q in\n%s", gone, got)
		}
	}
	if why := c.terminal(); why != "panic: runtime error: invalid memory address or nil pointer dereference" {
		t.Errorf("terminal %q", why)
	}
	// The Logs page shows the crash on its own line, as an error.
	if text := coreLogText(got); !strings.Contains(text, "2026-11-22 10:00:05 ERROR panic: runtime error") {
		t.Errorf("Logs page reads\n%s", text)
	}
	var none *coreLog
	if none.terminal() != "" {
		t.Error("no log, yet last words")
	}
}

func TestCrashStart(t *testing.T) {
	for line, want := range map[string]bool{
		"panic: boom": true, "fatal error: concurrent map writes": true, "SIGSEGV: segmentation violation": true,
		"SIGABRT: abort": true, "SIG: x": false, "SIGsegv: x": false, "goroutine 1 [running]:": false, "panicked": false,
	} {
		if crashStart([]byte(line)) != want {
			t.Errorf("%q: %v", line, !want)
		}
	}
}
