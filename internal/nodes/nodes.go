// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package nodes keeps the account's nodes as native mihomo proxies.
//
// The Hyper service sends AnyTLS share links, read here as the Hyper app reads them, and
// stored as mihomo proxies plus the metadata the UI shows (label, country, city). There is
// no intermediate format: the proxy object is what mihomo reads, under the name cfg-<id>-out.
package nodes

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
)

// SchemaVersion of the store file.
const SchemaVersion = 1

// Node is one proxy with its UI metadata. Proxy has no "name"; the renderer sets it.
type Node struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Country string         `json:"country,omitempty"`
	City    string         `json:"city,omitempty"`
	Sort    int            `json:"sort,omitempty"` // country sort from the server, higher first
	Proxy   map[string]any `json:"proxy"`
}

// Type is the mihomo proxy type.
func (n Node) Type() string { s, _ := n.Proxy["type"].(string); return s }

// Tag is the node's mihomo proxy name.
func (n Node) Tag() string { return Tag(n.ID) }

// Tag is the mihomo proxy name of a node id.
func Tag(id string) string { return "cfg-" + id + "-out" }

// Store is the nodes file.
type Store struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
}

// Find returns the node with an id.
func (s *Store) Find(id string) (Node, bool) {
	for _, n := range s.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// Load reads the store; a missing file is an empty store.
func Load(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Version: SchemaVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err = json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Version != SchemaVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", path, s.Version)
	}
	for i := range s.Nodes {
		s.Nodes[i].Proxy = normalize(s.Nodes[i].Proxy).(map[string]any)
	}
	return &s, nil
}

// Save writes the store atomically, readable by root only (it holds credentials).
func (s *Store) Save(path string) error {
	s.Version = SchemaVersion
	b, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o600)
}

// Summary is the luci.hyper `nodes` entry.
type Summary struct {
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Type    string  `json:"type"`
	Country *string `json:"country"`
	City    *string `json:"city"`
}

func (n Node) Summary() Summary {
	return Summary{Name: n.ID, Label: n.Label, Type: n.Type(), Country: ptr(n.Country), City: ptr(n.City)}
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ParseOptions tag the nodes of one subscription response.
type ParseOptions struct {
	Country string
	Sort    int
	// InsecureDefault skips certificate checks for AnyTLS links that carry no insecure flag,
	// as the Hyper app does.
	InsecureDefault bool
}

// Parsed is one decrypted subscription body.
type Parsed struct {
	Nodes   []Node
	Remarks string
	Status  string
	Invalid int
	// Unsupported counts skipped links by scheme.
	Unsupported map[string]int
}

var linkScheme = lazyre.New(`^[A-Za-z][A-Za-z0-9+.-]{0,31}$`)

// Parse reads a decrypted subscription body as the Hyper app does (hyper-tunnel,
// packages/app-client/src/subscribe/client.ts): REMARKS= / STATUS= lines and AnyTLS share
// links, optionally base64. Links of any other scheme are skipped and counted.
func Parse(body []byte, opts ParseOptions) (*Parsed, error) {
	p := &Parsed{Unsupported: map[string]int{}}
	text := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	if !bytes.Contains(text, []byte("://")) {
		if dec, err := decodeBase64(text); err == nil {
			text = bytes.TrimSpace(dec)
		}
	}
	seen := map[string]bool{}
	for _, raw := range strings.Split(string(text), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
		case strings.HasPrefix(line, "REMARKS="):
			p.Remarks = strings.TrimSpace(line[len("REMARKS="):])
		case strings.HasPrefix(line, "STATUS="):
			p.Status = strings.TrimSpace(line[len("STATUS="):])
		case strings.HasPrefix(line, "anytls://"):
			n, ok := anyTLS(line, opts)
			if !ok {
				p.Invalid++
			} else if !seen[n.ID] {
				seen[n.ID] = true
				p.Nodes = append(p.Nodes, n)
			}
		default:
			if scheme, _, ok := strings.Cut(line, "://"); ok && linkScheme().MatchString(scheme) {
				p.Unsupported[strings.ToLower(scheme)]++
			} else {
				p.Invalid++
			}
		}
	}
	return p, nil
}

