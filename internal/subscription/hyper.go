// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/hypersync"
	"github.com/HyperPteLtd/openwrt-hyper/internal/links"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
)

// The Hyper source: the signed-in account's nodes and the server's rule catalog
// (hypersync), read as a Clash document so the router checks it as any subscription.
const (
	HyperSource = "hyper"
	HyperGroup  = "Hyper"
	// hyperSetPrefix starts the tags of the catalog's rule sets: list-<rulekey.ID>-<kind>-<policy>,
	// so a later read whose list or whole catalog fails keeps them.
	hyperSetPrefix = "list-"
	maxDisabled    = 128
)

// HyperAccount is the account a hyper source reads; the command line sets it.
var HyperAccount *account.Client

// NewHyperSource is the configuration of the hyper source; disabled names catalog lists the
// administrator turned off.
func NewHyperSource(disabled []string) Source {
	return Source{ID: HyperSource, Name: HyperGroup, Kind: HyperSource, Rules: RoutingOwn, Disabled: disabled}
}

func (s Source) validateHyper() error {
	if s.ID != HyperSource || s.URL != "" || s.Token != "" || s.Origin != "" || s.UserID != "" || s.PlanID != 0 || len(s.Filters) > 0 || len(s.Disabled) > maxDisabled {
		return errors.New("invalid_hyper_source")
	}
	for _, k := range s.Disabled {
		if !rulekey.Valid(k) {
			return errors.New("invalid_hyper_source")
		}
	}
	return nil
}

// readHyper reads the account's feed over the router's ways out (fallback.go) and builds its
// profile, keeping what a partial failure could not read from the previous one.
func (f Fetcher) readHyper(ctx context.Context, s Source, usage **Usage) (Profile, error) {
	acct, alt := f.HyperClient()
	if acct == nil || acct.Sessions.Load() == nil {
		return Profile{}, ErrLoginRequired
	}
	feed, err := (&hypersync.Reader{Client: acct, Alt: alt, HTTP: acct.HTTP}).Read(ctx)
	switch {
	case errors.Is(err, hypersync.ErrNotSignedIn), errors.Is(err, hypersync.ErrLoginRequired):
		return Profile{}, ErrLoginRequired
	case errors.Is(err, hypersync.ErrNoSubscription):
		return Profile{}, ErrSuspended
	case errors.Is(err, hypersync.ErrSessionChanged):
		return Profile{}, ErrSuperseded
	case err != nil:
		return Profile{}, err
	}
	var previous *Profile
	if f.previous != nil && f.previous.Current != nil {
		previous = f.previous.Current
	}
	p, err := HyperProfile(s, feed, previous)
	if err == nil {
		*usage = hyperUsage(feed.Status)
	}
	return p, err
}

// HyperClient is HyperAccount over this fetcher's ways out: the usual one, and alt (the router's
// other way, fallback.go) when there is one. Nil without an account.
func (f Fetcher) HyperClient() (client *account.Client, alt string) {
	if HyperAccount == nil {
		return nil, ""
	}
	base := http.Client{Timeout: 30 * time.Second}
	if f.Client != nil {
		base = *f.Client
	}
	// The account API answers where it is; a redirect is never followed with the session.
	base.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var other *route
	if f.Fallback {
		if other = fallbackRoute(); other != nil {
			alt = "fallback"
		}
	}
	acct := *HyperAccount
	acct.HTTP = func(way string) *http.Client {
		if way != "" && other != nil {
			return other.client(base, false)
		}
		c := base
		return &c
	}
	return &acct, alt
}

// HyperList is one catalog list as the rules page shows it.
type HyperList struct {
	Key     string `json:"key"`
	Policy  string `json:"policy"`
	Enabled bool   `json:"server_enabled"` // the server's own switch
	Entries int    `json:"entries"`
	Stale   bool   `json:"stale,omitempty"` // this read kept the previous copy
}

// ConfigureHyper creates or updates the hyper source after a verified sign-in or a change of
// its disabled lists, renewing one a sign-out revoked.
func (s *Store) ConfigureHyper(disabled []string) error {
	return s.configure(NewHyperSource(disabled), true)
}

// HyperDisabled is the hyper source's disabled lists; none without the source.
func (s *Store) HyperDisabled() []string {
	r, err := s.read(HyperSource)
	if err != nil || r.Config == nil {
		return nil
	}
	return slices.Clone(r.Config.Disabled)
}

