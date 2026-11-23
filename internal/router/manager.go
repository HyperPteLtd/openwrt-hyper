package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

type Store interface {
	Load() (Intent, error)
	Save(Intent) error
}
type Backend interface {
	// Direct releases only resources owned by Hyper, then verifies the LAN path.
	Direct(context.Context) error
	// Fault applies the saved failure policy to an existing takeover. The result
	// is true only after the network helper has verified its protective rules.
	Fault(context.Context) (bool, error)
	Start(context.Context, Intent) error
	Healthy(context.Context) error
}
type Status struct {
	ConfirmationSeconds int    `json:"confirmation_seconds,omitempty"`
	Version             string `json:"version,omitempty"` // installed plugin version
	Intent              Intent `json:"saved"`
	Applied             uint64 `json:"applied_revision"`
	Phase               string `json:"phase"`
	RunningSince        int64  `json:"running_since,omitempty"`
	FailOpen            bool   `json:"fail_open"`
	Problem             string `json:"error,omitempty"`
	SideRouter          bool   `json:"side_router,omitempty"` // see SideRouter
	// When the China lists in use were downloaded; unset for the packaged copies.
	ChinaLists int64 `json:"china_lists,omitempty"`
	// What the last plugin update check found (cmd/hyperd pluginState).
	Plugin json.RawMessage `json:"plugin,omitempty"`
}
type Timing struct{ Start, Stop, Heartbeat, Retry time.Duration }

var DefaultTiming = Timing{180 * time.Second, 15 * time.Second, 2 * time.Second, 30 * time.Second}

type Manager struct {
	instance    int64
	mu          sync.Mutex
	store       Store
	backend     Backend
	timing      Timing
	state       Status
	frozen      bool
	cancel      context.CancelFunc
	session     context.Context
	generation  uint64
	reviewEpoch uint64
	groups      map[string]GroupResult
	lanes       map[string]uint64
	wake        chan struct{}
	hot         chan struct{}
	failCancel  context.CancelFunc
}

// Reloader swaps the running configuration without releasing the network.
type Reloader interface {
	Reload(context.Context, Intent) error
}

var errHotFallback = errors.New("hot reload fell back to restart")

// ErrRevisionChanged rejects a change made against a configuration revision that is no longer current.
var ErrRevisionChanged = errors.New("configuration revision changed")

func New(s Store, b Backend, t Timing) *Manager {
	m := &Manager{instance: newInstance(), store: s, backend: b, timing: t, wake: make(chan struct{}, 1), hot: make(chan struct{}, 1), groups: map[string]GroupResult{}, lanes: map[string]uint64{}}
	v, e := s.Load()
	if errors.Is(e, os.ErrNotExist) {
		v = initial()
	} else if e != nil {
		m.frozen = true
		v = initial()
		m.state.Problem = e.Error()
	}
	m.state.Intent = v
	m.state.Phase = "initializing"
	m.state.FailOpen = true
	return m
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	s.Intent = s.Intent.clone()
	return s
}

// RuntimeState lets long-running kernel jobs observe activation without
// cloning the entire routing policy at each health checkpoint.
func (m *Manager) RuntimeState() (phase, problem string, wantsProxy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := &m.state.Intent
	return m.state.Phase, m.state.Problem, !m.frozen && i.Enabled && i.Mode != "direct"
}

// Change serializes validation, disk commit and cancellation. A failed write never
// schedules network side effects. An expected revision protects async callbacks.
func (m *Manager) Change(expected *uint64, edit func(*Intent) error) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.frozen {
		return 0, ErrNotReady
	}
	i := m.state.Intent.clone()
	if expected != nil && *expected != i.Revision {
		return 0, ErrRevisionChanged
	}
	if e := edit(&i); e != nil {
		return 0, e
	}
	if !validMode(i.Mode) {
		return 0, errors.New("unknown routing mode")
	}
	// An unchanged intent keeps a healthy core; a failed one may still be retried.
	if m.state.Phase == "running" && reflect.DeepEqual(i, m.state.Intent.clone()) {
		return i.Revision, nil
	}
	i.Revision++
	if e := m.store.Save(i); e != nil {
		return 0, e
	}
	old := m.state.Intent
	m.state.Intent = i
	if m.hotLocked(old, i) {
		return i.Revision, nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return i.Revision, nil
}

