package router

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

type failoverControl interface {
	Groups(context.Context) ([]control.Group, error)
	Delays(context.Context) (map[string]control.Latency, error)
	Measure(context.Context, string) (control.Latency, error)
	Version(context.Context) (string, error)
}

type nodeFailure struct {
	token          string
	member         string
	misses, offset int
}

func eligibleGroup(g control.Group, mode string) bool {
	if !g.Selectable || g.Type != "selector" || len(g.Members) < 2 || !slices.Contains(g.Members, g.Selected) || (g.Tag == Global) != (mode == "global") {
		return false
	}
	// The compiler uses source-scoped node IDs. Excluding special outbounds
	// and nested groups preserves the subscription's routing decisions.
	for _, id := range g.Members {
		if len(id) != 37 || !strings.HasPrefix(id, "node:") {
			return false
		}
	}
	return mode == "global" || mode == "smart"
}

func failoverGroups(ctx context.Context, c failoverControl) ([]control.Group, error) {
	query, end := context.WithTimeout(ctx, 2*time.Second)
	defer end()
	return c.Groups(query)
}

func measureNode(ctx context.Context, c failoverControl, id string) (control.Latency, error) {
	query, end := context.WithTimeout(ctx, 12*time.Second)
	defer end()
	return c.Measure(query, id)
}

func (m *Manager) failoverLoop(ctx context.Context, c failoverControl, source, mode string) {
	failures := map[string]nodeFailure{}
	index := 0
	for ctxWait(ctx, 30*time.Second) == nil {
		groups, e := failoverGroups(ctx, c)
		if e != nil {
			continue
		}
		eligible := make([]control.Group, 0, len(groups))
		for _, g := range groups {
			if eligibleGroup(g, mode) {
				eligible = append(eligible, g)
			}
		}
		if len(eligible) == 0 {
			continue
		}
		for range min(2, len(eligible)) {
			g := eligible[index%len(eligible)]
			index++
			token, i, needed, e := m.ReviewToken(source)
			if e != nil || !needed || !i.Policy.Failover() || i.Mode != mode {
				return
			}
			previous := failures[g.Tag]
			if previous.token != token {
				previous = nodeFailure{}
			}
			next, target := probeFailover(ctx, c, g, previous)
			next.token = token
			failures[g.Tag] = next
			if target != "" {
				m.acceptFailover(ctx, c, source, g, target, token)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func probeFailover(ctx context.Context, c failoverControl, g control.Group, previous nodeFailure) (nodeFailure, string) {
	if previous.member != g.Selected {
		previous = nodeFailure{member: g.Selected}
	}
	// Explicitly distinguish a rejected/unknown core RPC from a scheduled
	// node probe that returned no successful sample.
	current, e := measureNode(ctx, c, g.Selected)
	if (e == nil && current.Measured > 0) || ctx.Err() != nil {
		return nodeFailure{member: g.Selected}, ""
	}
	if e != nil && !errors.Is(e, context.DeadlineExceeded) && !errors.Is(e, control.ErrProbeFailed) {
		return nodeFailure{member: g.Selected}, ""
	}
	check, end := context.WithTimeout(ctx, 2*time.Second)
	_, e = c.Version(check)
	end()
	if e != nil {
		return nodeFailure{member: g.Selected}, ""
	}
	previous.misses++
	if previous.misses < 2 {
		return previous, ""
	}
	query, end := context.WithTimeout(ctx, 2*time.Second)
	history, e := c.Delays(query)
	end()
	if e != nil {
		return previous, ""
	}
	candidates := slices.DeleteFunc(slices.Clone(g.Members), func(n string) bool { return n == g.Selected })
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := history[candidates[i]], history[candidates[j]]
		return a.Measured > 0 && (b.Measured == 0 || a.Delay < b.Delay)
	})
	var best string
	var lowest uint64
	for range min(4, len(candidates)) {
		node := candidates[previous.offset%len(candidates)]
		previous.offset++
		delay, e := measureNode(ctx, c, node)
		if ctx.Err() != nil {
			return previous, ""
		}
		if e == nil && delay.Measured > 0 && (best == "" || delay.Delay < lowest) {
			best, lowest = node, delay.Delay
		}
	}
	return previous, best
}

func (m *Manager) acceptFailover(ctx context.Context, c failoverControl, source string, g control.Group, target, token string) {
	groups, e := failoverGroups(ctx, c)
	if e != nil {
		return
	}
	same := slices.ContainsFunc(groups, func(v control.Group) bool {
		return v.Tag == g.Tag && v.Selected == g.Selected && slices.Equal(v.Members, g.Members) && v.Selectable && v.Type == "selector"
	})
	if !same {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.state.Phase != "running" || !m.state.Intent.Enabled || !m.state.Intent.Policy.Failover() || m.state.Intent.Source != source || m.reviewToken() != token || m.groups[source+"\x00"+g.Tag].State == "pending" {
		return
	}
	if _, e := m.chooseLocked(source, g.Tag, target); e == nil {
		if r, ok := m.backend.(interface{ Records() *Records }); ok {
			r.Records().Log("core", "info", "node_failover_requested")
		}
	}
}