// HyperProfile builds the profile of a feed: every node in one selector, the catalog's lists
// in catalog order, mainland names and addresses direct, the rest through the selector.
func HyperProfile(s Source, feed *hypersync.Feed, previous *Profile) (Profile, error) {
	p := Profile{SchemaVersion: SchemaVersion, Source: s.ID, Format: HyperSource, Title: firstNonEmpty(feed.Remarks, HyperGroup), Countries: map[string][]string{}}
	var proxies []any
	var names []string
	country := map[string]string{} // node name -> country
	taken := map[string]bool{}
	add := func(code string, proxy map[string]any) {
		name := field(proxy, "name")
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s %d", field(proxy, "name"), i)
		}
		taken[name] = true
		proxy["name"] = name
		proxies = append(proxies, proxy)
		names = append(names, name)
		country[name] = code
	}
	failed := slices.Clone(feed.Failed)
	for _, c := range feed.Countries {
		res, err := links.Convert(c.Body)
		if err != nil {
			failed = append(failed, c.Code)
			continue
		}
		for _, w := range res.Warnings {
			p.Notices = append(p.Notices, Notice{Code: "link_" + w.Code, At: c.Code})
		}
		b, err := res.YAML()
		var doc map[string]any
		if err != nil || yaml.Unmarshal(b, &doc) != nil {
			failed = append(failed, c.Code)
			continue
		}
		for _, item := range list(doc["proxies"]) {
			if m := object(item); m != nil {
				add(c.Code, m)
			}
		}
	}
	// A country this read could not reach keeps its nodes of the previous profile.
	if previous != nil {
		for _, code := range failed {
			for _, id := range previous.Countries[code] {
				if n, ok := previous.node(id); ok {
					m := map[string]any{}
					for k, v := range n.Outbound {
						m[k] = v
					}
					m["name"] = n.Name
					add(code, m)
				}
			}
		}
	}
	if len(names) == 0 {
		return p, errNoUsableNodes
	}

	sets, rules := hyperRules(s, feed.Catalog, previous)
	p.BinarySets = sets
	p.Lists = hyperLists(feed.Catalog, sets, previous)
	rules = append(rules, "GEOSITE,CN,DIRECT", "GEOIP,CN,DIRECT,no-resolve", "MATCH,"+HyperGroup)
	doc := map[string]any{
		"proxies":      proxies,
		"proxy-groups": []any{map[string]any{"name": HyperGroup, "type": "select", "proxies": toAnyList(names)}},
		"rules":        toAnyList(rules),
	}
	if err := parseClash(&p, doc); err != nil {
		return p, err
	}
	if len(p.Nodes) == 0 {
		return p, errNoUsableNodes
	}
	resolve(&p)
	if len(p.Nodes) == 0 {
		return p, errNoUsableNodes
	}
	for _, n := range p.Nodes {
		if code := country[n.Name]; code != "" {
			p.Countries[code] = append(p.Countries[code], n.ID)
		}
	}
	return p, nil
}

