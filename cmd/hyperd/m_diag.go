// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mihomo"
	"github.com/HyperPteLtd/openwrt-hyper/internal/update"
)

var diagTarget = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)

// Log types: "core", "hyper", "combined".
func (m *manager) logFiles(kind string) ([]string, bool) {
	switch kind {
	case "", "combined":
		return []string{m.paths.Log, m.paths.CoreLog}, true
	case "hyper":
		return []string{m.paths.Log}, true
	case "core":
		return []string{m.paths.CoreLog}, true
	}
	return nil, false
}

func (m *manager) registerDiag() {
	m.handlers["traffic"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct {
			PerSource *bool `json:"per_source"`
		}
		_ = jsonArgs(args, &a)
		api := m.svc.controller()
		if api == nil || !m.svc.isRunning() {
			return map[string]any{"running": false}, nil
		}
		perSource := a.PerSource == nil || *a.PerSource
		read := api.Connections
		if !perSource {
			read = api.Totals // the overview polls totals every few seconds
		}
		conns, err := read(ctx)
		if err != nil {
			return map[string]any{"running": true, "error": err.Error()}, nil
		}
		out := map[string]any{"running": true, "upload_total": conns.UploadTotal, "download_total": conns.DownloadTotal, "memory": conns.Memory, "per_source": nil}
		if perSource {
			type src struct {
				Upload      uint64 `json:"upload"`
				Download    uint64 `json:"download"`
				Connections int    `json:"connections"`
			}
			per := map[string]*src{}
			for _, c := range conns.Connections {
				ip := c.Metadata.SourceIP
				if ip == "" {
					continue
				}
				if per[ip] == nil {
					per[ip] = &src{}
				}
				per[ip].Upload += c.Upload
				per[ip].Download += c.Download
				per[ip].Connections++
			}
			out["per_source"] = per
		}
		return out, nil
	}

	m.handlers["logs"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct {
			Type  string
			Lines int
		}
		_ = jsonArgs(args, &a)
		files, ok := m.logFiles(a.Type)
		if !ok {
			return map[string]any{"error": "illegal type"}, nil
		}
		n := a.Lines
		if n == 0 {
			n = 200
		}
		n = min(max(n, 10), 1000)
		var sources []string
		for _, f := range files {
			text := tail(f, n)
			if f == m.paths.CoreLog {
				text = coreLogText(text)
			}
			sources = append(sources, text)
		}
		return map[string]any{"content": mergeLogs(sources, n)}, nil
	}
	m.handlers["log_clean"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ Type string }
		_ = jsonArgs(args, &a)
		files, ok := m.logFiles(a.Type)
		if !ok {
			return map[string]any{"result": false, "error": "illegal type"}, nil
		}
		for _, f := range files {
			if _, err := os.Stat(f); err == nil {
				_ = os.Truncate(f, 0)
			}
		}
		return map[string]any{"result": true}, nil
	}

	m.handlers["diag"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct{ Type, Target, Via string }
		_ = jsonArgs(args, &a)
		if !diagTarget().MatchString(a.Target) || len(a.Target) > 253 {
			return map[string]any{"result": false, "error": "illegal target"}, nil
		}
		s, _ := m.settings()
		var name string
		var argv []string
		switch a.Type {
		case "ping":
			name, argv = "ping", []string{"-c", "4", "-W", "2", a.Target}
		case "traceroute":
			name, argv = "traceroute", []string{"-w", "2", "-q", "1", "-m", "15", a.Target}
		case "nslookup":
			name, argv = "nslookup", []string{a.Target, "127.0.0.1"}
		case "http":
			name, argv = "curl", []string{"-o", "/dev/null", "-sS", "-m", "10", "-w",
				"HTTP %{http_code} | connect %{time_connect}s | tls %{time_appconnect}s | ttfb %{time_starttransfer}s\n"}
			if a.Via != "direct" && m.svc.isRunning() {
				argv = append(argv, "-x", fmt.Sprintf("socks5h://127.0.0.1:%d", s.MixedPort))
			}
			argv = append(argv, "--", "https://"+a.Target)
		default:
			return map[string]any{"result": false, "error": "illegal type"}, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, err := m.sys.Run(ctx, name, argv...)
		return map[string]any{"result": err == nil, "output": string(out)}, nil
	}

	m.handlers["health_check"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		s, _ := m.settings()
		running := m.svc.isRunning()
		// While the core runs, every site goes through it, so the rules decide the route:
		// mainland sites should go direct and the rest through the proxy. Otherwise straight out.
		proxy := ""
		if running {
			proxy = fmt.Sprintf("socks5://127.0.0.1:%d", s.MixedPort)
		}
		probe := func(url string) map[string]any {
			c, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(c, http.MethodHead, url, nil)
			start := m.sys.Now()
			res, err := m.sys.HTTP(proxy).Do(req)
			if err != nil {
				return map[string]any{"ok": false, "ms": nil}
			}
			res.Body.Close()
			return map[string]any{"ok": res.StatusCode < 400, "ms": m.sys.Now().Sub(start).Milliseconds()}
		}
		out := map[string]any{"service": map[string]any{"ok": running}, "node": nil, "via_core": running}
		var mu sync.Mutex
		var wg sync.WaitGroup
		set := func(k string, v any) { mu.Lock(); out[k] = v; mu.Unlock() }
		sites := map[string]any{}
		for _, site := range healthSites {
			wg.Go(func() {
				r := probe(site.url)
				mu.Lock()
				sites[site.name] = r
				mu.Unlock()
			})
		}
		wg.Go(func() {
			if api := m.svc.controller(); api != nil && running {
				ms, err := api.Delay(ctx, mihomo.MainGroup, probeURL, 3*time.Second)
				if err == nil {
					set("node", map[string]any{"delay": ms})
				} else {
					set("node", map[string]any{"message": err.Error()})
				}
			}
		})
		wg.Go(func() {
			c, cancel := context.WithTimeout(ctx, 6*time.Second)
			_, err := m.sys.Run(c, "nslookup", "www.google.com", "127.0.0.1")
			cancel()
			set("dns", map[string]any{"ok": err == nil})
		})
		wg.Wait()
		out["sites"] = sites
		return out, nil
	}
}

