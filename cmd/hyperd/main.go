package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"

	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

const socket = router.RunDir + "/manager.sock"

var errRejected = errors.New("manager rejected request")

// updaterReasons are the fixed failure messages of /usr/libexec/hyper/package-update.
var updaterReasons = map[string]bool{
	"A package update is already in progress": true, "APK identity does not match release": true,
	"Cannot compare package versions": true, "Cannot download a package matching the release digest": true,
	"Cannot plan the offline APK upgrade": true, "Cannot read the router architecture": true,
	"Cannot verify the official release metadata": true, "Checking plugin updates requires the ucode command": true,
	"Checking plugin updates requires an HTTPS downloader (uclient-fetch, curl or GNU Wget)": true,
	"Installed APK providers changed during preflight":                                       true, "Insufficient temporary storage for update": true,
	"Invalid installed package version": true, "IPK identity does not match release": true,
	"Package manager did not apply the requested version": true, "Refusing package downgrade": true,
}

type request struct {
	Method    string `json:"method,omitempty"`
	scheduled bool
	Level     string          `json:"level,omitempty"`
	Query     string          `json:"query,omitempty"`
	Revision  *uint64         `json:"revision,omitempty"`
	Source    string          `json:"source,omitempty"`
	Group     string          `json:"group,omitempty"`
	Node      string          `json:"node,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Page      string          `json:"page,omitempty"`
	Offset    int             `json:"offset,omitempty"`
	Limit     int             `json:"limit,omitempty"`
	Task      string          `json:"task,omitempty"`
	Action    string          `json:"action,omitempty"`
	Token     string          `json:"token,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Policy    router.Policy   `json:"policy,omitempty"`
}
type job struct {
	ID     string          `json:"id"`
	Stage  string          `json:"stage"`
	Action string          `json:"action"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}
type app struct {
	edits           sync.Mutex
	sequence        uint64
	mutation        sync.Mutex
	stopJobs        context.CancelFunc
	activeJobs      sync.WaitGroup
	clockEvents     chan struct{}
	refreshes       refreshWork
	tests           probeSet
	m               *router.Manager
	sys             *router.System
	ctx             context.Context
	mu              sync.Mutex
	jobs            map[string]job
	order           []string
	busy            map[string]bool
	journalError    error
	bootstrapCancel context.CancelFunc
	bootstrapDone   chan struct{}
	closing         bool
}

func serve(ctx context.Context) error {
	if e := os.MkdirAll(router.RunDir, 0711); e != nil {
		return e
	}
	lf, e := os.OpenFile(router.RunDir+"/daemon.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lf.Close()
	if e = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("manager already running")
	}
	if e = os.Remove(socket); e != nil && !os.IsNotExist(e) {
		return e
	}
	l, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer l.Close()
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	sys := router.NewSystem()
	m := router.New(router.Disk{Path: router.Root + "/private/intent.json"}, sys, router.DefaultTiming)
	jobsCtx, stopJobs := context.WithCancel(ctx)
	defer stopJobs()
	s := &app{clockEvents: make(chan struct{}, 1), m: m, sys: sys, ctx: jobsCtx, stopJobs: stopJobs, busy: map[string]bool{}, jobs: map[string]job{}}
	s.journalError = s.loadJobs()
	// The capture method follows the firewall; store it before the first takeover.
	router.EnsureMethod(ctx)
	// Read UCI only once per boot. Restarting the manager on the same boot retains
	// the last explicit operation, including a manual stop.
	if _, err := os.Stat(router.RunDir + "/boot-policy"); os.IsNotExist(err) {
		b, _ := exec.CommandContext(ctx, "uci", "-q", "get", "hyper.main.enabled").Output()
		if _, e = m.Change(nil, func(i *router.Intent) error { i.Enabled = strings.TrimSpace(string(b)) == "1"; return nil }); e == nil {
			e = router.Atomic(router.RunDir+"/boot-policy", []byte("1"), 0600)
			if e != nil {
				return e
			}
		}
	}
	sources := subscription.OpenStore(router.Root + "/sources")
	if err := sources.CollectSets(router.Root + "/rulesets"); err != nil {
		sys.Records().Log("subscription", "warn", "rule_provider_cleanup_failed")
	}
	if _, err := selectInitialSource(m, sources, ""); err != nil {
		sys.Records().Log("service", "warn", "initial_subscription_select_failed")
	}
	s.queueFirstKernel()
	s.useBundledKernel()
	go s.adoptHyper()
	go s.schedule(jobsCtx)
	go s.keepChinaLists(jobsCtx)
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	server := &http.Server{Handler: s, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 70 * time.Second, MaxHeaderBytes: 4096}
	go func() { <-ctx.Done(); _ = server.Close() }()
	e = server.Serve(l)
	if errors.Is(e, http.ErrServerClosed) {
		<-done
		return nil
	}
	return e
}
func (s *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	limit := int64(12 << 20)
	if r.URL.Path == "/validate_source" {
		limit = 26 << 20
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if e != nil {
		http.Error(w, "request too large", 413)
		return
	}
	var req request
	if len(b) > 0 {
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
	}
	v, e := s.handle(r.Context(), strings.TrimPrefix(r.URL.Path, "/"), req)
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
func items[T any](v []T) map[string]any {
	if v == nil {
		v = []T{}
	}
	return map[string]any{"items": v}
}
func (s *app) handle(ctx context.Context, method string, r request) (any, error) {
	status := s.m.Status()
	events := s.sys.Records()
	switch method {
	case "validate_source":
		return s.reviewSource(ctx, r)
	case "environment":
		return router.EnvironmentStatus(), nil
	case "devices":
		v, e := router.Devices(ctx)
		return items(v), e
	case "network_preferences":
		return router.ReadPreferences(ctx)
	case "custom_rules":
		return items(status.Intent.Policy.Rules), nil
	case "latencies":
		return s.tests.snapshot(status.Intent.Source, r.Method)
	case "status":
		status.Version = subscription.PackageVersion()
		if t := cnlist.Updated(); !t.IsZero() {
			status.ChinaLists = t.UnixMilli()
		}
		status.SideRouter = router.SideRouter()
		if status.Phase == "starting" && s.sys != nil {
			status.ConfirmationSeconds = s.sys.ConfirmationSeconds()
		}
		return status, nil
	case "groups":
		return s.m.GroupState(status.Intent.Source), nil
	case "live_groups":
		v, e := s.sys.Client().Groups(ctx)
		return items(v), e
	case "traffic":
		return s.sys.Client().Traffic(ctx)
	case "routes":
		if r.Limit == 0 {
			r.Limit = 6
		}
		events.Viewed()
		return items(events.Routes(r.Limit)), nil
	case "logs":
		if r.Limit == 0 {
			r.Limit = 50
		}
		return items(events.Logs(r.Offset, r.Limit, r.Source, r.Level, r.Query)), nil
	case "connections":
		if r.Limit == 0 {
			r.Limit = 50
		}
		events.Viewed()
		return map[string]any{"items": events.Page(method, r.Offset, r.Limit)}, nil
	case "rule_hits":
		events.Viewed()
		return events.RuleHits(), nil
	case "task":
		return s.task(r.Task)
	case "notify_clock":
		select {
		case s.clockEvents <- struct{}{}:
		default:
		}
		return map[string]bool{"accepted": true}, nil
	case "notify_network":
		// Latency results only go stale when the uplinks or LANs actually changed.
		if s.m.NetworkNotice(ctx) {
			s.tests.reset()
		}
		return map[string]bool{"accepted": true}, nil
	case "notify_source":
		if status.Intent.Source == "" && r.Source != "" {
			selected, err := selectInitialSource(s.m, subscription.OpenStore(router.Root+"/sources"), r.Source)
			if err != nil {
				return nil, err
			}
			if selected {
				s.tests.reset()
				return map[string]bool{"accepted": true}, nil
			}
		}
		if r.Source == "" || status.Intent.ReferencesSource(r.Source) {
			s.tests.reset()
		}
		if p, err := subscription.OpenStore(router.Root + "/sources").Load(r.Source); err == nil {
			s.m.PruneChoices(r.Source, p)
		}
		s.m.Refresh(r.Source)
		return map[string]bool{"accepted": true}, nil
	case "clear":
		if r.Page != "logs" && r.Page != "connections" {
			return nil, errors.New("unknown log type")
		}
		events.Clear(r.Page)
		return map[string]bool{"cleared": true}, nil
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		if method == "uninstall" {
			return revisionTask(s.m.Status().Intent.Revision), nil
		}
		return nil, errors.New("uninstall in progress")
	}
	switch method {
	case "save_network":
		var prefs router.Preferences
		if json.Unmarshal(r.Args, &prefs) != nil {
			return nil, errors.New("invalid network preferences")
		}
		// The capture method is chosen automatically at startup, not by the page.
		prefs.Method = router.StoredMethod(ctx)
		if prefs.Validate() != nil {
			return nil, errors.New("invalid network preferences")
		}
		return s.runJob("uci", "save_network", func(ctx context.Context) (json.RawMessage, error) {
			old, oldErr := router.ReadPreferences(ctx)
			if e := router.SavePreferences(ctx, prefs); e != nil {
				return nil, e
			}
			// Refresh period and boot autostart are read when used;
			// only settings that shape the takeover need a new start.
			if oldErr != nil || !old.SameNetwork(prefs) {
				s.m.Refresh("")
			}
			return nil, nil
		})
	case "mode", "source", "policy", "save_rules":
		return s.change(ctx, method, r)
	case "start", "stop", "uninstall":
		id, e := s.m.Change(r.Revision, func(i *router.Intent) error {
			i.Enabled = method == "start"
			return nil
		})
		if e == nil {
			s.tests.reset()
			events.Log("service", "info", method)
			if method == "uninstall" {
				s.mu.Lock()
				s.closing = true
				s.stopJobs()
				s.mu.Unlock()
				s.activeJobs.Wait()
			}
		}
		return revisionTask(id), e
	case "test_nodes":
		return s.probes(r)
	case "cancel_tests":
		s.tests.stop()
		return map[string]bool{"cancelled": true}, nil
	case "select_node":
		store := subscription.OpenStore(router.Root + "/sources")
		p, e := store.Load(r.Source)
		memberSource := r.Source
		if e != nil {
			return nil, errors.New("source unavailable")
		}
		valid := false
		if r.Group == router.Global || r.Group == router.Local && p.Local {
			for _, n := range p.Nodes {
				if n.ID == r.Node && !n.Special {
					valid = true
					break
				}
			}
		} else {
			for _, g := range p.Groups {
				if g.ID == r.Group && g.Kind == "selector" && slices.Contains(g.Members, r.Node) {
					valid = true
					break
				}
			}
		}
		for _, g := range status.Intent.Policy.Groups {
			if g.ID == r.Group {
				for _, ref := range g.Members {
					if ref.ID == r.Node {
						valid = true
						memberSource = ref.Source
					}
				}
			}
		}
		if !valid {
			return nil, errors.New("unknown selectable member")
		}
		if strings.HasPrefix(r.Node, "node:") {
			if memberSource != r.Source {
				p, e = store.Load(memberSource)
				if e != nil {
					return nil, errors.New("source unavailable")
				}
			}
			found := false
			for _, n := range p.Nodes {
				if n.ID == r.Node {
					found = true
					if n.Special {
						return nil, errors.New("unknown selectable member")
					}
					if key := n.CapabilityKey(); key != "" && subscription.CurrentCoreFeatures(ctx).Availability(key) == "unsupported" {
						return nil, errors.New("mihomo_protocol_unsupported")
					}
					break
				}
			}
			if !found {
				return nil, errors.New("unknown selectable member")
			}
		}
		id, e := s.m.Choose(r.Source, r.Group, r.Node)
		return revisionTask(id), e
	case "health_check":
		if status.Phase != "running" {
			return nil, errors.New("core is not running")
		}
		return s.runJob("health", "health_check", func(ctx context.Context) (json.RawMessage, error) {
			p, e := router.ReadPreferences(ctx)
			if e != nil {
				return nil, e
			}
			return json.Marshal(s.sys.Checks(ctx, p.DNS))
		})
	case "plugin_check", "plugin_update":
		action := "check"
		if method == "plugin_update" {
			action = "apply"
		}
		return s.runJob("plugin", method, func(ctx context.Context) (json.RawMessage, error) {
			cmd := exec.CommandContext(ctx, "/usr/libexec/hyper/package-update", action)
			out, e := cmd.Output()
			if e != nil {
				var exit *exec.ExitError
				if errors.As(e, &exit) {
					// The updater ends with one of its own fixed reasons; package-manager output
					// before it stays private.
					lines := strings.Split(strings.TrimSpace(string(exit.Stderr)), "\n")
					if last := strings.TrimSpace(lines[len(lines)-1]); updaterReasons[last] {
						return nil, errors.New(last)
					}
				}
				return nil, errors.New("verified plugin update failed")
			}
			if json.Valid(out) {
				return out, nil
			}
			return nil, nil
		})
	case "subscription_read", "subscription_write", "kernel_read", "kernel_write", "conflicts":
		return s.dispatch(ctx, method, r)
	}
	return nil, errors.New("unknown operation")
}
func child(ctx context.Context, bin string, args []string, body []byte) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, "/usr/sbin/"+bin, args...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.WaitDelay = time.Second
	out, e := cmd.Output()
	if len(out) > 4<<20 {
		return nil, errors.New("response too large")
	}
	if e != nil {
		if bin == "hyper-kernel" {
			var failure *exec.ExitError
			if errors.As(e, &failure) {
				return nil, publicKernelError(string(failure.Stderr))
			}
		}
		if bin == "hyper-subscription" || bin == "hyper-net" {
			if json.Valid(out) {
				return out, nil
			}
		}
		return nil, errors.New("operation failed")
	}
	if !json.Valid(out) {
		return nil, errors.New("invalid helper response")
	}
	return out, nil
}
func (s *app) dispatch(ctx context.Context, method string, r request) (any, error) {
	bin := "hyper-subscription"
	arg := []string{r.Action}
	body := r.Args
	lane := method
	switch method {
	case "subscription_read":
		if !slices.Contains([]string{"list", "status", "profile", "nodes", "groups", "rules", "set_match", "account", "catalog", "checkin_status"}, r.Action) {
			return nil, errors.New("unknown read operation")
		}
		return child(ctx, bin, arg, body)
	case "subscription_write":
		if !slices.Contains([]string{"login", "refresh", "rollback", "revoke", "logout", "send_code", "api", "checkin", "rule_toggle"}, r.Action) {
			return nil, errors.New("unknown subscription operation")
		}
		lane = ""
		// Signing in and turning a server rule list on or off take effect by reading the
		// account again: the refresh follows as soon as the helper reports success.
		if r.Action == "login" || r.Action == "rule_toggle" {
			return s.runJob(lane, method+"/"+r.Action, func(ctx context.Context) (json.RawMessage, error) {
				out, e := child(ctx, bin, arg, body)
				var res struct {
					OK   bool `json:"ok"`
					Data struct {
						Result bool `json:"result"`
					} `json:"data"`
				}
				if e == nil && json.Unmarshal(out, &res) == nil && res.OK && res.Data.Result {
					s.refreshHyper()
				}
				return out, e
			})
		}
		if r.Action == "refresh" {
			guarded, e := protectReferences(body, s.m.Status().Intent)
			if e != nil {
				return nil, e
			}
			return s.runRefresh(guarded, r.scheduled)
		}
	case "kernel_read":
		bin = "hyper-kernel"
		if !slices.Contains([]string{"status", "capabilities", "target-capabilities", "versions"}, r.Action) {
			return nil, errors.New("unknown kernel query")
		}
		if r.Action == "status" {
			st, err := kernelStatus(ctx)
			if err != nil {
				return nil, err
			}
			return kernelView{Status: st, Bootstrap: s.bootstrapJob()}, nil
		}
		if r.Action != "versions" {
			return child(ctx, bin, arg, nil)
		}
	case "kernel_write":
		return s.kernelJob(r)
	case "conflicts":
		return child(ctx, "hyper-net", []string{"conflicts"}, nil)
	}
	return s.runJob(lane, method+"/"+r.Action, func(ctx context.Context) (json.RawMessage, error) {
		out, e := child(ctx, bin, arg, body)

		return out, e
	})
}
func (s *app) runJob(lane, action string, fn func(context.Context) (json.RawMessage, error)) (any, error) {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		return nil, e
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	if s.journalError != nil {
		e := s.journalError
		s.mu.Unlock()
		return nil, e
	}
	if lane != "" && s.busy[lane] || len(s.busy) >= 16 {
		s.mu.Unlock()
		return nil, errors.New("operation already in progress")
	}
	if lane == "" {
		lane = id
	}
	s.busy[lane] = true
	j := job{ID: id, Stage: "running", Action: action}
	s.jobs[id] = j
	s.order = append(s.order, id)
	if len(s.order) > 128 {
		for idx, v := range s.order {
			if s.jobs[v].Stage != "running" {
				delete(s.jobs, v)
				s.order = slices.Delete(s.order, idx, idx+1)
				break
			}
		}
	}
	if e := s.saveJobs(); e != nil {
		delete(s.jobs, id)
		delete(s.busy, lane)
		s.order = slices.Delete(s.order, len(s.order)-1, len(s.order))
		s.mu.Unlock()
		return nil, e
	}
	s.activeJobs.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.activeJobs.Done()
		c, end := context.WithTimeout(s.ctx, 10*time.Minute)
		defer end()
		out, e := fn(c)
		j.Stage = "done"
		if len(out) <= 64<<10 {
			j.Result = out
		}
		var response struct {
			OK    *bool           `json:"ok"`
			Error string          `json:"error"`
			Data  json.RawMessage `json:"data"`
		}
		if e == nil && json.Unmarshal(out, &response) == nil && response.OK != nil && !*response.OK {
			if len(response.Error) > 0 && len(response.Error) <= 64 && strings.Trim(response.Error, "abcdefghijklmnopqrstuvwxyz0123456789_-") == "" {
				e = errors.New(response.Error)
			} else {
				e = errors.New("subscription operation failed")
			}
		}
		if e != nil {
			j.Stage = "failed"
			j.Error = e.Error()
		}
		s.mu.Lock()
		s.jobs[id] = j
		delete(s.busy, lane)
		if err := s.saveJobs(); err != nil {
			s.sys.Records().Log("task", "warn", "task_journal_write_failed")
		}
		s.mu.Unlock()
		if quietTask(action, e) {
			return
		}
		event, level := action+" "+j.Stage+" "+id, map[bool]string{true: "warn", false: "info"}[e != nil]
		if action == bootstrapAction && j.Error != "" || response.Error != "" && response.Error == j.Error {
			event += " " + j.Error
		} else if action == bootstrapAction {
			event += " " + string(publicBootstrapResult(j.Result))
		} else if code := heldUpdate(action, response.Data); e == nil && code != "" {
			// The task is done, but the update it downloaded waits for the administrator.
			event, level = action+" held "+id+" "+code, "warn"
		}
		s.sys.Records().Log("task", level, event)
	}()
	return map[string]string{"task": id}, nil
}

// quietTask keeps routine outcomes out of the log, as OpenClash does: latency tests and
// connectivity checks show their results where they were started, and a settings change
// already logs itself as a service event. Failures are still logged, except a test that a
// newer one cancelled.
func quietTask(action string, e error) bool {
	routine := strings.HasPrefix(action, "latency/") || action == "health_check"
	return routine && (e == nil || errors.Is(e, context.Canceled)) || e == nil && strings.HasPrefix(action, "configure/")
}

func call(ctx context.Context, method string, data []byte) ([]byte, error) {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer t.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://router/"+method, bytes.NewReader(data))
	resp, e := (&http.Client{Transport: t}).Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 && e == nil {
		e = errRejected
	}
	return b, e
}
func uninstall(ctx context.Context) error {
	if _, e := os.Stat(socket); e == nil {
		if _, e = call(ctx, "uninstall", nil); e != nil {
			return e
		}
		for {
			b, e := call(ctx, "status", nil)
			if e != nil {
				return e
			}
			var st router.Status
			if json.Unmarshal(b, &st) == nil && st.Phase == "direct" && !st.Intent.Enabled {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	cmd := exec.CommandContext(ctx, "/usr/libexec/hyper/network", "release")
	if e := cmd.Run(); e != nil {
		return errors.New("network could not be restored")
	}
	return nil
}

// purgePaths lists what `hyperd purge` deletes once the network is direct again: the
// kernels, subscriptions and accounts, private state, rule sets, CN lists and geodata.
func purgePaths() []string {
	return []string{router.Root + "/kernel", "/tmp/hyper/kernel", router.Root + "/mihomo-kernel", router.Root + "/sources", router.Root + "/private", router.Root + "/rulesets", cnlist.Dir, "/tmp/hyper/mihomo-kernel",
		router.GeodataDir}
}

func runDaemon() {
	ctx, end := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer end()
	args := os.Args[1:]
	var e error
	switch {
	case len(args) == 1 && args[0] == "serve":
		e = serve(ctx)
	case len(args) == 1 && args[0] == "guard":
		e = router.Guard(ctx)
	case len(args) == 1 && args[0] == "uninstall":
		c, stop := context.WithTimeout(ctx, 25*time.Second)
		e = uninstall(c)
		stop()
	case len(args) == 1 && args[0] == "purge":
		c, stop := context.WithTimeout(ctx, 25*time.Second)
		e = uninstall(c)
		stop()
		if e == nil {
			for _, p := range purgePaths() {
				if e = os.RemoveAll(filepath.Clean(p)); e != nil {
					break
				}
			}
		}
	case (len(args) == 2 || len(args) == 3) && args[0] == "validate-source":
		e = validateSource(args)
	case len(args) == 2 && args[0] == "notify-source":
		b, _ := json.Marshal(request{Source: args[1]})
		_, e = call(ctx, "notify_source", b)
	case len(args) == 1 && args[0] == "notify-clock":
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, e = call(c, "notify_clock", nil)
	case len(args) == 1 && args[0] == "notify-network":
		// Runs from every interface hotplug event, which netifd handles one at a time.
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, e = call(c, "notify_network", nil)
	case len(args) == 2 && args[0] == "call":
		var d []byte
		d, e = rpcInput(os.Stdin)
		if e == nil {
			c, stop := context.WithTimeout(ctx, 30*time.Second)
			var b []byte
			b, e = call(c, args[1], d)
			stop()
			if errors.Is(e, errRejected) && json.Valid(b) {
				e = nil
			}
			if len(b) > 0 {
				_, _ = os.Stdout.Write(b)
			}
		}
	default:
		e = errors.New("expected serve, guard, uninstall, purge, notify-clock, notify-network, notify-source SOURCE or call METHOD")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
