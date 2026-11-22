// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/core"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mihomo"
)

// operation is a start/stop/restart the UI asked for; ids only grow.
type operation struct {
	ID     int    `json:"id"`
	Action string `json:"action"`
	State  string `json:"state"` // queued | running | done | failed
	Time   int64  `json:"time"`
	Error  string `json:"error,omitempty"`
}

// health is what the top bar shows: failopen = LAN is direct because the core is down.
type health struct {
	State    string `json:"state"` // ok | degraded | failopen | recovering
	Failures int    `json:"failures"`
	Time     int64  `json:"time"`
}

// service owns the core process and the network takeover. Actions run one at a time
// in worker(); a newer request replaces one still queued.
type service struct {
	m    *manager
	mu   sync.Mutex
	op   *operation
	next string
	wake chan struct{}

	running bool // the core is meant to be up
	// starting holds while a start brings up its core and takeover and confirms them: the
	// monitor leaves both to it, or it could take over before the listeners are up.
	starting bool
	sup      *core.Supervisor
	cancel   context.CancelFunc
	done     chan struct{}
	secret   string
	gid      uint32          // the core's group, 0 when it runs as hyperd itself
	api      *control.Client // one client (and connection pool) per running core
	settings mihomo.Settings
	health   *health
	every    time.Duration // monitor interval
	// ready reports whether the core's listeners are up; confirm spaces the four health
	// checks a start must pass.
	ready   func(mihomo.Settings) bool
	confirm time.Duration
	// A failed start is tried again retryAfter later, then twice as long each time (see
	// scheduleRetry); retries counts them since the last request or successful start.
	retryAfter time.Duration
	retries    int
	retry      *time.Timer

	// Firewall repairs (monitor only): tableGone checks the takeover's table, repairs holds
	// the recent ones, and after too many the LAN stays direct until holdUntil.
	tableGone func(string) bool
	repairs   []time.Time
	holdUntil time.Time
}

func newService(m *manager) *service {
	return &service{m: m, wake: make(chan struct{}, 1), every: 5 * time.Second, tableGone: nftTableGone,
		ready: coreListening, confirm: time.Second, retryAfter: 30 * time.Second}
}

// Ten repairs in five minutes, like OpenClash's firewall reloads: hyper never
// fights another tool over the rules for long.
const (
	repairLimit  = 10
	repairWindow = 5 * time.Minute
)

// request queues an action. Start/restart are ignored while the service is disabled,
// so settings writes and late sync results never undo an explicit Stop.
func (s *service) request(action string) *operation {
	if action != "stop" && !s.m.enabled() {
		return nil
	}
	s.m.probes.cancel("")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopRetry()
	id := 1
	if s.op != nil {
		id = s.op.ID + 1
	}
	s.op = &operation{ID: id, Action: action, State: "queued", Time: s.m.sys.Now().Unix()}
	s.next = action
	select {
	case s.wake <- struct{}{}:
	default:
	}
	op := *s.op
	return &op
}

func (s *service) operation() *operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op == nil {
		return nil
	}
	op := *s.op
	if (op.State == "queued" || op.State == "running") && s.m.sys.Now().Unix()-op.Time > 180 {
		op.State, op.Error = "failed", "timeout"
	}
	return &op
}

func (s *service) setOp(state, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op != nil {
		s.op.State, s.op.Error = state, errText
	}
}

func (s *service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		s.mu.Lock()
		action := s.next
		s.next = ""
		if s.op != nil && s.op.Action == action {
			s.op.State = "running"
		}
		s.mu.Unlock()
		if action == "" {
			continue
		}
		var err error
		switch action {
		case "stop":
			s.stop(ctx, "Service stopped.")
		case "start", "restart":
			s.stop(ctx, "")
			if s.m.enabled() && !s.m.stopping() {
				err = s.start(ctx)
			}
		}
		s.mu.Lock()
		superseded := s.next != ""
		s.mu.Unlock()
		if superseded {
			continue
		}
		if err != nil {
			s.m.log.Errorf("DAEMON", "Start failed: %v", err)
			s.setOp("failed", errCode(err))
			if ctx.Err() == nil {
				s.scheduleRetry()
			}
		} else {
			s.setOp("done", "")
			if action != "stop" {
				s.mu.Lock()
				s.retries = 0
				s.mu.Unlock()
			}
		}
	}
}