// healthSites are checked on both sides of the rules, mainland first.
var healthSites = []struct{ name, url string }{
	{"baidu", "https://www.baidu.com/"}, {"netease", "https://music.163.com/"},
	{"google", "https://www.google.com/generate_204"}, {"youtube", "https://www.youtube.com/generate_204"},
	{"github", "https://github.com/"},
}

// tail returns the last n lines of a file (reads at most 1 MiB).
func tail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	off := max(st.Size()-1<<20, 0)
	f.Seek(off, io.SeekStart)
	b, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var (
	logfmtField = lazyre.New(`(\w+)=("(?:[^"\\]|\\.)*"|\S*)`)
	coreLevels  = map[string]string{"debug": "DEBUG", "info": "INFO", "warning": "WARN", "warn": "WARN", "error": "ERROR", "fatal": "ERROR"}
)

// coreLogText rewrites mihomo's logfmt lines (time="RFC3339" level=warning msg="...") as
// "2006-01-02 15:04:05 WARN ..." so the Logs page shows their time and severity.
// Other lines (panics, Go runtime output) pass through.
func coreLogText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "time=") {
			continue
		}
		f := map[string]string{}
		for _, m := range logfmtField().FindAllStringSubmatch(line, -1) {
			v := m[2]
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
			f[m[1]] = v
		}
		t, err := time.Parse(time.RFC3339Nano, f["time"])
		level := coreLevels[f["level"]]
		if err != nil || level == "" {
			continue
		}
		lines[i] = t.Format("2006-01-02 15:04:05") + " " + level + " " + f["msg"]
	}
	return strings.Join(lines, "\n")
}

var logStamp = lazyre.New(`(^|\s|time=")(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})`)

