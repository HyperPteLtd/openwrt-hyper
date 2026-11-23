package router

import (
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"maps"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type NodeRef struct {
	Source string `json:"source"`
	ID     string `json:"node"`
}
type LocalGroup struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Members []NodeRef `json:"members"`
}
type Telegram struct {
	Node      NodeRef        `json:"node"`
	Interface string         `json:"interface"`
	Address   netip.Addr     `json:"address"`
	Port      uint16         `json:"port"`
	Allow     []netip.Prefix `json:"allow"`
}

var localID = regexp.MustCompile("^local:[a-zA-Z0-9_-]{1,64}$")

func (p Policy) refs() []NodeRef {
	var r []NodeRef
	for _, g := range p.Groups {
		r = append(r, g.Members...)
	}
	if p.Telegram != nil {
		r = append(r, p.Telegram.Node)
	}
	return r
}
func (i Intent) ReferencesSource(source string) bool {
	if i.Source == source {
		return true
	}
	for _, r := range i.Policy.refs() {
		if r.Source == source {
			return true
		}
	}
	return false
}

type profileStore interface {
	Load(string) (subscription.Profile, error)
}

func LoadProfile(i Intent, s profileStore) (subscription.Profile, error) {
	p, e := s.Load(i.Source)
	if e != nil {
		return p, e
	}
	bySource := map[string]map[string]subscription.Node{}
	seen := map[string]bool{}
	for _, n := range p.Nodes {
		seen[n.ID] = true
	}
	primary := maps.Clone(seen)
	for _, r := range i.Policy.refs() {
		if r.Source == i.Source {
			if !primary[r.ID] {
				return p, errors.New("referenced primary node no longer exists")
			}
			continue
		}
		set, ok := bySource[r.Source]
		if !ok {
			other, e := s.Load(r.Source)
			if e != nil {
				return p, errors.New("referenced subscription unavailable")
			}
			set = map[string]subscription.Node{}
			for _, n := range other.Nodes {
				set[n.ID] = n
			}
			bySource[r.Source] = set
		}
		n, ok := set[r.ID]
		if !ok {
			return p, errors.New("referenced node no longer exists")
		}
		if !seen[n.ID] {
			p.Nodes = append(p.Nodes, n)
			seen[n.ID] = true
		}
	}
	return p, nil
}
func validateRefs(p Policy) error {
	if len(p.Groups) > 128 {
		return errors.New("too many local groups")
	}
	ids := map[string]bool{}
	for _, g := range p.Groups {
		if !localID.MatchString(g.ID) || ids[g.ID] || strings.TrimSpace(g.Name) == "" || len(g.Name) > 128 || len(g.Members) == 0 || len(g.Members) > 1024 {
			return errors.New("invalid local group")
		}
		ids[g.ID] = true
	}
	for _, r := range p.refs() {
		if len(r.Source) == 0 || len(r.Source) > 256 || !strings.HasPrefix(r.ID, "node:") {
			return errors.New("invalid explicit source/node reference")
		}
	}
	if t := p.Telegram; t != nil {
		if !t.Address.IsValid() || t.Address.IsUnspecified() || t.Address.IsMulticast() || t.Address.IsLoopback() || t.Port < 1024 || t.Port >= 19761 && t.Port <= 19764 || len(t.Allow) == 0 || len(t.Allow) > 128 {
			return errors.New("invalid Telegram LAN listener")
		}
	}
	return nil
}
func telegram(t Telegram, p subscription.Profile, n Network) (obj, []string, error) {
	if e := validateRefs(Policy{Telegram: &t}); e != nil {
		return nil, nil, e
	}
	var lan netip.Prefix
	for _, pref := range n.LAN[t.Interface] {
		if pref.Addr() == t.Address {
			lan = pref
			break
		}
	}
	if !lan.IsValid() {
		return nil, nil, errors.New("Telegram listener must use an address of a managed LAN")
	}
	allow := []string{}
	for _, pref := range t.Allow {
		if !pref.IsValid() || pref.Bits() < lan.Bits() || !lan.Contains(pref.Addr()) {
			return nil, nil, errors.New("Telegram client range outside managed LAN")
		}
		allow = append(allow, pref.String())
	}
	for _, node := range p.Nodes {
		if node.ID != t.Node.ID || !node.Special || node.Protocol != "mtproxy" {
			continue
		}

		in := obj{"type": "tunnel", "name": "telegram-lan", "network": []string{"tcp"}, "listen": t.Address.String(), "port": t.Port, "target": net.JoinHostPort(node.Server, strconv.Itoa(node.Port))}
		var rules []string
		for _, prefix := range allow {
			rules = append(rules, "AND,((IN-NAME,telegram-lan),(SRC-IP-CIDR,"+prefix+")),DIRECT")
		}
		return in, append(rules, "IN-NAME,telegram-lan,REJECT"), nil
	}
	return nil, nil, errors.New("Telegram source no longer available")
}
