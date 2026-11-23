package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go.yaml.in/yaml/v3"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

type accountRefresh struct {
	http.RoundTripper
	configURL          string
	detailURL          string
	expectedAccess     string
	details            int
	shareURL           string
	shareRequests      int
	requests           int
	publicRuleRequests int
	warnings           uint64
	warningCodes       string
}

func (a *accountRefresh) RoundTrip(r *http.Request) (*http.Response, error) {
	isDetail := r.URL.String() == a.detailURL
	if isDetail {
		a.details++
		if r.Header.Get("Authorization") == "" || r.Header.Get("Cookie") != "" {
			return nil, errors.New("expected authenticated access to the owned plan")
		}
	}
	isConfig := r.URL.Scheme+"://"+r.URL.Host+r.URL.Path == a.configURL
	if a.shareURL != "" && r.URL.String() == a.shareURL {
		a.shareRequests++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			return nil, errors.New("subscription link request carried account credentials")
		}
	}
	if isConfig {
		a.requests++
		if r.URL.RawQuery != "" || !strings.HasPrefix(r.UserAgent(), "Hyper/") || !strings.HasSuffix(r.UserAgent(), " (OpenWrt)") || r.Header.Get("Authorization") == "" {
			return nil, errors.New("expected an authenticated Hyper UA config request without query overrides")
		}
	}
	if strings.Contains(r.URL.Path, "/subscriptions/rulesets/") {
		a.publicRuleRequests++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			return nil, errors.New("anonymous rule request carried account credentials")
		}
	}
	resp, err := a.RoundTripper.RoundTrip(r)
	if err == nil && isDetail {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (128<<10)+1))
		resp.Body.Close()
		var detail struct {
			SubscriptionAccess string
			URL                *string
		}
		if readErr != nil || resp.StatusCode != http.StatusOK || len(body) > 128<<10 || json.Unmarshal(body, &detail) != nil || detail.SubscriptionAccess != a.expectedAccess {
			return nil, errors.New("unexpected access policy on owned plan")
		}
		if (detail.URL == nil) != (a.expectedAccess == "app") {
			return nil, errors.New("unexpected issued link for plan access policy")
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err == nil && isConfig {
		a.warningCodes = resp.Header.Get("X-Routing-Warning-Codes")
		a.warnings, _ = strconv.ParseUint(resp.Header.Get("X-Routing-Warnings"), 10, 32)
	}
	return resp, err
}

// Compare the independently fetched native YAML with the snapshot produced by
// the signed-in or anonymous refresh path. All comparisons use identities.
func verifyNativeProfile(body []byte, p subscription.Profile, inventory []string) error {
	var exported struct {
		Proxies []struct{ Name, Type string }
		Groups  []struct {
			Name, Type string
			Proxies    []string
		} `yaml:"proxy-groups"`
		Providers map[string]any `yaml:"rule-providers"`
		Rules     []string
	}
	if err := yaml.Unmarshal(body, &exported); err != nil {
		return errors.New("native template is not Clash YAML")
	}
	if p.Local || len(exported.Groups) == 0 || len(exported.Rules) == 0 || len(exported.Proxies) != len(p.Nodes) || len(p.Groups) != len(exported.Groups) {
		return fmt.Errorf("native snapshot shape mismatch: exported/saved nodes %d/%d groups %d/%d rules %d/%d", len(exported.Proxies), len(p.Nodes), len(exported.Groups), len(p.Groups), len(exported.Rules), len(p.Rules))
	}
	ids := map[string]string{"DIRECT": subscription.Direct, "REJECT": subscription.Reject}
	for _, node := range p.Nodes {
		if node.Special || node.Protocol == "mtproxy" {
			return errors.New("specialized protocol present as a general proxy")
		}
		ids[node.Name] = node.ID
	}
	for _, node := range exported.Proxies {
		if ids[node.Name] == "" {
			return errors.New("exported node missing from imported snapshot")
		}
	}
	for _, g := range p.Groups {
		ids[g.Name] = g.ID
	}
	for j, group := range exported.Groups {
		g := p.Groups[j]
		if g.Name != group.Name || len(group.Proxies) != len(g.Members) {
			return errors.New("saved group order or member count differs")
		}
		for k, name := range group.Proxies {
			if g.Members[k] != ids[name] {
				return errors.New("group member differs from native export")
			}
		}
	}
	for name := range exported.Providers {
		if !slices.ContainsFunc(p.BinarySets, func(s subscription.BinarySet) bool { return s.Tag == name }) {
			return errors.New("exported provider missing from committed snapshot")
		}
	}
	n := 0
	for _, line := range exported.Rules {
		fields := strings.Split(line, ",")
		end := len(fields) - 1
		if end < 1 {
			return errors.New("invalid rule in native export")
		}
		if fields[end] == "no-resolve" {
			end--
		}
		target := ids[fields[end]]
		if target == "" {
			return errors.New("native rule target not found")
		}
		if fields[0] == "MATCH" {
			if target != p.Final {
				return errors.New("default target mismatch")
			}
			continue
		}
		if n >= len(p.Rules) || p.Rules[n].Target != target {
			return errors.New("ordered rule target mismatch")
		}
		prefix, _ := p.Rules[n].Match["clash"].(string)
		want := strings.Join(fields[:end], ",")
		switch strings.ToUpper(want) {
		case "GEOIP,CN":
			want = "RULE-SET," + subscription.DomesticIPSet
		case "GEOSITE,CN":
			want = "RULE-SET," + subscription.DomesticDomainSet
		}
		if prefix != want {
			return fmt.Errorf("native matcher differs at rule %d", n+1)
		}
		n++
	}
	if n != len(p.Rules) {
		return errors.New("committed snapshot has unexpected extra rules")
	}
	if len(inventory) != 16 || len(exported.Proxies) != len(inventory)-1 {
		return errors.New("synthetic plan must include each protocol and export all except MTProxy")
	}
	return nil
}