// scheduleRetry queues the failed start again retryAfter (30 s) later, then twice as long
// each time up to five minutes (the longer waits keep a lasting cause, such as an expired
// plan, from filling the log). A start that failed at boot, or
// whose core or takeover broke right after it, so recovers on its own once its cause goes
// away. Any request cancels it; it starts only while the service stays enabled and the
// failed operation is still the last one.
func (s *service) scheduleRetry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op == nil || s.retryAfter <= 0 {
		return
	}
	id := s.op.ID
	delay := min(s.retryAfter<<min(s.retries, 4), 5*time.Minute)
	s.retries++
	s.stopRetry()
	s.retry = time.AfterFunc(delay, func() { s.retryStart(id) })
}

// stopRetry cancels a pending retry; s.mu is held.
func (s *service) stopRetry() {
	if s.retry != nil {
		s.retry.Stop()
		s.retry = nil
	}
}

func (s *service) retryStart(id int) {
	if s.m.ctx.Err() != nil || !s.m.enabled() || s.m.stopping() {
		return
	}
	s.mu.Lock()
	if s.op == nil || s.op.ID != id || s.op.State != "failed" || s.next != "" || s.running {
		s.mu.Unlock()
		return
	}
	s.retry = nil
	s.op.State, s.op.Error, s.op.Time = "queued", "", s.m.sys.Now().Unix()
	s.next = s.op.Action
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()
	s.m.probes.cancel("")
	s.m.log.Printf("DAEMON", "Trying the failed start again.")
}

// errCode is what the UI reads of a failed operation; the log has the cause.
func errCode(err error) string {
	var re rpcError
	if errors.As(err, &re) {
		return re.Code
	}
	return "start_failed"
}

func (s *service) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.sup != nil && s.sup.State().Running
}

func (s *service) state() (core.State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.sup == nil {
		return core.State{}, false
	}
	return s.sup.State(), true
}

// controller is the running core's API client, nil when stopped.
func (s *service) controller() *control.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	return s.api
}

// proxyURL is the core's loopback mixed port, for retrying blocked downloads.
func (s *service) proxyURL() string {
	if !s.isRunning() {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("socks5://127.0.0.1:%d", s.settings.MixedPort)
}

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var errNoNodes = rpcError{"no_nodes"}

// render builds the core config from the current settings, nodes and lists.
func (m *manager) render(ctx context.Context, s mihomo.Settings, secret string) (*mihomo.Output, error) {
	store, err := m.nodes()
	if err != nil {
		return nil, err
	}
	in := mihomo.Input{Settings: s, Nodes: store.Nodes, Secret: secret,
		Resolver: m.wanResolver(ctx, s.Mode == mihomo.ModeGlobal), Neighbors: m.neighbors(ctx), Self: selfAddrs()}
	if s.Mode == mihomo.ModeSmart {
		if in.Rules, err = mihomo.LoadServerRules(m.paths.rules(), s.DisabledRules); err != nil {
			return nil, err
		}
		in.DirectList, _ = mihomo.LoadList(m.paths.list("direct_list"))
		in.ProxyList, _ = mihomo.LoadList(m.paths.list("proxy_list"))
	}
	out, err := mihomo.Render(in)
	if errors.Is(err, mihomo.ErrNoNodes) {
		return nil, errNoNodes
	}
	return out, err
}

// reclaimDir removes a runtime directory that holds anything uid does not own: an
// earlier build left the core's directory and cache.db in /var/run under the
// core's own account, and the core's files stay root's own. Only a full start calls it,
// before the core runs; the state there is rebuilt (the fake-ip cache starts empty).
func reclaimDir(path string, uid int) error {
	foreign := false
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			foreign = true
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if foreign {
		return os.RemoveAll(path)
	}
	return nil
}

// writeCore replaces the core dir's generated files.
func (m *manager) writeCore(out *mihomo.Output) (string, error) {
	dir := m.paths.CoreDir
	if err := reclaimDir(dir, os.Geteuid()); err != nil {
		return "", fmt.Errorf("cannot reclaim the core's runtime directory: %w", err)
	}
	for _, sub := range []string{"rules", "devices"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	for name, body := range out.Files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return "", err
		}
	}
	// builtin/<list>.mrs: the mainland lists in use, downloaded or packaged.
	if _, err := m.linkLists(); err != nil {
		return "", err
	}
	cfg := filepath.Join(dir, "config.yaml")
	return cfg, os.WriteFile(cfg, out.Config, 0o600)
}

