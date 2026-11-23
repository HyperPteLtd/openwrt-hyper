package subscription

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/proxyspec"
)

func set(words string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Fields(words) {
		out[v] = true
	}
	return out
}

func list(v any) []any            { a, _ := v.([]any); return a }
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func textlist(v any) ([]string, bool) {
	if s, ok := v.(string); ok {
		return []string{s}, true
	}
	if ss, ok := v.([]string); ok {
		return ss, true
	}
	a, ok := v.([]any)
	if !ok {
		return nil, false
	}
	r := make([]string, 0, len(a))
	for _, x := range a {
		s, ok := x.(string)
		if !ok || s == "" {
			return nil, false
		}
		r = append(r, s)
	}
	return r, true
}
func resolve(p *Profile) {
	names := map[string]string{Direct: Direct, Reject: Reject}
	ambiguous := map[string]bool{}
	add := func(name, id string) {
		if old := names[name]; old != "" && old != id {
			ambiguous[name] = true
		}
		names[name] = id
	}
	special := map[string]bool{}
	seen := map[string]bool{}
	unique := p.Nodes[:0]
	for _, n := range p.Nodes {
		if n.Special {
			special[n.ID] = true
		}
		add(n.Name, n.ID)
		if !seen[n.ID] {
			seen[n.ID] = true
			unique = append(unique, n)
		}
	}
	p.Nodes = unique
	for i := range p.Groups {
		g := &p.Groups[i]
		if seen["group:"+g.Name] {
			ambiguous[g.Name] = true
		}
		seen["group:"+g.Name] = true
		g.ID = ref("group", []string{p.Source, g.Name})
		add(g.Name, g.ID)
	}
	bad := func(name string) bool { return ambiguous[name] || names[name] == "" }
	p.chains(names, bad, special)
	for i := range p.Groups {
		g := &p.Groups[i]
		if ambiguous[g.Name] {
			p.notice("ambiguous_group", g.Name, false)
		}
		var retained []string
		for _, m := range g.Members {
			if bad(m) || special[names[m]] {
				p.notice("mihomo_unavailable_member", g.Name, false)
				continue
			}
			retained = append(retained, names[m])
		}
		g.Members = retained
		if len(g.Members) == 0 {
			p.notice("invalid_group", g.Name, false)
		}
	}
	if names["DIRECT"] == "" {
		names["DIRECT"] = Direct
	}
	if names["REJECT"] == "" {
		names["REJECT"] = Reject
	}
	groups := map[string]Group{}
	for _, g := range p.Groups {
		groups[g.ID] = g
	}
	marks := map[string]uint8{}
	var visit func(string) bool
	visit = func(id string) bool {
		if marks[id] == 1 {
			return true
		}
		if marks[id] == 2 {
			return false
		}
		marks[id] = 1
		for _, m := range groups[id].Members {
			if _, ok := groups[m]; ok && visit(m) {
				return true
			}
		}
		marks[id] = 2
		return false
	}
	for _, g := range p.Groups {
		if visit(g.ID) {
			p.notice("cyclic_group", g.Name, false)
			break
		}
	}
	target := func(t, at string) string {
		if bad(t) || special[names[t]] {
			p.notice("unresolved_target", at, t == Reject || strings.EqualFold(t, "REJECT"))
			return ""
		}
		id := names[t]
		if strings.HasPrefix(id, "node:") {
			gid := ref("group", []string{p.Source, "single", id})
			if _, exists := groups[gid]; !exists {
				g := Group{ID: gid, Name: t, Kind: "selector", Members: []string{id}}
				p.Groups = append(p.Groups, g)
				groups[gid] = g
			}
			return gid
		}
		return id
	}
	for i := range p.Rules {
		p.Rules[i].Target = target(p.Rules[i].Target, fmt.Sprintf("rule %d", i+1))
	}
	if p.Final != "" {
		p.Final = target(p.Final, "final")
	}
}

