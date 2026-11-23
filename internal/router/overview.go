package router

import (
	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"slices"
	"strings"
)

type RouteObservation struct {
	Group    string   `json:"group,omitempty"`
	Chain    []string `json:"chain,omitempty"`
	Outbound string   `json:"outbound"`
	Time     int64    `json:"time"`
	Evidence string   `json:"evidence"`
}

// Use only the manager's own group IDs reported by Mihomo in this connection's
// chain. Sharing a final outbound never establishes group attribution.
func (r *Records) Routes(limit int) []RouteObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []RouteObservation{}
	seen := map[string]bool{}
	for j := len(r.connections) - 1; j >= 0 && len(out) < min(max(limit, 1), 32); j-- {
		c := r.connections[(j+r.pos)%len(r.connections)]
		if c.Outbound == "" || c.Result == "failed" || c.Created != 0 && c.Created < r.routeSince {
			continue
		}
		var chain []string
		for _, tag := range c.Chain {
			if tag == Local || tag == Global || strings.HasPrefix(tag, "group:") || strings.HasPrefix(tag, "local:") {
				chain = append(chain, tag)
			}
		}
		// Mihomo reports chains leaf first. Present the matched policy first.
		slices.Reverse(chain)
		key := strings.Join(chain, "\x00") + "\x01" + c.Outbound
		if seen[key] {
			continue
		}
		seen[key] = true
		item := RouteObservation{Chain: chain, Outbound: c.Outbound, Time: c.Created, Evidence: c.Result}
		if len(chain) != 0 {
			item.Group = chain[0]
		}
		out = append(out, item)
	}
	return out
}
func (r *Records) Logs(offset, limit int, source, level, q string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Event{}
	limit = min(max(limit, 1), 100)
	q = strings.ToLower(q)
	for j := len(r.events) - 1; j >= 0 && len(out) < limit; j-- {
		v := r.events[j]
		if source != "" && v.Source != source || level != "" && v.Level != level || q != "" && !strings.Contains(strings.ToLower(v.Message), q) {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		out = append(out, v)
	}
	return out
}
func sliceConnection(c control.Connection) control.Connection {
	// Bound individual values as well as record count on a low-memory router.
	if len(c.Chain) > 32 {
		c.Chain = slices.Clone(c.Chain[:32])
	}
	values := []*string{&c.ID, &c.Source, &c.Destination, &c.Network, &c.Rule, &c.Outbound}
	for _, s := range values {
		if len(*s) > 512 {
			*s = (*s)[:512]
		}
	}
	for i, s := range c.Chain {
		if len(s) > 128 {
			c.Chain[i] = s[:128]
		}
	}
	return c
}