// hotLocked queues an in-place reload when the core keeps running in a proxy
// mode on both sides of the change.
func (m *Manager) hotLocked(old, next Intent) bool {
	if _, ok := m.backend.(Reloader); !ok || m.state.Phase != "running" || m.cancel == nil {
		return false
	}
	if !old.Enabled || !next.Enabled || old.Mode == "direct" || next.Mode == "direct" {
		return false
	}
	select {
	case m.hot <- struct{}{}:
	default:
	}
	return true
}
func (m *Manager) phase(p string, fail bool, e error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if recorder, ok := m.backend.(interface{ Records() *Records }); ok {
		level := "info"
		detail := p
		if e != nil {
			level = "warn"
			detail += " " + e.Error()
		}
		recorder.Records().Log("service", level, detail)
	}
	m.state.Phase = p
	m.state.FailOpen = fail
	m.state.Applied = 0
	if e != nil {
		m.state.Problem = e.Error()
	} else if !m.frozen {
		m.state.Problem = ""
	}
}
func (m *Manager) clean(fault bool) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.timing.Stop)
	defer cancel()
	if fault {
		return m.backend.Fault(ctx)
	}
	return false, m.backend.Direct(ctx)
}
func (m *Manager) wait(ctx context.Context, d time.Duration) bool {
	var t <-chan time.Time
	if d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		t = timer.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-m.wake:
	case <-t:
	}
	return true
}

// Run is the sole owner of network transitions. A request during cleanup is
// persisted immediately but cannot interrupt cleanup.
func (m *Manager) Run(ctx context.Context) {
	needRelease := true
	protected := false
	defer func() {
		if needRelease || protected {
			_, _ = m.clean(false)
		}
	}()
	for ctx.Err() == nil {
		if needRelease {
			m.phase("releasing", true, nil)
			_, _, wantsProxy := m.RuntimeState()
			held, e := m.clean(wantsProxy)
			if e != nil {
				m.phase("release_failed", true, e)
				if !m.wait(ctx, m.timing.Retry) {
					return
				}
				continue
			}
			protected = held
			needRelease = false
		}
		m.mu.Lock()
		select {
		case <-m.wake:
		default:
		}
		i := m.state.Intent.clone()
		if m.frozen || !i.Enabled || i.Mode == "direct" {
			if protected {
				m.mu.Unlock()
				needRelease = true
				continue
			}
			m.state.Phase = "direct"
			if m.frozen {
				m.state.Phase = "state_unavailable"
			} else {
				m.state.Applied = i.Revision
				m.state.Problem = ""
			}
			m.state.RunningSince = 0
			m.mu.Unlock()
			if !m.wait(ctx, 0) {
				return
			}
			continue
		}
		attempt, cancel := context.WithCancel(ctx)
		m.cancel = cancel
		m.session = attempt
		m.generation++
		m.mu.Unlock()
		e := attempt.Err()
		if e == nil {
			needRelease = true
			m.phase("starting", true, nil)
			boot, end := context.WithTimeout(attempt, m.timing.Start)
			e = m.backend.Start(boot, i)
			if e == nil {
				e = boot.Err()
			}
			end()
		}
		var actual map[string]string
		if e == nil && attempt.Err() == nil {
			if reader, ok := m.backend.(interface {
				Selected(context.Context) (map[string]string, error)
			}); ok {
				read, finish := context.WithTimeout(attempt, 3*time.Second)
				actual, e = reader.Selected(read)
				finish()
			}
		}
		if e == nil && attempt.Err() == nil {
			m.publish(i, actual)
			m.startFailover(attempt, i)
			e = m.watch(attempt)
		}
		interrupted := attempt.Err() != nil || errors.Is(e, errHotFallback)
		m.stopFailover()
		cancel()
		m.mu.Lock()
		m.cancel = nil
		m.mu.Unlock()
		if !interrupted && e != nil {
			m.phase("releasing", true, e)
			_, _, wantsProxy := m.RuntimeState()
			held, cleanup := m.clean(wantsProxy)
			if cleanup != nil {
				e = fmt.Errorf("%w (release failed: %v)", e, cleanup)
				m.phase("release_failed", true, e)
			} else {
				needRelease = false
				protected = held
				if held {
					m.phase("protected", false, e)
				} else {
					m.phase("fail_open", true, e)
				}
				if errors.Is(e, ErrFallbackReady) {
					continue
				}
			}
			if !m.wait(ctx, m.timing.Retry) {
				return
			}
		}
	}
}