// chains resolves each node's dialer-proxy to the ID of a node or group of the same document,
// as the core names them. The core refuses a whole configuration over a chain that names
// nothing or can lead back to its own node, so such a node is dropped instead, and so is
// every chain through it; references to it then resolve as to any other missing node.
func (p *Profile) chains(names map[string]string, bad func(string) bool, special map[string]bool) {
	via := map[string]string{}
	drop := map[string]string{}
	for _, n := range p.Nodes {
		name := n.Dialer()
		switch id := names[name]; {
		case name == "":
		case clashTarget(name) == Direct:
			delete(n.Outbound, "dialer-proxy")
		case bad(name) || special[id] || id == Reject:
			drop[n.ID] = "dialer_proxy_unavailable"
		default:
			via[n.ID] = id
		}
	}
	// A group in a chain may pick any member, so a member that leads back counts as a cycle.
	edges := map[string][]string{}
	for n, id := range via {
		edges[n] = []string{id}
	}
	for _, g := range p.Groups {
		for _, m := range g.Members {
			if !bad(m) {
				edges[g.ID] = append(edges[g.ID], names[m])
			}
		}
	}
	// Tarjan's strongly connected components: a chained node on a cycle can reach itself.
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var connect func(string)
	connect = func(v string) {
		index[v], low[v] = len(index), len(index)
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range edges[v] {
			if _, seen := index[w]; !seen {
				connect(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		top := len(stack) - 1
		for stack[top] != v {
			top--
		}
		component := stack[top:]
		stack = stack[:top]
		for _, w := range component {
			onStack[w] = false
		}
		if len(component) > 1 || slices.Contains(edges[v], v) {
			for _, w := range component {
				if via[w] != "" {
					drop[w] = "dialer_proxy_cycle"
				}
			}
		}
	}
	for _, n := range p.Nodes {
		if _, seen := index[n.ID]; via[n.ID] != "" && !seen {
			connect(n.ID)
		}
	}
	for changed := true; changed; {
		changed = false
		for n, id := range via {
			if drop[n] == "" && drop[id] != "" {
				drop[n] = "dialer_proxy_unavailable"
				changed = true
			}
		}
	}
	kept := p.Nodes[:0]
	for _, n := range p.Nodes {
		if code := drop[n.ID]; code != "" {
			p.notice(code, n.Name, false)
			continue
		}
		if id := via[n.ID]; id != "" {
			n.Outbound["dialer-proxy"] = id
		}
		kept = append(kept, n)
	}
	p.Nodes = kept
	for name, id := range names {
		if drop[id] != "" {
			delete(names, name)
		}
	}
}

// parseClash reads a Clash document. The router checks what is its own concern: the safety of
// each proxy (proxyspec.Safe), its limits, and the groups and rules it compiles. Providers are
// not read: a profile's rule sets are its own (BinarySets).
func parseClash(p *Profile, m map[string]any) error {
	for _, k := range []string{"proxy-providers", "rule-providers"} {
		if len(object(m[k])) > 0 {
			if k == "rule-providers" {
				p.notice("mihomo_unsupported_rule_set", k, false)
			} else {
				p.notice("provider_required", k, false)
			}
		}
	}
	if len(list(m["proxies"])) > maxNodes || len(list(m["proxy-groups"])) > maxNodes {
		return errors.New("node_limit")
	}
	for i, item := range list(m["proxies"]) {
		proxy := object(item)
		name := field(proxy, "name")
		err := proxyspec.Safe(proxy)
		if err == nil {
			out := maps.Clone(proxy)
			delete(out, "name")
			err = p.addProxy(name, proxyProtocol(out), out)
		}
		if err != nil {
			p.notice(err.Error(), first(publicLabel(name, ""), fmt.Sprintf("proxy %d", i+1)), false)
		}
	}
	for _, item := range list(m["proxy-groups"]) {
		v := object(item)
		name := field(v, "name")
		g := Group{Name: name, Options: map[string]any{}}
		switch field(v, "type") {
		case "select":
			g.Kind = "selector"
		case "url-test":
			g.Kind = "urltest"
		case "fallback":
			g.Kind = "fallback"
		case "load-balance":
			g.Kind = "loadbalance"
		default:
			p.notice("unsupported_group", name, false)
			continue
		}
		members, ok := textlist(v["proxies"])
		if !safeLabel(name) || !ok || len(members) == 0 {
			p.notice("invalid_group", name, false)
			continue
		}
		for _, v := range members {
			g.Members = append(g.Members, clashTarget(v))
		}
		for k, x := range v {
			switch k {
			case "name", "type", "proxies":
			case "hidden":
				g.Hidden, _ = x.(bool)
			case "icon":
			// External display resources are controlled by the client.
			case "url", "tolerance", "strategy", "lazy", "expected-status", "timeout", "max-failed-times", "disable-udp":
				g.Options[k] = x
			case "interval":
				g.Options[k] = fmt.Sprint(x) + "s"
			default:
				p.notice("unsupported_group_option", name, false)
			}
		}
		p.Groups = append(p.Groups, g)
	}
	if len(list(m["rules"])) > maxRules {
		return errors.New("rule_limit")
	}
	for i, e := range list(m["rules"]) {
		s, _ := e.(string)
		if kind, name, _, ok := geoRule(s); ok && !clashDefaultGeo(m, kind) {
			p.notice("rule_provider_custom_geo", kind+" "+name, rejects(s))
			continue
		}
		parseMihomoRule(p, s, i+1)
	}
	return nil
}
func clashTarget(s string) string {
	switch s {
	case "DIRECT":
		return Direct
	case "REJECT":
		return Reject
	}
	return s
}
