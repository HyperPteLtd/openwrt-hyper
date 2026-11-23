// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
)

// The plugin's own updates: /usr/libexec/hyper/package-update checks the official release
// and installs the package for this router. hyperd checks once a day (hyper.main.auto_check)
// and, with hyper.main.auto_update, installs what it finds, as 0.x did.

// updaterPath and updaterRun (where an installation's output goes) are the router's; tests
// replace them.
var updaterPath, updaterRun = "/usr/libexec/hyper/package-update", router.RunDir

// pluginState is what the router last learned of a newer plugin version, for the overview.
type pluginState struct {
	Installed string `json:"installed,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Available bool   `json:"available"`
	CheckedAt int64  `json:"checked_at,omitempty"` // ms
	Error     string `json:"error,omitempty"`
}

// updaterFailure is the updater's last line when it is one of its fixed reasons.
func updaterFailure(stderr []byte) error {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); updaterReasons[last] {
		return errors.New(last)
	}
	return errors.New("verified plugin update failed")
}

// runUpdater checks, or installs ("apply"). The installation replaces this daemon, whose
// service the package restarts: it runs in its own session with its output in files, so
// neither the restart nor this process ending can cut the package manager short.
func runUpdater(ctx context.Context, action string) (json.RawMessage, error) {
	if action == "check" {
		out, e := exec.CommandContext(ctx, updaterPath, "check").Output()
		if e != nil {
			var exit *exec.ExitError
			if errors.As(e, &exit) {
				return nil, updaterFailure(exit.Stderr)
			}
			return nil, errors.New("verified plugin update failed")
		}
		if !json.Valid(out) {
			return nil, errors.New("verified plugin update failed")
		}
		return out, nil
	}
	files := map[string]*os.File{}
	for _, name := range []string{"plugin-update.out", "plugin-update.log"} {
		f, e := os.OpenFile(filepath.Join(updaterRun, name), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		files[name] = f
	}
	cmd := exec.Command(updaterPath, "apply")
	cmd.Stdout, cmd.Stderr = files["plugin-update.out"], files["plugin-update.log"]
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e := cmd.Start(); e != nil {
		return nil, e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case e := <-done:
		out, _ := os.ReadFile(files["plugin-update.out"].Name())
		if e != nil {
			stderr, _ := os.ReadFile(files["plugin-update.log"].Name())
			return nil, updaterFailure(stderr)
		}
		if !json.Valid(out) {
			return nil, errors.New("verified plugin update failed")
		}
		return out, nil
	}
}

// pluginJob runs the page's check or installation, or the daily one (plugin_auto: check, and
// install with auto, as the switches were when it started).
func (s *app) pluginJob(method string, install bool) (any, error) {
	return s.runJob("plugin", method, func(ctx context.Context) (json.RawMessage, error) {
		action := "check"
		if method == "plugin_update" {
			action = "apply"
		}
		out, e := runUpdater(ctx, action)
		s.notePlugin(out, e)
		if e == nil && method == "plugin_auto" && install && s.pluginAvailable() {
			out, e = runUpdater(ctx, "apply")
			s.notePlugin(out, e)
		}
		return out, e
	})
}

func (s *app) notePlugin(out json.RawMessage, e error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugin.CheckedAt, s.plugin.Error = time.Now().UnixMilli(), ""
	if e != nil {
		s.plugin.Error = e.Error()
		return
	}
	var r struct {
		Installed string `json:"installed"`
		Candidate string `json:"candidate"`
		Available bool   `json:"available"`
	}
	if json.Unmarshal(out, &r) == nil && r.Installed != "" {
		s.plugin.Installed, s.plugin.Candidate, s.plugin.Available = r.Installed, r.Candidate, r.Available
	}
}

func (s *app) pluginAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plugin.Available
}

// pluginStatus is the last check, for status; nothing before the first.
func (s *app) pluginStatus() *pluginState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plugin.CheckedAt == 0 {
		return nil
	}
	p := s.plugin
	return &p
}

// keepPluginCurrent checks daily, the first time a while after start so routers that boot
// together do not ask GitHub at once.
func (s *app) keepPluginCurrent(ctx context.Context) {
	wait := 10*time.Minute + rand.N(50*time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 24 * time.Hour
		p, err := router.ReadPreferences(ctx)
		if err != nil || !*p.AutoCheck && !*p.AutoUpdate {
			continue
		}
		if _, err := s.pluginJob("plugin_auto", *p.AutoUpdate); err != nil {
			// Busy with the page's own check or installation: try again in an hour.
			wait = time.Hour
		}
	}
}