func (s *service) start(ctx context.Context) error {
	m := s.m
	settings, err := m.runtimeSettings(ctx)
	if err != nil {
		return err
	}
	secret := randomSecret()
	out, err := m.render(ctx, settings, secret)
	if err != nil {
		return err
	}
	// The core runs as root in the hyper group, as OpenClash runs its own; its files stay
	// root's own.
	gid, err := coreGroup()
	if err != nil {
		return coded("core_group_missing", err)
	}
	cfg, err := m.writeCore(out)
	if err != nil {
		return err
	}
	// The core inherits serve's open-file limit. procd already applies it to the service;
	// setting it here also stops Go from handing the core its original soft limit.
	if err = setFileLimit(coreFiles); err != nil {
		m.log.Printf("CORE", "WARN open-file limit stays below %d (%v): busy routers may run out.", coreFiles, err)
	}
	c := core.Core{Bin: m.paths.CoreBin, Dir: m.paths.CoreDir, Config: cfg, Env: coreEnv(), Group: gid}
	if err = c.Check(ctx); err != nil {
		return coded("core_config_invalid", err)
	}
	if err = os.WriteFile(m.paths.Secret, []byte(secret+"\n"), 0o600); err != nil {
		return err
	}
	if err = os.WriteFile(m.paths.Secret+".addr", []byte(fmt.Sprintf("127.0.0.1:%d\n", settings.ControlPort)), 0o600); err != nil {
		return err
	}
	if err = m.writePlan(settings, gid); err != nil {
		return err
	}
	// O_APPEND: trimming the log must not leave the core writing at its old offset.
	logf, _ := os.OpenFile(m.paths.CoreLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	var filtered *coreLog
	if logf != nil {
		filtered = &coreLog{w: logf}
		c.Log = filtered
	}
	sup := &core.Supervisor{Core: c, OnExit: func(err error) {
		m.log.Errorf("CORE", "mihomo exited (%v), restarting.", err)
	}}
	runCtx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sup.Run(runCtx)
		if logf != nil {
			filtered.Close()
			logf.Close()
		}
	}()
	s.mu.Lock()
	api := control.New(fmt.Sprintf("127.0.0.1:%d", settings.ControlPort), secret)
	s.running, s.starting, s.sup, s.cancel, s.done, s.secret, s.settings, s.gid, s.api = true, true, sup, cancel, done, secret, settings, gid, api
	s.health = nil
	s.mu.Unlock()
	m.devices.reset(out.Devices)

	version := ""
	deadline := time.Now().Add(15 * time.Second)
	for {
		// A core that exits while starting fails the start at once, with its last words.
		if st := sup.State(); st.Restarts > 0 {
			s.stop(ctx, "")
			return coded("core_exited_on_start", coreExit("mihomo exited while starting", filtered, st.LastExit))
		}
		if version == "" {
			pctx, pcancel := context.WithTimeout(ctx, time.Second)
			version, _ = api.Version(pctx)
			pcancel()
		}
		// The controller can answer before the listeners are up: the
		// takeover waits for them, or LAN connections and lookups would meet closed ports.
		if version != "" && s.ready(settings) {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			s.stop(ctx, "")
			return rpcError{"core_start_failed"}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err = m.network(ctx, "up"); err != nil {
		s.stop(ctx, "")
		return coded("network_takeover_failed", fmt.Errorf("network takeover failed: %w", err))
	}
	if err = s.confirmStart(ctx, sup, api, filtered); err != nil {
		s.stop(ctx, "")
		return err
	}
	s.mu.Lock()
	s.starting = false
	s.mu.Unlock()
	s.setHealth("ok", 0)
	m.log.Printf("DAEMON", "mihomo %s started (%s, %s).", version, settings.Mode, map[bool]string{true: "dns only", false: settings.Method}[settings.DNSOnly])
	return nil
}

// confirmStart holds a streak of four healthy checks, s.confirm apart, before a start
// counts: a core or takeover that comes up and falls over
// at once fails the start here instead of after the page already shows it connected. The
// core exiting fails it at once. Any other failed check starts the streak again and the
// third one fails the start, so a takeover still settling (or a busy router's slow answer)
// recovers while one that stays broken fails within seconds. It counts checks, not time: a
// slow router's slow but healthy checks never run a clock out.
func (s *service) confirmStart(ctx context.Context, sup *core.Supervisor, api *control.Client, log *coreLog) error {
	streak, failures := 0, 0
	for streak < 4 {
		if streak+failures > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.confirm):
			}
		}
		if st := sup.State(); !st.Running || st.Restarts > 0 {
			return coded("core_exited_on_start", coreExit("mihomo exited right after starting", log, st.LastExit))
		}
		err := s.startHealth(ctx, api)
		if err == nil {
			streak++
			continue
		}
		if streak, failures = 0, failures+1; failures == 3 {
			return err
		}
	}
	return nil
}

