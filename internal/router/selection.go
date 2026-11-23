package router

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// Refresh invalidates in-flight work without changing the user's desired state.
// Notifications include their source so an inactive subscription update is cheap.
func (m *Manager) Refresh(source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if source != "" && !m.state.Intent.ReferencesSource(source) {
		return
	}
	// A refreshed subscription or a network notice reloads in place, which is a no-op when
	// nothing changed; only a different LAN, DNS or address family falls back to a restart.
	if m.hotLocked(m.state.Intent, m.state.Intent) {
		m.reviewEpoch++
		return
	}
	m.refreshLocked()
}

// NetworkNotice handles an interface event and reports whether it changed anything. An event
// that leaves the network as the last start or reload saw it must not interrupt a start,
// reload the core, wake a failed start early or void a pending review.
func (m *Manager) NetworkNotice(ctx context.Context) bool {
	changed, takeover := true, true
	if w, ok := m.backend.(interface {
		NetworkChanged(context.Context) (bool, bool)
	}); ok {
		c, end := context.WithTimeout(ctx, 10*time.Second)
		changed, takeover = w.NetworkChanged(c)
		end()
	}
	if !changed {
		return false
	}
	m.mu.Lock()
	if !takeover && m.state.Phase == "starting" && m.cancel != nil {
		// A WAN resolver that came or went keeps the takeover: the core reloads once it runs.
		m.reviewEpoch++
		select {
		case m.hot <- struct{}{}:
		default:
		}
		m.mu.Unlock()
		return true
	}
	m.mu.Unlock()
	m.Refresh("")
	return true
}

func (m *Manager) refreshLocked() {
	m.reviewEpoch++
	if m.cancel != nil {
		m.cancel()
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

type selectionBackend interface {
	Select(context.Context, string, string) error
}
type GroupResult struct {
	Saved    string `json:"saved"`
	Applied  string `json:"applied,omitempty"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	Revision uint64 `json:"revision"`
}

// Choose uses one lane per source/group. The worker always reads the most recent
// durable choice before dispatching and checks it again before acknowledging.
func (m *Manager) Choose(source, group, node string) (uint64, error) {
	if len(source) == 0 || len(source) > 256 || len(group) == 0 || len(group) > 200 || len(node) == 0 || len(node) > 200 {
		return 0, errors.New("invalid selection")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chooseLocked(source, group, node)
}

func (m *Manager) chooseLocked(source, group, node string) (uint64, error) {
	if m.frozen {
		return 0, ErrNotReady
	}
	i := m.state.Intent.clone()
	if source != i.Source {
		return 0, errors.New("source has changed")
	}
	if i.Choices[source] == nil {
		i.Choices[source] = map[string]string{}
	}
	i.Choices[source][group] = node
	i.Revision++
	if e := m.store.Save(i); e != nil {
		return 0, e
	}
	m.state.Intent = i
	current := m.state.Phase == "running" && i.Enabled && ((group == Global && i.Mode == "global") || (group != Global && i.Mode == "smart"))
	key := source + "\x00" + group
	r := m.groups[key]
	r.Saved = node
	r.Revision = i.Revision
	r.Error = ""
	if !current {
		r.State = "saved"
		m.groups[key] = r
		if m.cancel != nil && m.state.Phase != "running" {
			m.cancel()
			select {
			case m.wake <- struct{}{}:
			default:
			}
		}
		return i.Revision, nil
	}
	r.State = "pending"
	m.groups[key] = r
	if m.lanes[key] != m.generation {
		m.lanes[key] = m.generation
		go m.applyChoice(source, group, key, m.generation)
	}
	return i.Revision, nil
}
func (m *Manager) applyChoice(source, group, key string, generation uint64) {
	for {
		m.mu.Lock()
		r := m.groups[key]
		if generation != m.generation {
			m.mu.Unlock()
			return
		}
		if m.state.Phase != "running" || m.state.Intent.Source != source || !m.state.Intent.Enabled {
			delete(m.lanes, key)
			m.mu.Unlock()
			return
		}
		ctx0 := m.session
		m.mu.Unlock()
		ctx, end := context.WithTimeout(ctx0, 5*time.Second)
		b, ok := m.backend.(selectionBackend)
		var err error
		if !ok {
			err = errors.New("core selection unavailable")
		} else {
			err = b.Select(ctx, group, r.Saved)
		}
		end()
		m.mu.Lock()
		latest := m.groups[key]
		if generation != m.generation || ctx0.Err() != nil {
			if m.lanes[key] == generation {
				delete(m.lanes, key)
			}
			m.mu.Unlock()
			return
		}
		if latest.Revision != r.Revision {
			m.mu.Unlock()
			continue
		}
		if err != nil {
			r.State = "failed"
			r.Error = "local core did not confirm the selection"
		} else {
			r.State = "applied"
			r.Applied = r.Saved
		}
		m.groups[key] = r
		if err == nil {
			m.acknowledgeChoices(source)
		}
		delete(m.lanes, key)
		m.mu.Unlock()
		return
	}
}

// A hot switch publishes the current revision only after every selection that
// participates in the current routing mode has a matching core readback.
func (m *Manager) acknowledgeChoices(source string) {
	i := m.state.Intent
	for group, wanted := range i.Choices[source] {
		if (i.Mode == "global") != (group == Global) {
			continue
		}
		r, tracked := m.groups[source+"\x00"+group]
		if !tracked && strings.HasPrefix(group, "group:") {
			// The subscription no longer has the group (publish): its selection reset.
			continue
		}
		if r.State != "applied" || r.Applied != wanted {
			return
		}
	}
	m.state.Applied = i.Revision
}

// PruneChoices forgets the selections of a source's groups its committed profile no longer has:
// a group that comes back starts from its default, not from a member that may be gone too.
func (m *Manager) PruneChoices(source string, p subscription.Profile) {
	groups := map[string]bool{}
	for _, g := range p.Groups {
		groups[g.ID] = true
	}
	stale := func(choices map[string]string) bool {
		for group := range choices {
			if strings.HasPrefix(group, "group:") && !groups[group] {
				return true
			}
		}
		return false
	}
	m.mu.Lock()
	found := stale(m.state.Intent.Choices[source])
	m.mu.Unlock()
	if !found {
		return
	}
	_, _ = m.Change(nil, func(i *Intent) error {
		for group := range i.Choices[source] {
			if strings.HasPrefix(group, "group:") && !groups[group] {
				delete(i.Choices[source], group)
			}
		}
		return nil
	})
}

func (m *Manager) GroupState(source string) map[string]GroupResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	ret := map[string]GroupResult{}
	for id, v := range m.state.Intent.Choices[source] {
		r := m.groups[source+"\x00"+id]
		if r.Saved == "" {
			r.Saved = v
			r.State = "saved"
		}
		if m.state.Phase != "running" {
			r.Applied = ""
			r.State = "saved"
		}
		ret[id] = r
	}
	return ret
}

// Select switches a group and closes the connections that went through it, so the new node
// takes over at once instead of only for new connections.
func (s *System) Select(ctx context.Context, group, node string) error {
	c := s.Client()
	if e := c.Select(ctx, group, node); e != nil {
		return e
	}
	if c.CloseThrough(ctx, group) != nil {
		s.records.Log("core", "warn", "connections_not_reset")
	}
	return nil
}