// publish records the running revision and the group selections the core reports.
func (m *Manager) publish(i Intent, actual map[string]string) {
	m.mu.Lock()
	if i.Revision == m.state.Intent.Revision {
		m.groups = map[string]GroupResult{}
		for group, node := range i.Choices[i.Source] {
			if (group == Global && i.Mode == "global") || (group != Global && i.Mode == "smart") {
				seen, ok := actual[group]
				if !ok && strings.HasPrefix(group, "group:") {
					// A group the subscription no longer has: its selection reset.
					continue
				}
				r := GroupResult{Saved: node, State: "saved", Revision: i.Revision}
				if ok {
					r.Applied = seen
					if seen == node {
						r.State = "applied"
					} else {
						r.State = "failed"
						r.Error = "core returned another member"
					}
				}
				m.groups[i.Source+"\x00"+group] = r
			}
		}
		if recorder, ok := m.backend.(interface{ Records() *Records }); ok {
			if m.state.Phase != "running" {
				recorder.Records().Log("service", "info", "running")
			}
		}
		// A hot reload keeps the takeover, so the uptime carries on.
		if m.state.Phase != "running" || m.state.RunningSince == 0 {
			m.state.RunningSince = time.Now().UnixMilli()
		}
		m.state.Phase = "running"
		m.state.FailOpen = false
		m.state.Applied = i.Revision
		m.state.Problem = ""
	}
	m.mu.Unlock()
}

func (m *Manager) startFailover(parent context.Context, i Intent) {
	if !i.Policy.Failover() {
		return
	}
	sys, ok := m.backend.(interface{ Client() control.Client })
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.failCancel = cancel
	m.mu.Unlock()
	go m.failoverLoop(ctx, sys.Client(), i.Source, i.Mode)
}
func (m *Manager) stopFailover() {
	m.mu.Lock()
	if m.failCancel != nil {
		m.failCancel()
		m.failCancel = nil
	}
	m.mu.Unlock()
}

// reload applies the latest intent in place; any failure restarts the core.
func (m *Manager) reload(ctx context.Context) error {
	m.mu.Lock()
	i := m.state.Intent.clone()
	m.mu.Unlock()
	r := m.backend.(Reloader)
	c, end := context.WithTimeout(ctx, m.timing.Start)
	e := r.Reload(c, i)
	end()
	if e != nil {
		// A change of listeners or firewall needs a restart by design, and the restart logs
		// itself; only a reload that failed is worth a line.
		if ctx.Err() == nil && !errors.Is(e, ErrNeedsRestart) {
			if recorder, ok := m.backend.(interface{ Records() *Records }); ok {
				recorder.Records().Log("service", "warn", "reload fallback "+e.Error())
			}
		}
		return errHotFallback
	}
	var actual map[string]string
	if reader, ok := m.backend.(interface {
		Selected(context.Context) (map[string]string, error)
	}); ok {
		read, finish := context.WithTimeout(ctx, 3*time.Second)
		actual, _ = reader.Selected(read)
		finish()
	}
	m.mu.Lock()
	m.generation++
	m.mu.Unlock()
	m.stopFailover()
	m.publish(i, actual)
	m.startFailover(ctx, i)
	return nil
}
func (m *Manager) watch(ctx context.Context) error {
	ticker := time.NewTicker(m.timing.Heartbeat)
	defer ticker.Stop()
	miss := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.hot:
			if e := m.reload(ctx); e != nil {
				return e
			}
		case <-ticker.C:
			pulse, end := context.WithTimeout(ctx, m.timing.Heartbeat)
			e := m.backend.Healthy(pulse)
			end()
			if e == nil {
				miss = 0
			} else {
				miss++
				if miss >= 3 {
					return fmt.Errorf("local health: %w", e)
				}
			}
		}
	}
}