// mergeLogs interleaves sources by timestamp; lines without one stay with their record.
func mergeLogs(sources []string, limit int) string {
	type record struct {
		time  string
		order int
		text  string
	}
	var recs []*record
	seq := 0
	for _, src := range sources {
		var cur *record
		for _, line := range strings.Split(src, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			mt := logStamp().FindStringSubmatch(line)
			if mt == nil && cur != nil {
				cur.text += "\n" + line
				continue
			}
			cur = &record{order: seq, text: line}
			if mt != nil {
				cur.time = mt[2] + " " + mt[3]
			}
			seq++
			recs = append(recs, cur)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].time != recs[j].time {
			return recs[i].time < recs[j].time
		}
		return recs[i].order < recs[j].order
	})
	if len(recs) > limit {
		recs = recs[len(recs)-limit:]
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.text
	}
	return strings.Join(out, "\n")
}

func (m *manager) updater() *update.Updater {
	c, _ := m.config()
	return &update.Updater{Dir: m.paths.Update, Repo: c.Get("update", "repo"), Token: c.Get("config", "github_token"),
		Exists: m.sys.Exists, Run: m.sys.Run,
		// GitHub is often unreachable directly from mainland networks: retry through the core.
		Get: func(ctx context.Context, url string, h http.Header) (*http.Response, error) {
			get := func(proxy string) (*http.Response, error) {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return nil, err
				}
				req.Header = h.Clone()
				hc := m.sys.HTTP(proxy)
				hc.CheckRedirect = nil // release assets redirect to the CDN
				return hc.Do(req)
			}
			res, err := get("")
			if err != nil {
				if proxy := m.svc.proxyURL(); proxy != "" {
					return get(proxy)
				}
			}
			return res, err
		}}
}

func (m *manager) registerUpdate() {
	m.handlers["check_update"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return m.checkUpdate(ctx), nil
	}
	m.handlers["install_update"] = func(ctx context.Context, args json.RawMessage) (any, error) {
		var a struct{ Force, Automatic bool }
		_ = jsonArgs(args, &a)
		return m.installUpdate(ctx, a.Force, a.Automatic), nil
	}
}

func (m *manager) checkUpdate(ctx context.Context) map[string]any {
	u := m.updater()
	_ = os.MkdirAll(m.paths.Update, 0o700)
	rel, err := u.Latest(ctx)
	check := map[string]any{"time": m.sys.Now().Unix(), "latest": nil}
	if err == nil {
		check["latest"] = rel.Version
	}
	b, _ := json.Marshal(check)
	_ = os.WriteFile(filepath.Join(m.paths.Update, "check.json"), append(b, '\n'), 0o600)
	if err != nil {
		m.log.Errorf("UPDATE", "Checking for an update failed: %v", err)
		return map[string]any{"result": false, "error": updateCode(err)}
	}
	pm := u.Manager(ctx)
	current, _ := m.packageVersion(ctx).(string)
	pkg, _ := update.Pick(rel.Assets, pm)
	notes := rel.Body
	if len(notes) > 4000 {
		notes = notes[:4000]
	}
	var st any
	if s := update.ReadState(m.paths.Update); s != nil {
		st = s
	}
	return map[string]any{"result": true, "current": nullable(current), "latest": rel.Version,
		"update_available": current == "" || update.Compare(rel.Version, current) > 0,
		"published_at":     rel.PublishedAt, "notes": notes, "url": rel.URL, "installable": pkg != nil,
		"package_manager": pm.Name, "update_state": st}
}

