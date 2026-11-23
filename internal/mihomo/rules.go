// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package mihomo

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ServerRule is one rule list from GET <api>/api/subscribe/rules, in server order.
type ServerRule struct {
	Key     string
	Direct  bool // policy DIRECT; otherwise proxied (main-out)
	GeoIPCN bool // the list also covers mainland IPs (geoip: ["cn"])

	Domain, Suffix, Keyword, Regex []string
	CIDR                           []string
}

func (r ServerRule) empty() bool {
	return len(r.Domain)+len(r.Suffix)+len(r.Keyword)+len(r.Regex)+len(r.CIDR) == 0 && !(r.Direct && r.GeoIPCN)
}

type ruleIndexEntry struct {
	Key       string   `json:"key"`
	Policy    string   `json:"policy"`
	Enabled   bool     `json:"enabled"`
	HasDomain bool     `json:"has_domain"`
	HasIP     bool     `json:"has_ip"`
	GeoIP     []string `json:"geoip"`
}

// ruleFile is the list format on disk: rules[] of domain / domain_suffix /
// domain_keyword / domain_regex / ip_cidr arrays.
type ruleFile struct {
	Rules []struct {
		Domain        []string `json:"domain"`
		DomainSuffix  []string `json:"domain_suffix"`
		DomainKeyword []string `json:"domain_keyword"`
		DomainRegex   []string `json:"domain_regex"`
		IPCIDR        []string `json:"ip_cidr"`
	} `json:"rules"`
}

// LoadServerRules reads the enabled lists in index order. A missing index means no lists
// (not signed in or never synced).
func LoadServerRules(dir string, disabled []string) ([]ServerRule, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var index []ruleIndexEntry
	if err = json.Unmarshal(b, &index); err != nil {
		return nil, fmt.Errorf("rules index: %w", err)
	}
	var out []ServerRule
	for _, e := range index {
		if !e.Enabled || !rulekey.Valid(e.Key) || slices.Contains(disabled, e.Key) {
			continue
		}
		r := ServerRule{Key: e.Key, Direct: e.Policy == "DIRECT", GeoIPCN: slices.Contains(e.GeoIP, "cn")}
		for _, part := range []struct {
			has  bool
			name string
		}{{e.HasDomain, ".domain.json"}, {e.HasIP, ".ip.json"}} {
			if !part.has {
				continue
			}
			var f ruleFile
			raw, err := os.ReadFile(filepath.Join(dir, rulekey.ID(e.Key)+part.name))
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &f); err != nil {
				return nil, fmt.Errorf("rule list %s: %w", e.Key, err)
			}
			for _, x := range f.Rules {
				r.Domain = append(r.Domain, x.Domain...)
				r.Suffix = append(r.Suffix, x.DomainSuffix...)
				r.Keyword = append(r.Keyword, x.DomainKeyword...)
				r.Regex = append(r.Regex, x.DomainRegex...)
				r.CIDR = append(r.CIDR, x.IPCIDR...)
			}
		}
		if !r.empty() {
			out = append(out, r)
		}
	}
	return out, nil
}

// LoadList reads a direct_list / proxy_list file: one domain keyword per line.
func LoadList(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, nil
}