// anyTLS reads one anytls://password@server[:port][/][?query][#name] link.
func anyTLS(line string, opts ParseOptions) (Node, bool) {
	rest, fragment, _ := strings.Cut(line[len("anytls://"):], "#")
	authority := rest
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority = rest[:i]
	}
	if authority == "" || strings.ContainsFunc(authority, func(r rune) bool { return r <= ' ' || r == 0x7f || r == '\\' }) {
		return Node{}, false
	}
	u, err := url.Parse("https://" + rest)
	if err != nil || u.User == nil {
		return Node{}, false
	}
	password := u.User.Username()
	server := u.Hostname()
	port := 443
	if s := u.Port(); s != "" {
		if port, err = strconv.Atoi(s); err != nil {
			return Node{}, false
		}
	}
	if strings.TrimSpace(password) == "" || server == "" || port < 1 || port > 65535 ||
		strings.ContainsFunc(password+server, unicode.IsControl) {
		return Node{}, false
	}
	q := u.Query()
	sni := server
	for _, k := range []string{"sni", "serverName", "server_name", "peer", "host"} {
		if v, ok := query(q, k); ok && strings.TrimSpace(v) != "" {
			sni = strings.TrimSpace(v)
			break
		}
	}
	if strings.ContainsFunc(sni, unicode.IsControl) {
		return Node{}, false
	}
	proxy := map[string]any{"type": "anytls", "server": server, "port": int64(port), "password": password,
		"udp": flag(q, true, "udp"), "sni": sni,
		"idle-session-check-interval": int64(30), "idle-session-timeout": int64(30), "min-idle-session": int64(5)}
	if a, _ := query(q, "alpn"); a != "" {
		var alpn []any
		for _, v := range strings.Split(a, ",") {
			if v = strings.TrimSpace(v); v != "" && !strings.ContainsFunc(v, unicode.IsControl) {
				alpn = append(alpn, v)
			}
		}
		if len(alpn) > 0 {
			proxy["alpn"] = alpn
		}
	}
	if flag(q, opts.InsecureDefault, "skip-cert-verify", "insecure", "allowInsecure", "allow-insecure", "skip_cert_verify") {
		proxy["skip-cert-verify"] = true
	}
	label := cleanLabel(displayName(fragment))
	if label == "" {
		label = net.JoinHostPort(server, strconv.Itoa(port))
	}
	sum := md5.Sum([]byte(strings.Join([]string{opts.Country, "anytls", server, strconv.Itoa(port), label}, "|")))
	return Node{ID: "pp_" + hex.EncodeToString(sum[:])[:16], Label: label, Country: opts.Country, Sort: opts.Sort, Proxy: proxy}, true
}

// query reads a parameter by its name, or else by the name in any case.
func query(q url.Values, key string) (string, bool) {
	if v, ok := q[key]; ok && len(v) > 0 {
		return v[0], true
	}
	for k, v := range q {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0], true
		}
	}
	return "", false
}

// flag reads the first present parameter: present without a value, true or 1 mean yes.
func flag(q url.Values, def bool, keys ...string) bool {
	for _, k := range keys {
		if v, ok := query(q, k); ok {
			v = strings.TrimSpace(v)
			return v == "" || v == "1" || strings.EqualFold(v, "true")
		}
	}
	return def
}

var looseEscape = lazyre.New(`%([^0-9A-Fa-f]|[0-9A-Fa-f][^0-9A-Fa-f]|[0-9A-Fa-f]?$)`)

// displayName decodes a link's name. Templates put names in as they are, so a literal '%'
// keeps the node, as in the app.
func displayName(fragment string) string {
	escaped := looseEscape().ReplaceAllStringFunc(fragment, func(m string) string { return "%25" + m[1:] })
	if s, err := url.PathUnescape(escaped); err == nil {
		return s
	}
	return fragment
}

func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func decodeBase64(b []byte) ([]byte, error) {
	s := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' {
			return -1
		}
		return r
	}, string(b))
	s = strings.TrimRight(s, "=")
	if strings.ContainsAny(s, "-_") {
		return base64.RawURLEncoding.DecodeString(s)
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// normalize turns JSON numbers that are whole into int64 so YAML renders "443", not "443.0".
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x)
		}
		return x
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}
