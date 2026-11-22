// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package nodes

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../tests/fixtures/links.txt")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseLinks(t *testing.T) {
	p, err := Parse(fixture(t), ParseOptions{Country: "JP", Sort: 10, InsecureDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Remarks != "Hyper" || p.Status == "" {
		t.Fatalf("remarks %q status %q", p.Remarks, p.Status)
	}
	// Six AnyTLS nodes (one listed twice); like the app, every other scheme is skipped.
	if len(p.Nodes) != 6 || p.Invalid != 4 || !reflect.DeepEqual(p.Unsupported, map[string]int{"trojan": 1, "vless": 1}) {
		t.Fatalf("%d nodes, invalid %d, unsupported %v", len(p.Nodes), p.Invalid, p.Unsupported)
	}
	byLabel := map[string]Node{}
	for _, n := range p.Nodes {
		if n.Type() != "anytls" || n.Country != "JP" || n.Sort != 10 || len(n.ID) != 19 || n.Proxy["name"] != nil {
			t.Fatal(n)
		}
		if n.Proxy["idle-session-check-interval"] != int64(30) || n.Proxy["idle-session-timeout"] != int64(30) || n.Proxy["min-idle-session"] != int64(5) {
			t.Fatal(n.Proxy)
		}
		byLabel[n.Label] = n
	}
	want := map[string]map[string]any{
		"AnyTLS": {"server": "jp9.example.com", "port": int64(8443), "password": "pw", "sni": "cdn.example.com",
			"alpn": []any{"h2", "http/1.1"}, "udp": true, "skip-cert-verify": true},
		// Credentials decode as in the app: '+' stays a plus.
		"日本 · 东京 01": {"server": "jp1.example.com", "port": int64(443), "password": "p@ss+w", "sni": "jp1.example.com"},
		// No port means 443; peer stands in for sni.
		"日本 · 东京 02": {"server": "jp2.example.com", "port": int64(443), "sni": "edge.example.com"},
		// A literal % keeps the node; insecure=0 and udp=0 are honoured.
		"Strict 100%": {"sni": "jp3.example.com", "udp": false, "skip-cert-verify": nil},
		// No name: the address names it.
		"[2001:db8::1]:443": {"server": "2001:db8::1", "sni": "v6.example.com", "skip-cert-verify": true},
		// Parameter names in any case; a bare flag means yes.
		"Upper case": {"sni": "upper.example.com", "skip-cert-verify": true},
	}
	for label, fields := range want {
		n, ok := byLabel[label]
		if !ok {
			t.Fatalf("missing %q in %v", label, byLabel)
		}
		for k, v := range fields {
			if !reflect.DeepEqual(n.Proxy[k], v) {
				t.Errorf("%s: %s = %#v, want %#v", label, k, n.Proxy[k], v)
			}
		}
	}
	if s := byLabel["AnyTLS"].Summary(); s.Type != "anytls" || *s.Country != "JP" || s.City != nil {
		t.Fatal(s)
	}
	// Stable ids: the same link gives the same id, so main_node survives a sync.
	again, _ := Parse(fixture(t), ParseOptions{Country: "JP", InsecureDefault: true})
	if again.Nodes[3].ID != p.Nodes[3].ID {
		t.Fatal("unstable id")
	}
	other, _ := Parse(fixture(t), ParseOptions{Country: "SG"})
	if other.Nodes[3].ID == p.Nodes[3].ID {
		t.Fatal("country not in id")
	}
}

// Without a flag in the link, the Hyper app's default applies.
func TestParseInsecureDefault(t *testing.T) {
	for _, def := range []bool{true, false} {
		p, _ := Parse([]byte("anytls://pw@a.example.com:443/?sni=a.example.com#A"), ParseOptions{InsecureDefault: def})
		if len(p.Nodes) != 1 || (p.Nodes[0].Proxy["skip-cert-verify"] == true) != def {
			t.Fatal(def, p.Nodes)
		}
	}
}

func TestParseBase64(t *testing.T) {
	link := "anytls://pw@b.example.com:443?sni=b.example.com#B"
	p, err := Parse([]byte(base64.StdEncoding.EncodeToString([]byte("REMARKS=x\n"+link+"\n"+link))), ParseOptions{})
	if err != nil || len(p.Nodes) != 1 || p.Remarks != "x" {
		t.Fatal(err, p)
	}
	for _, bad := range []string{"anytls://", "anytls://pw@:443#A", "anytls://pw@a.example.com:x#A", "anytls://pw@a\\b:443#A", "anytls://p%0aw@a.example.com:443#A"} {
		if p, _ := Parse([]byte(bad), ParseOptions{}); len(p.Nodes) != 0 || p.Invalid != 1 {
			t.Errorf("%q: %v", bad, p.Nodes)
		}
	}
	p, _ = Parse([]byte("anytls://pw@a.example.com:443#A\x01B"), ParseOptions{})
	if len(p.Nodes) != 1 || p.Nodes[0].Label != "A B" {
		t.Fatal(p.Nodes)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	s, err := Load(path)
	if err != nil || len(s.Nodes) != 0 {
		t.Fatal(err, s)
	}
	p, _ := Parse(fixture(t), ParseOptions{Country: "JP"})
	s.Nodes = p.Nodes
	if err = s.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Nodes, s.Nodes) {
		a, _ := json.Marshal(back.Nodes[0])
		b, _ := json.Marshal(s.Nodes[0])
		t.Fatalf("round trip\n%s\n%s", a, b)
	}
	if n, ok := back.Find(p.Nodes[1].ID); !ok || n.Tag() != "cfg-"+n.ID+"-out" {
		t.Fatal(n)
	}
	os.WriteFile(path, []byte(`{"version":9,"nodes":[]}`), 0o600)
	if _, err = Load(path); err == nil {
		t.Fatal("accepted unknown version")
	}
}