// startHealth is one check of confirmStart: the core answers and the takeover is in place.
func (s *service) startHealth(ctx context.Context, api *control.Client) error {
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	_, err := api.Version(pctx)
	cancel()
	if err != nil {
		return coded("core_unresponsive_on_start", errors.New("mihomo stopped answering right after starting"))
	}
	if _, err = os.Stat(s.m.paths.Owned); err != nil {
		return coded("takeover_lost_on_start", errors.New("network takeover lost right after starting"))
	}
	return nil
}

// coreExit is the error for a core that ended: what happened, then the reason its last
// error line gave, or else how the process ended.
func coreExit(what string, log *coreLog, exit string) error {
	why := log.terminal()
	if why == "" {
		why = exit
	}
	if why == "" {
		return errors.New(what)
	}
	return fmt.Errorf("%s: %s", what, why)
}

// coreListening reports whether the core's listeners are up: the DNS listener, the mixed
// port (hyperd's own requests through the core) and, when it captures with TProxy, the
// TProxy port. TUN waits for its device in the network helper.
func coreListening(s mihomo.Settings) bool {
	if !listening("udp", s.DNSPort) || !listening("tcp", s.MixedPort) {
		return false
	}
	return s.DNSOnly || s.Method != mihomo.MethodTProxy || listening("tcp", s.TProxyPort)
}

// listening reports whether a socket of this host listens on port (/proc/net, both
// families): TCP in LISTEN, UDP bound.
func listening(proto string, port int) bool {
	local := fmt.Sprintf(":%04X", port)
	for _, name := range []string{"/proc/net/" + proto, "/proc/net/" + proto + "6"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) > 3 && strings.HasSuffix(f[1], local) && (proto == "udp" || f[3] == "0A") {
				return true
			}
		}
	}
	return false
}

// stop releases the takeover first (LAN keeps working), then ends the core.
func (s *service) stop(ctx context.Context, msg string) {
	s.mu.Lock()
	was, cancel, done, api := s.running, s.cancel, s.done, s.api
	s.running, s.starting, s.cancel, s.done, s.health, s.api = false, false, nil, nil, nil, nil
	s.mu.Unlock()
	if api != nil {
		api.HTTP.CloseIdleConnections()
	}
	if err := s.m.network(ctx, "down"); err != nil {
		s.m.log.Errorf("DAEMON", "Releasing the network takeover failed: %v", err)
	}
	if cancel != nil {
		cancel()
		<-done
	}
	if was && msg != "" {
		s.m.log.Printf("DAEMON", "%s", msg)
	}
}

