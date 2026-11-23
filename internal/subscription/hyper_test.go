// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package subscription

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/hypersync"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
)

func hyperFeed(jpPassword string) *hypersync.Feed {
	return &hypersync.Feed{
		Remarks: "Hyper",
		Status:  "Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09",
		Countries: []hypersync.Country{
			{Code: "JP", Sort: 9, Body: []byte("anytls://" + jpPassword + "@jp.example.com:443?sni=jp.example.com#日本 · 东京 01\nnotalink\n")},
			{Code: "SG", Sort: 5, Body: []byte("trojan://pw@sg.example.com:443?sni=sg.example.com#新加坡 01\nfoo://x@y:1#unknown\n")},
		},
		Catalog: &hypersync.Catalog{Lists: []hypersync.List{
			{CatalogEntry: hypersync.CatalogEntry{Key: "中国常用域名", Policy: "DIRECT", Enabled: true, Sort: 10}, Parsed: &hypersync.RuleList{Suffix: []string{"baidu.com"}, CIDR: []string{"1.0.1.0/24"}}},
			{CatalogEntry: hypersync.CatalogEntry{Key: "streaming", Policy: "PROXY", Enabled: true, Sort: 20}, Parsed: &hypersync.RuleList{Suffix: []string{"netflix.com"}, Keyword: []string{"nflx"}}},
			{CatalogEntry: hypersync.CatalogEntry{Key: "off", Policy: "PROXY", Enabled: false, Sort: 30}, Parsed: &hypersync.RuleList{Domain: []string{"off.example"}}},
			{CatalogEntry: hypersync.CatalogEntry{Key: "mine-off", Policy: "PROXY", Enabled: true, Sort: 40}, Parsed: &hypersync.RuleList{Domain: []string{"mine.example"}}},
		}},
	}
}

func clashRule(r Rule) string { s, _ := r.Match["clash"].(string); return s }

