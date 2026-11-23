// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package hypersync

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
)

// MaxRuleEntries bounds the server rule catalog.
const MaxRuleEntries = 128

// CatalogEntry is one validated entry of GET <api>/api/subscribe/rules:
// { key, value, policy: DIRECT|PROXY, enabled, sort }, where value is a list URL or the list.
// Keys are readable names (rulekey.Valid); files take rulekey.ID.
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
		if !rulekey.Valid(key) {
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