func (s *service) setHealth(state string, failures int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		s.health = nil
		return
	}
	s.health = &health{State: state, Failures: failures, Time: s.m.sys.Now().Unix()}
}

func (s *service) healthState() *health {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health == nil {
		return nil
	}
	h := *s.health
	return &h
}

// monitor restores the takeover once the core answers again after guard failed open,
// and reports degraded / failopen meanwhile.
func (s *service) monitor(ctx context.Context) {
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.every):
		}
		s.mu.Lock()
		starting := s.starting
		s.mu.Unlock()
		api := s.controller()
		if api == nil || starting {
			failures = 0
			continue
		}
		if s.m.stopping() {
			continue // the init script released the takeover; SIGTERM follows
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := api.Version(pctx)
		cancel()
		_, ownedErr := os.Stat(s.m.paths.Owned)
		owned := ownedErr == nil
		switch {
		case err != nil:
			failures++
			if owned {
				s.setHealth("degraded", failures)
			} else {
				s.setHealth("failopen", failures)
			}
		case !owned && s.m.sys.Now().Before(s.holdUntil):
			s.setHealth("failopen", 0)
		case !owned:
			s.setHealth("recovering", 0)
			s.m.log.Printf("WATCHDOG", "mihomo is answering again, restoring proxy rules.")
			if err := s.m.network(ctx, "up"); err != nil {
				s.m.log.Errorf("WATCHDOG", "Restoring failed: %v", err)
				continue
			}
			failures = 0
			s.setHealth("ok", 0)
		default:
			if failures > 0 {
				s.m.log.Printf("WATCHDOG", "mihomo is answering again.")
			}
			failures = 0
			s.setHealth("ok", 0)
			s.repairFirewall(ctx)
		}
	}
}

// repairFirewall puts the takeover's rules back when a firewall stop or restart (fw4 flush)
// or another tool removed them while the core, routes and DNS stayed: dnsmasq would keep
// handing out fake addresses nothing captures. Past the limit, or when a repair fails, the
// LAN goes direct for a while instead.
func (s *service) repairFirewall(ctx context.Context) {
	if !s.tableGone("hyper") || !s.isRunning() {
		return
	}
	now := s.m.sys.Now()
	s.repairs = slices.DeleteFunc(s.repairs, func(t time.Time) bool { return now.Sub(t) >= repairWindow })
	if len(s.repairs) < repairLimit {
		s.repairs = append(s.repairs, now)
		out, err := s.m.sys.Run(ctx, s.m.paths.Network, "repair")
		if err == nil {
			if strings.TrimSpace(string(out)) == "repaired" {
				s.m.log.Printf("WATCHDOG", "The firewall rules were removed (firewall restart?), restored them.")
			}
			return
		}
		s.m.log.Errorf("WATCHDOG", "Restoring the removed firewall rules failed: %s", strings.TrimSpace(string(out)))
	} else {
		s.m.log.Errorf("WATCHDOG", "The firewall rules keep being removed; the LAN goes direct for %v.", repairWindow)
	}
	s.holdUntil = now.Add(repairWindow)
	if err := s.m.network(ctx, "fault"); err != nil {
		s.m.log.Errorf("WATCHDOG", "Releasing the takeover failed: %v", err)
	}
	s.setHealth("failopen", 0)
}