func (m *manager) installUpdate(ctx context.Context, force, automatic bool) map[string]any {
	fail := func(e string) map[string]any { return map[string]any{"result": false, "error": e} }
	c, _ := m.config()
	if automatic && c.Get("update", "auto_update") != "1" {
		return fail("automatic_update_disabled")
	}
	if err := os.MkdirAll(m.paths.Update, 0o700); err != nil {
		return fail("update_failed")
	}
	lock, err := os.OpenFile(filepath.Join(m.paths.Update, "install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fail("update_failed")
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail("update_in_progress")
	}
	if st := update.ReadState(m.paths.Update); st != nil && st.State == "installing" && m.sys.Now().Unix()-st.Time < 1800 {
		return fail("update_in_progress")
	}
	u := m.updater()
	rel, err := u.Latest(ctx)
	if err != nil {
		m.log.Errorf("UPDATE", "Checking for an update failed: %v", err)
		return fail(updateCode(err))
	}
	current, _ := m.packageVersion(ctx).(string)
	if !force && current != "" && update.Compare(rel.Version, current) <= 0 {
		return fail("up_to_date")
	}
	job, err := u.Prepare(ctx, rel, u.Manager(ctx), automatic)
	if err != nil {
		m.log.Errorf("UPDATE", "Downloading %s failed: %v", rel.Version, err)
		return fail(updateCode(err))
	}
	if c, _ := m.config(); automatic && c.Get("update", "auto_update") != "1" {
		os.RemoveAll(filepath.Join(m.paths.Update, "download"))
		return fail("automatic_update_disabled")
	}
	b, _ := json.Marshal(job)
	if os.WriteFile(filepath.Join(m.paths.Update, "install.json"), b, 0o600) != nil ||
		update.WriteState(m.paths.Update, update.State{State: "installing", Version: rel.Version, Time: m.sys.Now().Unix()}) != nil {
		return fail("update_failed")
	}
	// The package replaces hyperd and restarts the service: install from a detached
	// process in its own session so procd's restart does not kill it.
	cmd := exec.Command(m.paths.Self, "install-update", m.paths.Update)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		update.WriteState(m.paths.Update, update.State{State: "failed", Version: rel.Version, Time: m.sys.Now().Unix(), Error: "update_failed"})
		return fail("update_failed")
	}
	go cmd.Wait()
	return map[string]any{"result": true, "started": true, "version": rel.Version}
}

// updateCode is what the page reads of a failed update check or download; hyperd's log
// keeps the cause.
func updateCode(err error) string {
	var status update.StatusError
	var unreachable *url.Error
	switch {
	case errors.Is(err, update.ErrRepo):
		return "update_repo_invalid"
	case errors.Is(err, update.ErrNoPackage):
		return "update_no_package"
	case errors.Is(err, update.ErrChecksum):
		return "update_checksum_failed"
	case errors.As(err, &status) && (status == 403 || status == 429):
		return "update_rate_limited" // GitHub's limit for unauthenticated requests
	case errors.As(err, &status) && status == 404:
		return "update_release_missing"
	case errors.As(err, &status):
		return "update_server_error"
	case errors.As(err, &unreachable), errors.Is(err, context.DeadlineExceeded):
		return "update_unreachable"
	}
	return "update_failed"
}

// autoUpdate is the daily check (and install when enabled); force skips the 24h wait.
func (m *manager) autoUpdate(ctx context.Context, force bool) {
	c, _ := m.config()
	check, auto := c.Get("update", "auto_check") != "0", c.Get("update", "auto_update") == "1"
	if !check && !auto {
		return
	}
	var prev struct {
		Time int64 `json:"time"`
	}
	readJSONFile(filepath.Join(m.paths.Update, "check.json"), &prev)
	now := m.sys.Now().Unix()
	if !force && prev.Time > 0 && now >= prev.Time && now-prev.Time < 86400 {
		return
	}
	res := m.checkUpdate(ctx)
	if res["result"] != true || res["update_available"] != true || res["installable"] != true {
		return
	}
	if c, _ := m.config(); c.Get("update", "auto_update") == "1" {
		m.installUpdate(ctx, false, true)
	}
}

// installUpdateMain is `hyperd install-update DIR`, the detached installer.
func installUpdateMain(ctx context.Context, dir string) error {
	lock, err := os.OpenFile(filepath.Join(dir, "install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	var job update.Job
	if !readJSONFile(filepath.Join(dir, "install.json"), &job) {
		return nil
	}
	os.Remove(filepath.Join(dir, "install.json"))
	paths := DefaultPaths()
	st := update.Install(ctx, dir, job, func() bool {
		m := &manager{paths: paths}
		c, _ := m.config()
		return c.Get("update", "auto_update") == "1"
	})
	if err := update.WriteState(dir, st); err != nil {
		return err
	}
	if st.State == "done" {
		_ = exec.Command("/etc/init.d/rpcd", "reload").Run()
	}
	return nil
}