func TestHyperProfile(t *testing.T) {
	src := NewHyperSource([]string{"mine-off"})
	if err := src.validate(); err != nil {
		t.Fatal(err)
	}
	p, err := HyperProfile(src, hyperFeed("pw"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Nodes) != 2 || p.Nodes[0].Name != "日本 · 东京 01" || p.Nodes[0].Protocol != "anytls" || p.Nodes[1].Protocol != "trojan" {
		t.Fatalf("%+v", p.Nodes)
	}
	if len(p.Groups) != 1 || p.Groups[0].Name != HyperGroup || !slices.Equal(p.Groups[0].Members, []string{p.Nodes[0].ID, p.Nodes[1].ID}) || p.Final != p.Groups[0].ID {
		t.Fatalf("groups %+v final %q", p.Groups, p.Final)
	}
	if p.Groups[0].ID != HyperGroupID() {
		t.Fatal("the selector's route is not HyperGroupID:", p.Groups[0].ID)
	}
	if !slices.Equal(p.Countries["JP"], []string{p.Nodes[0].ID}) || !slices.Equal(p.Countries["SG"], []string{p.Nodes[1].ID}) {
		t.Fatal(p.Countries)
	}
	var tags []string
	for _, s := range p.BinarySets {
		tags = append(tags, s.Tag)
		if s.Format != "text" || s.SHA256 == "" || s.Entries == 0 {
			t.Errorf("%+v", s)
		}
	}
	cn := rulekey.ID("中国常用域名")
	want := []string{"list-" + cn + "-domain-direct", "list-" + cn + "-ip-direct", "list-streaming-domain-proxy", "list-streaming-keyword-proxy"}
	if !slices.Equal(tags, want) {
		t.Fatalf("sets %v, want %v", tags, want)
	}
	// Catalog order, then mainland direct, then the selector.
	if len(p.Rules) != 6 || p.Rules[0].Target != Direct || p.Rules[2].Target != p.Groups[0].ID {
		t.Fatalf("%+v", p.Rules)
	}
	if !strings.Contains(string(p.BinarySets[0].Data), "+.baidu.com") || string(p.BinarySets[1].Data) != "1.0.1.0/24\n" {
		t.Fatalf("%q %q", p.BinarySets[0].Data, p.BinarySets[1].Data)
	}
	for _, n := range p.Notices {
		if !strings.HasPrefix(n.Code, "link_") {
			t.Errorf("notice %+v", n)
		}
	}
	// A new password keeps the node's ID: the router's selection stays.
	q, err := HyperProfile(src, hyperFeed("rotated"), nil)
	if err != nil || q.Nodes[0].ID != p.Nodes[0].ID || q.Nodes[0].Outbound["password"] != "rotated" {
		t.Fatalf("%v %+v", err, q.Nodes)
	}
}

// What a read could not reach stays as the previous profile had it.
func TestHyperProfileKeepsWhatFailed(t *testing.T) {
	src := NewHyperSource(nil)
	prev, err := HyperProfile(src, hyperFeed("pw"), nil)
	if err != nil {
		t.Fatal(err)
	}
	feed := hyperFeed("pw")
	feed.Countries = feed.Countries[:1]
	feed.Failed = []string{"SG"}
	feed.Catalog.Lists[1].Parsed = nil // streaming could not be downloaded
	p, err := HyperProfile(src, feed, &prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Nodes) != 2 || p.Nodes[1].ID != prev.Nodes[1].ID || !slices.Equal(p.Countries["SG"], prev.Countries["SG"]) {
		t.Fatalf("%+v", p.Nodes)
	}
	var tags []string
	for _, s := range p.BinarySets {
		tags = append(tags, s.Tag)
	}
	if !slices.Contains(tags, "list-streaming-domain-proxy") || !slices.Contains(tags, "list-mine-off-domain-proxy") {
		t.Fatal(tags)
	}
	// The catalog could not be read at all: every list of the previous profile stays.
	feed.Catalog = nil
	p, err = HyperProfile(src, feed, &prev)
	if err != nil || len(p.BinarySets) != len(prev.BinarySets) || len(p.Rules) != len(prev.Rules) {
		t.Fatalf("%v %d sets, %d rules", err, len(p.BinarySets), len(p.Rules))
	}
	// No node at all is not a profile.
	feed.Countries, feed.Failed = nil, []string{"JP", "SG"}
	if _, err := HyperProfile(src, feed, nil); err != errNoUsableNodes {
		t.Fatal(err)
	}
}

func TestHyperUsage(t *testing.T) {
	u := hyperUsage("Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09")
	if u == nil || u.Download != 1610612736 || u.Total == nil || *u.Total != 107374182400 || u.Expires != "2027-11-09T00:00:00Z" {
		t.Fatalf("%+v", u)
	}
	if hyperUsage("") != nil || hyperUsage("Traffic: lots / 1 GB") != nil {
		t.Fatal("usage from nothing")
	}
	if n, ok := sizeBytes("512 B"); !ok || n != 512 {
		t.Fatal(n, ok)
	}
}

func TestHyperSourceValidation(t *testing.T) {
	many := make([]string, maxDisabled+1)
	for i := range many {
		many[i] = fmt.Sprint("list", i)
	}
	for _, s := range []Source{
		{ID: "other", Kind: HyperSource, Name: "x"},
		func() Source { s := NewHyperSource(nil); s.Name = "bad\nname"; return s }(),
		NewHyperSource([]string{"bad\nkey"}),
		NewHyperSource(many),
	} {
		if s.validate() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}

// A link the router cannot read leaves only that node out: the update applies at once and
// keeps its rules, without asking for confirmation.
func TestHyperLinkNoticesKeepRouting(t *testing.T) {
	feed := hyperFeed("pw")
	feed.Countries[1].Body = append(feed.Countries[1].Body, []byte("vmess://not-base64\n")...)
	p, err := HyperProfile(NewHyperSource(nil), feed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notices) == 0 {
		t.Fatal("no notice for the links left out")
	}
	c := Assess(p, nil, Policy{})
	if c.Decision != "ready" || c.Profile.Local || len(c.Profile.Rules) == 0 || len(c.Profile.BinarySets) == 0 {
		t.Fatalf("%s local=%v rules=%d notices=%+v", c.Decision, c.Profile.Local, len(c.Profile.Rules), c.Notices)
	}
}