// network runs the takeover helper (up | down). It reads the plan written by start.
func (m *manager) network(ctx context.Context, action string) error {
	if m.paths.Network == "" || !m.sys.Exists(m.paths.Network) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := m.sys.Run(ctx, m.paths.Network, action)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var (
	planValue = lazyre.New(`^[A-Za-z0-9_.:/-]*$`)
	planList  = lazyre.New(`^[A-Za-z0-9_.:/ -]*$`)
	netName   = lazyre.New(`^[A-Za-z0-9_]+$`)
	devName   = lazyre.New(`^[A-Za-z0-9_.-]{1,15}$`)
)

// writePlan hands the network helper what it needs, as a shell-sourceable file.
// Every value is validated here; the helper never reads UCI or RPC input itself.
func (m *manager) writePlan(s mihomo.Settings, coreGID uint32) error {
	c, _ := m.config()
	infra := func(k, def string) string {
		if v := c.Get("infra", k); v != "" {
			return v
		}
		return def
	}
	method := s.Method
	if s.DNSOnly {
		method = "none"
	}
	bit := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	mark := infra("tproxy_mark", "101")
	if s.Method == mihomo.MethodTUN {
		mark = infra("tun_mark", "102")
	}
	var lans []string
	for _, n := range c.Section("infra").List("lan_network") {
		if !netName().MatchString(n) {
			return fmt.Errorf("invalid infra.lan_network %q", n)
		}
		lans = append(lans, n)
	}
	if len(lans) == 0 {
		lans = []string{"lan"}
	}
	lanMode := s.LANMode
	vars := [][2]string{
		{"CORE_GID", strconv.FormatUint(uint64(coreGID), 10)},
		{"METHOD", method}, {"IPV6", bit(s.IPv6)}, {"ROUTER_SELF", bit(s.RouterSelf)}, {"FAKE_IP", bit(s.FakeIP && !s.DNSOnly)},
		{"TPROXY_PORT", strconv.Itoa(s.TProxyPort)}, {"DNS_PORT", strconv.Itoa(s.DNSPort)},
		{"SELF_MARK", strconv.Itoa(s.Mark)}, {"MARK", mark}, {"TABLE", infra("table_mark", "100")},
		{"TUN_NAME", s.TunName}, {"DNS_REDIRECT", bit(infra("dns_redirect", "1") == "1")},
		{"LAN_MODE", lanMode},
	}
	lists := [][2]string{
		{"LAN_NETWORKS", strings.Join(lans, " ")},
		{"DIRECT_MACS", strings.Join(s.DirectMACs, " ")},
		{"PROXY_MACS", strings.Join(s.ProxyMACs, " ")},
	}
	for _, n := range []string{"SELF_MARK", "MARK", "TABLE"} {
		for _, v := range vars {
			if v[0] == n {
				if x, err := strconv.Atoi(v[1]); err != nil || x < 1 || x > 0xffff {
					return fmt.Errorf("invalid %s %q", strings.ToLower(n), v[1])
				}
			}
		}
	}
	var b strings.Builder
	b.WriteString("# Written by hyperd for /usr/libexec/hyper/network. Do not edit.\n")
	for _, v := range vars {
		if !planValue().MatchString(v[1]) {
			return fmt.Errorf("unsafe plan value %s", v[0])
		}
		b.WriteString("HYPER_" + v[0] + "='" + v[1] + "'\n")
	}
	for _, v := range lists {
		if !planList().MatchString(v[1]) {
			return fmt.Errorf("unsafe plan value %s", v[0])
		}
		b.WriteString("HYPER_" + v[0] + "='" + v[1] + "'\n")
	}
	if err := os.MkdirAll(filepath.Dir(m.paths.Plan), 0o700); err != nil {
		return err
	}
	tmp := m.paths.Plan + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.paths.Plan)
}

// procMemoryKB is the VmRSS of a process and its children (the core itself starts none).
func procMemoryKB(pid int) any {
	total, found := 0, false
	var walk func(pid, depth int)
	walk = func(pid, depth int) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
				if n, err := strconv.Atoi(f[1]); err == nil {
					total, found = total+n, true
				}
			}
		}
		if depth >= 2 {
			return
		}
		kids, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
		for _, k := range strings.Fields(string(kids)) {
			if n, err := strconv.Atoi(k); err == nil {
				walk(n, depth+1)
			}
		}
	}
	walk(pid, 0)
	if !found {
		return nil
	}
	return total
}

