// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package rulekey

import (
	"regexp"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, k := range []string{"cn-direct-domain", "中国常用域名", "中国互联网公司 IP", "微信常用域名和 IP", "GEOIP CN", strings.Repeat("中", 64)} {
		if !Valid(k) {
			t.Errorf("%q refused", k)
		}
	}
	for _, k := range []string{"", " lead", "trail ", "a\nb", "it's", `a"b`, `a\b`, "\xff", strings.Repeat("中", 65)} {
		if Valid(k) {
			t.Errorf("%q accepted", k)
		}
	}
}

// Plain keys keep their names; others get a stable ASCII ID that differs per key.
func TestID(t *testing.T) {
	if ID("cn-direct-domain") != "cn-direct-domain" {
		t.Fatal(ID("cn-direct-domain"))
	}
	safe := regexp.MustCompile(`^k-[0-9a-f]{16}$`)
	a, b := ID("中国常用域名"), ID("GEOIP CN")
	if !safe.MatchString(a) || !safe.MatchString(b) || a == b || ID("中国常用域名") != a {
		t.Fatal(a, b)
	}
}
