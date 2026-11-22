// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// hyperd is the Hyper router manager: it owns the mihomo core, the firewall and DNS
// takeover, and answers the luci.hyper ubus methods for the web UI.
//
//	hyperd serve          long-running manager (procd instance)
//	hyperd guard          fail-open watchdog (separate procd instance)
//	hyperd list           rpcd exec plugin: method signatures
//	hyperd call METHOD    rpcd exec plugin: forward one call to serve
//	hyperd notify-network       hotplug: restart the core only if an interface change altered its config
//	hyperd install-update DIR   detached package installer (started by serve)
//	hyperd run-session CMD...   exec CMD in a new session (setsid for firmware without it)
//	hyperd version
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Version is set at build time (-X main.Version=...).
var Version = "dev"

func main() {
	// Replaces this process (exec), so it runs before anything else starts.
	if len(os.Args) > 2 && os.Args[1] == "run-session" {
		os.Exit(cmdRunSession(os.Args[2:], os.Stderr))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hyperd:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	paths := DefaultPaths()
	switch {
	case len(args) == 1 && args[0] == "serve":
		return serve(ctx, paths)
	case len(args) == 1 && args[0] == "guard":
		return guard(ctx, paths, guardTiming{Tick: 2 * time.Second, Misses: 3})
	case len(args) == 1 && args[0] == "list":
		_, err := os.Stdout.Write(methodsJSON)
		return err
	case len(args) == 2 && args[0] == "call":
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, err := rpcdCall(c, paths.Socket, args[1], os.Stdin)
		if len(out) > 0 {
			os.Stdout.Write(out)
		}
		return err
	case len(args) == 1 && args[0] == "notify-network":
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, err := socketCall(c, paths.Socket, "notify_network", strings.NewReader("{}"))
		if len(out) > 0 {
			os.Stdout.Write(out)
		}
		return err
	case len(args) == 2 && args[0] == "install-update":
		return installUpdateMain(ctx, args[1])
	case len(args) == 1 && args[0] == "version":
		fmt.Println(Version)
		return nil
	}
	return errors.New("usage: hyperd serve | guard | list | call METHOD | notify-network | install-update DIR | run-session CMD... | version")
}