func (m *manager) registerService() {
	// Not in methods.json: rpcd cannot reach it, only root through the socket (hotplug).
	m.handlers["notify_network"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		restarted, err := m.networkChanged(ctx)
		return map[string]any{"result": err == nil, "restarted": restarted}, err
	}
	m.handlers["restart"] = func(context.Context, json.RawMessage) (any, error) {
		op := m.svc.request("restart")
		return map[string]any{"result": true, "accepted": true, "operation": orCurrent(op, m.svc)}, nil
	}
	m.handlers["set_enabled"] = func(_ context.Context, args json.RawMessage) (any, error) {
		var a struct{ Enabled bool }
		_ = jsonArgs(args, &a)
		if a.Enabled && m.sessions.Load() == nil {
			return map[string]any{"result": false, "error": "not signed in"}, nil
		}
		if err := m.edit(func(c *uciConfig) bool {
			c.Ensure("config", "hyper").Set("enabled", map[bool]string{true: "1", false: "0"}[a.Enabled])
			return true
		}); err != nil {
			return nil, err
		}
		action := "stop"
		if a.Enabled {
			action = "start"
		}
		op := m.svc.request(action)
		return map[string]any{"result": true, "accepted": true, "operation": orCurrent(op, m.svc)}, nil
	}
}

func orCurrent(op *operation, s *service) *operation {
	if op != nil {
		return op
	}
	return s.operation()
}

// networkChanged handles an interface notice from hotplug. Address lifetimes and RA
// refreshes usually change nothing in the core config, and restarting anyway would drop
// every open connection: restart only when the rendered config differs (a new WAN
// resolver, for example) or a managed LAN came up after the takeover skipped it (a guest
// Wi-Fi switched on). The attached-network sets are refreshed by hotplug itself.
func (m *manager) networkChanged(ctx context.Context) (bool, error) {
	if !m.svc.isRunning() {
		return false, nil
	}
	m.svc.mu.Lock()
	secret := m.svc.secret
	m.svc.mu.Unlock()
	s, err := m.runtimeSettings(ctx)
	if err != nil {
		return false, err
	}
	out, err := m.render(ctx, s, secret)
	if err != nil {
		return false, err
	}
	running, err := os.ReadFile(filepath.Join(m.paths.CoreDir, "config.yaml"))
	if err == nil && bytes.Equal(running, out.Config) {
		missing, err := m.sys.Run(ctx, m.paths.Network, "missing-lans")
		devs := strings.Fields(string(missing))
		if err != nil || len(devs) == 0 || slices.ContainsFunc(devs, func(d string) bool { return !devName().MatchString(d) }) {
			return false, nil
		}
		m.log.Printf("DAEMON", "LAN %s came up, restarting to take it over.", strings.Join(devs, ", "))
		m.svc.request("restart")
		return true, nil
	}
	m.log.Printf("DAEMON", "Network changed the core configuration, restarting.")
	m.svc.request("restart")
	return true, nil
}

// restartIfEnabled applies a settings change.
func (m *manager) restartIfEnabled() {
	if m.enabled() {
		m.svc.request("restart")
	}
}

// run is serve's background work: the action worker, the health monitor, device list
// refresh, log trimming and the daily schedules. On boot it starts the core if enabled.
func (m *manager) run(ctx context.Context) {
	go m.svc.worker(ctx)
	go m.svc.monitor(ctx)
	go m.devices.loop(ctx)
	go m.schedule(ctx)
	if m.enabled() {
		m.svc.request("start")
	}
}

// stopping reports an init script stop in progress: it has released the takeover and is
// about to signal serve, so nothing may take the LAN over again until the next start.
func (m *manager) stopping() bool {
	_, err := os.Stat(m.paths.stopping())
	return err == nil
}

// shutdown is serve's exit: release the takeover and stop the core.
func (m *manager) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.svc.stop(ctx, "Service stopped.")
}
