// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package subscription

import (
	"encoding/json"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"net/netip"
	"slices"
	"strings"
)

// MaxRuleEntries bounds the server rule catalog.
const MaxRuleEntries = 128

var ruleKey = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// CatalogEntry is one validated entry of GET <api>/api/subscribe/rules:
// { key, value, policy: DIRECT|PROXY, enabled, sort }, where value is a list URL or the list.
type CatalogEntry struct {
	Key     string
	Policy  string
	Enabled bool
	Sort    int
	URL     string
	Inline  string
}

var urlRe = lazyre.New(`^https?://\S+$`)

// NormalizeCatalog validates and orders the catalog (sort, then key).
func NormalizeCatalog(raw json.RawMessage) ([]CatalogEntry, []string) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		var wrapped struct {
			List []json.RawMessage `json:"list"`
		}
		if json.Unmarshal(raw, &wrapped) != nil || wrapped.List == nil {
			return nil, []string{"not a list"}
		}
		list = wrapped.List
	}
	var out []CatalogEntry
	var errs []string
	seen := map[string]bool{}
	for i, item := range list {
		if i >= MaxRuleEntries {
			break
		}
		var e struct {
			Key     any   `json:"key"`
			Value   any   `json:"value"`
			Policy  any   `json:"policy"`
			Enabled *bool `json:"enabled"`
			Sort    any   `json:"sort"`
		}
		if json.Unmarshal(item, &e) != nil {
			errs = append(errs, "invalid entry")
			continue
		}
		key, _ := e.Key.(string)
		if !ruleKey().MatchString(key) {
			errs = append(errs, fmt.Sprintf("invalid key: %v", e.Key))
			continue
		}
		if seen[key] {
			errs = append(errs, "duplicate key: "+key)
			continue
		}
		policy := strings.ToUpper(fmt.Sprint(e.Policy))
		if policy != "DIRECT" && policy != "PROXY" {
			errs = append(errs, fmt.Sprintf("unsupported policy for %s: %v", key, e.Policy))
			continue
		}
		value, _ := e.Value.(string)
		if strings.TrimSpace(value) == "" {
			errs = append(errs, "empty value for "+key)
			continue
		}
		seen[key] = true
		c := CatalogEntry{Key: key, Policy: policy, Enabled: e.Enabled == nil || *e.Enabled}
		if f, ok := e.Sort.(float64); ok && f == float64(int(f)) {
			c.Sort = int(f)
		}
		if v := strings.TrimSpace(value); !strings.Contains(value, "\n") && urlRe().MatchString(v) {
			c.URL = v
		} else {
			c.Inline = value
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b CatalogEntry) int {
		if a.Sort != b.Sort {
			return a.Sort - b.Sort
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out, errs
}

// RuleList is a parsed list. GEOIP entries are kept apart: CN maps to the bundled set.
type RuleList struct {
	Domain, Suffix, Keyword, Regex, CIDR []string
	GeoIP                                []string
	Count, Skipped                       int
}

var domainRe = lazyre.New(`^[A-Za-z0-9_*]([A-Za-z0-9_.-]*[A-Za-z0-9_])?$`)

func cidrOrIP(v string) bool {
	if _, err := netip.ParsePrefix(v); err == nil {
		return true
	}
	_, err := netip.ParseAddr(v)
	return err == nil
}

// ParseRuleList reads a Clash style list: classical lines ("DOMAIN-SUFFIX,example.com"),
// plain domain / CIDR lines, optionally inside a YAML "payload:" block.
func ParseRuleList(text string) RuleList {
	var r RuleList
	add := func(dst *[]string, v string) {
		if !slices.Contains(*dst, v) {
			*dst = append(*dst, v)
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || line == "payload:" {
			continue
		}
		if strings.HasPrefix(line, "-") {
			line = strings.TrimSpace(line[1:])
		}
		line = strings.TrimSpace(strings.Trim(line, `'"`))
		if line == "" {
			continue
		}
		if strings.Contains(line, ",") {
			f := strings.Split(line, ",")
			kind, v := strings.ToUpper(strings.TrimSpace(f[0])), strings.TrimSpace(f[1])
			if v == "" {
				r.Skipped++
				continue
			}
			switch kind {
			case "DOMAIN":
				add(&r.Domain, strings.ToLower(v))
			case "DOMAIN-SUFFIX":
				add(&r.Suffix, strings.ToLower(strings.TrimLeft(v, ".")))
			case "DOMAIN-KEYWORD":
				add(&r.Keyword, strings.ToLower(v))
			case "DOMAIN-REGEX":
				add(&r.Regex, v)
			case "IP-CIDR", "IP-CIDR6":
				if !cidrOrIP(v) {
					r.Skipped++
					continue
				}
				add(&r.CIDR, v)
			case "GEOIP":
				add(&r.GeoIP, strings.ToLower(v))
			default: // PROCESS-NAME, MATCH, ... mean nothing on a router
				r.Skipped++
				continue
			}
			r.Count++
			continue
		}
		switch {
		case strings.Contains(line, "/") && cidrOrIP(line):
			add(&r.CIDR, line)
		case strings.HasPrefix(line, "+."):
			add(&r.Suffix, strings.ToLower(line[2:]))
		case strings.HasPrefix(line, "."):
			add(&r.Suffix, strings.ToLower(line[1:]))
		case domainRe().MatchString(line) && strings.Contains(line, "."):
			add(&r.Domain, strings.ToLower(line))
		default:
			r.Skipped++
			continue
		}
		r.Count++
	}
	return r
}

type ruleFile struct {
	Version int              `json:"version"`
	Rules   []map[string]any `json:"rules"`
}

// Files renders the list as the domain / ip files mihomo.LoadServerRules reads; nil when empty.
func (r RuleList) Files() (domain, ip []byte) {
	d := map[string]any{}
	for k, v := range map[string][]string{"domain": r.Domain, "domain_suffix": r.Suffix, "domain_keyword": r.Keyword, "domain_regex": r.Regex} {
		if len(v) > 0 {
			d[k] = v
		}
	}
	if len(d) > 0 {
		domain, _ = json.Marshal(ruleFile{Version: 3, Rules: []map[string]any{d}})
		domain = append(domain, '\n')
	}
	if len(r.CIDR) > 0 {
		ip, _ = json.Marshal(ruleFile{Version: 3, Rules: []map[string]any{{"ip_cidr": r.CIDR}}})
		ip = append(ip, '\n')
	}
	return
}

// IndexEntry is one line of rules/index.json (also what rules_list reports).
type IndexEntry struct {
	Key       string   `json:"key"`
	Policy    string   `json:"policy"`
	Enabled   bool     `json:"enabled"`
	Sort      int      `json:"sort"`
	HasDomain bool     `json:"has_domain"`
	HasIP     bool     `json:"has_ip"`
	GeoIP     []string `json:"geoip"`
	Count     int      `json:"count"`
	Skipped   int      `json:"skipped"`
	Source    string   `json:"source"`
	Stale     bool     `json:"stale,omitempty"`
}
