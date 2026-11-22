// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package uci

import (
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	c, err := Parse(strings.NewReader(`
config hyper 'config'
	option enabled '1'   # trailing comment
	option dns_server "https://1.1.1.1/dns-query"
	option label 'it'\''s'

config hyper control
	list lan_direct_mac_addrs '00:11:22:33:44:55'
	list lan_direct_mac_addrs 'aa:bb:cc:dd:ee:ff'

config rule
	option x bare
config rule
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Get("config", "enabled") != "1" || c.Get("config", "dns_server") != "https://1.1.1.1/dns-query" {
		t.Fatal(c.Section("config"))
	}
	if got := c.Get("config", "label"); got != "it's" {
		t.Fatalf("label %q", got)
	}
	if got := c.Section("control").List("lan_direct_mac_addrs"); !slices.Equal(got, []string{"00:11:22:33:44:55", "aa:bb:cc:dd:ee:ff"}) {
		t.Fatal(got)
	}
	if rules := c.OfType("rule"); len(rules) != 2 || rules[1].Name != "@rule[1]" || rules[0].Get("x") != "bare" {
		t.Fatal(rules)
	}
	if c.Get("missing", "x") != "" || c.Section("config").List("enabled")[0] != "1" {
		t.Fatal("fallbacks")
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"option x 'y'", "config a\n\toption x 'y", "bogus a b", "config a\n\toption x"} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	c, _ := Parse(strings.NewReader("config hyper 'config'\n\toption a 'x'\nconfig rule\n\toption n '1'\n"))
	c.Section("config").Set("label", "it's")
	c.Ensure("control", "hyper").SetList("macs", []string{"a", "b"})
	c.Section("config").SetList("gone", nil)
	path := t.TempDir() + "/hyper"
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Get("config", "label") != "it's" || !slices.Equal(back.Section("control").List("macs"), []string{"a", "b"}) || back.OfType("rule")[0].Get("n") != "1" {
		t.Fatalf("%s", c.Bytes())
	}
	if string(back.Bytes()) != string(c.Bytes()) {
		t.Fatalf("unstable\n%s\n%s", back.Bytes(), c.Bytes())
	}
}