func (p *Profile) node(id string) (Node, bool) {
	for _, n := range p.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func toAnyList(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// hyperRules turns the catalog into rule sets and their rules. A list that could not be read,
// or the whole catalog, keeps its sets of the previous profile.
func hyperRules(s Source, c *hypersync.Catalog, previous *Profile) ([]BinarySet, []string) {
	var sets []BinarySet
	var rules []string
	keep := func(id string) {
		if previous == nil {
			return
		}
		for _, set := range previous.BinarySets {
			if strings.HasPrefix(set.Tag, hyperSetPrefix+id+"-") {
				sets = append(sets, set)
				rules = append(rules, hyperRule(set))
			}
		}
	}
	if c == nil {
		if previous != nil {
			for _, set := range previous.BinarySets {
				if strings.HasPrefix(set.Tag, hyperSetPrefix) {
					sets = append(sets, set)
					rules = append(rules, hyperRule(set))
				}
			}
		}
		return sets, rules
	}
	for _, l := range c.Lists {
		if !l.Enabled || slices.Contains(s.Disabled, l.Key) {
			continue
		}
		id := rulekey.ID(l.Key)
		if l.Parsed == nil {
			keep(id)
			continue
		}
		policy := "proxy"
		if l.Policy == "DIRECT" {
			policy = "direct"
		}
		for _, part := range []struct {
			kind, behavior string
			body           []byte
		}{
			{"domain", "domain", domainList(l.Parsed.Domain, l.Parsed.Suffix)},
			{"keyword", "classical", classicalList(l.Parsed.Keyword, l.Parsed.Regex)},
			{"ip", "ipcidr", cidrList(l.Parsed.CIDR)},
		} {
			if len(part.body) == 0 {
				continue
			}
			sum := sha256.Sum256(part.body)
			set := BinarySet{Tag: hyperSetPrefix + id + "-" + part.kind + "-" + policy, Format: "text", Behavior: part.behavior,
				Entries: strings.Count(string(part.body), "\n"), SHA256: hex.EncodeToString(sum[:]), Data: part.body}
			sets = append(sets, set)
			rules = append(rules, hyperRule(set))
		}
	}
	return sets, rules
}

// hyperRule is the rule of one catalog set, its policy taken from the tag.
func hyperRule(set BinarySet) string {
	target := HyperGroup
	if strings.HasSuffix(set.Tag, "-direct") {
		target = "DIRECT"
	}
	rule := "RULE-SET," + set.Tag + "," + target
	if set.Behavior == "ipcidr" {
		rule += ",no-resolve"
	}
	return rule
}

var hyperDomain = regexp.MustCompile(`^[A-Za-z0-9_*]([A-Za-z0-9_.-]*[A-Za-z0-9_])?$`)

func domainList(exact, suffix []string) []byte {
	var b strings.Builder
	for _, d := range exact {
		if hyperDomain.MatchString(d) {
			b.WriteString(strings.ToLower(strings.TrimSuffix(d, ".")) + "\n")
		}
	}
	for _, d := range suffix {
		d = strings.TrimPrefix(d, ".")
		if hyperDomain.MatchString(d) {
			b.WriteString("+." + strings.ToLower(strings.TrimSuffix(d, ".")) + "\n")
		}
	}
	return []byte(b.String())
}

func classicalList(keyword, regex []string) []byte {
	var b strings.Builder
	for _, k := range keyword {
		if k != "" && !strings.ContainsAny(k, ",\r\n\x00 ") {
			b.WriteString("DOMAIN-KEYWORD," + k + "\n")
		}
	}
	for _, r := range regex {
		if _, err := regexp.Compile(r); err == nil && r != "" && !strings.ContainsAny(r, ",\r\n\x00") {
			b.WriteString("DOMAIN-REGEX," + r + "\n")
		}
	}
	return []byte(b.String())
}

func cidrList(list []string) []byte {
	var b strings.Builder
	for _, c := range list {
		if p, err := netip.ParsePrefix(c); err == nil {
			b.WriteString(p.Masked().String() + "\n")
		} else if a, err := netip.ParseAddr(c); err == nil {
			a = a.Unmap().WithZone("")
			b.WriteString(netip.PrefixFrom(a, a.BitLen()).String() + "\n")
		}
	}
	return []byte(b.String())
}

// hyperUsage reads the subscription's "Traffic: <used> / <total> | Expires: <date>".
func hyperUsage(status string) *Usage {
	used, total, expires := hypersync.ParseStatus(status)
	u, ok := sizeBytes(used)
	if !ok {
		return nil
	}
	usage := &Usage{Download: u}
	if t, ok := sizeBytes(total); ok && t > 0 {
		usage.Total = &t
	}
	if d, err := time.Parse("2006-01-02", expires); err == nil {
		usage.Expires = d.UTC().Format(time.RFC3339)
	}
	return usage
}

var sizeText = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([KMGTP]?)i?B$`)

func sizeBytes(s string) (uint64, bool) {
	m := sizeText.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	if m[2] != "" {
		n *= math.Pow(1024, float64(strings.Index("KMGTP", m[2])+1))
	}
	if m[2] == "" {
		n = math.Floor(n)
	}
	if n < 0 || n > 1<<53 {
		return 0, false
	}
	return uint64(n), true
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// hyperLists summarises the catalog for the rules page, the previous one when it could not be read.
func hyperLists(c *hypersync.Catalog, sets []BinarySet, previous *Profile) []HyperList {
	if c == nil {
		if previous != nil {
			return previous.Lists
		}
		return nil
	}
	var out []HyperList
	for _, l := range c.Lists {
		h := HyperList{Key: l.Key, Policy: l.Policy, Enabled: l.Enabled, Stale: l.Parsed == nil}
		prefix := hyperSetPrefix + rulekey.ID(l.Key) + "-"
		for _, set := range sets {
			if strings.HasPrefix(set.Tag, prefix) {
				h.Entries += set.Entries
			}
		}
		out = append(out, h)
	}
	return out
}
