// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package rulekey names the server's rule lists on the router. The server keys its lists
// by readable names, Chinese ones too ("中国常用域名"); the UI and the disabled-list
// setting keep that name, while files and core rule providers take an ASCII ID.
package rulekey

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
)

var plain = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Valid reports whether key can name a list: 1 to 64 characters of UTF-8 without
// surrounding spaces, control characters, quotes or backslashes.
func Valid(key string) bool {
	return key != "" && utf8.ValidString(key) && utf8.RuneCountInString(key) <= 64 && key == strings.TrimSpace(key) &&
		!strings.ContainsFunc(key, func(r rune) bool { return unicode.IsControl(r) || strings.ContainsRune(`'"\`, r) })
}

// ID is the name the list's files and core rule providers take: a plain ASCII key as it
// is (so their names stay what they were), any other "k-" and 16 hex digits of its SHA-256.
func ID(key string) string {
	if plain().MatchString(key) {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "k-" + hex.EncodeToString(sum[:8])
}
