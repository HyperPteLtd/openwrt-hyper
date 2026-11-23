package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeAliasLoop(t *testing.T) {
	for _, body := range []string{
		"proxies: &a\n  - *a\n",
		"payload: &a [*a]\n",
		"a: &a {b: [x, *a]}\n",
		"a: &a {<<: *a}\n",
		"a: &a\n  b: &b\n    c: *a\n",
		`{"extra": &a {"x": *a}}`,
	} {
		if value, err := Decode([]byte(body)); err == nil {
			t.Errorf("%q decodes to %v", body, value)
		}
	}
}

// A body whose aliases expand to millions of values fails rather than takes the memory.
func TestDecodeAliasBomb(t *testing.T) {
	bomb := "a: &a [x, x, x, x, x, x, x, x, x]\n"
	previous := "a"
	for _, name := range []string{"b", "c", "d", "e", "f"} {
		bomb += name + ": &" + name + " [" + strings.Repeat("*"+previous+", ", 8) + "*" + previous + "]\n"
		previous = name
	}
	bomb += "proxies: [*f]\n" // 9^6 values from 300 bytes
	if _, err := Decode([]byte(bomb)); !errors.Is(err, errAliases) {
		t.Errorf("a billion laughs: %v", err)
	}
	// Deep instead of wide: each alias one level more.
	deep := "a0: &a0 [x]\n"
	for n := 1; n <= 12_000; n++ {
		deep += fmt.Sprintf("a%d: &a%d [*a%d]\n", n, n, n-1)
	}
	if _, err := Decode([]byte(deep)); err == nil {
		t.Error("12000 nested aliases decode")
	}
}

// Anchors used the way configs use them stay within the limits.
func TestDecodeAliases(t *testing.T) {
	var body strings.Builder
	body.WriteString("all: &all [")
	for n := range 300 {
		fmt.Fprintf(&body, "p%d, ", n)
	}
	body.WriteString("DIRECT]\ngroup: &group {type: select, url: 'http://x', interval: 300}\nproxy-groups:\n")
	for n := range 200 {
		fmt.Fprintf(&body, "  - {name: g%d, <<: *group, proxies: *all}\n", n)
	}
	value, err := Decode([]byte(body.String()))
	if err != nil {
		t.Fatal(err)
	}
	groups := AsList(AsObject(value).Value("proxy-groups"))
	if len(groups) != 200 || len(AsList(AsObject(groups[199]).Value("proxies"))) != 301 {
		t.Fatalf("groups decode as %v", groups)
	}
	// Each alias is a copy of its own: changing one changes no other.
	first, second := AsObject(groups[0]), AsObject(groups[1])
	first.Set("type", "url-test")
	AsList(first.Value("proxies"))[0] = "changed"
	if second.String("type") != "select" || AsList(second.Value("proxies"))[0] != "p0" {
		t.Errorf("aliases share values: %v", second)
	}
}

func TestDecodeMerge(t *testing.T) {
	body := `base: &base {type: ss, cipher: aes-128-gcm, password: x, port: 8388}
server: &server {server: 1.1.1.1, port: 443}
proxies:
  - {name: list, <<: [*base, *server]}
  - {name: one, <<: *base, port: 1}
  - {port: 2, <<: *server, name: before}
  - name: inline
    <<: {type: http}
  - {name: quoted, "<<": *base}
  - {name: scalar, <<: 1}
`
	value, err := Decode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range AsList(AsObject(value).Value("proxies")) {
		encoded, _ := json.Marshal(item)
		got = append(got, string(encoded))
	}
	want := []string{
		// The first mapping of a list wins.
		`{"name":"list","type":"ss","cipher":"aes-128-gcm","password":"x","port":8388,"server":"1.1.1.1"}`,
		// Keys the mapping sets win, before or after the merge.
		`{"name":"one","type":"ss","cipher":"aes-128-gcm","password":"x","port":1}`,
		`{"port":2,"server":"1.1.1.1","name":"before"}`,
		`{"name":"inline","type":"http"}`,
		// A quoted "<<" is a key like any other.
		`{"name":"quoted","\u003c\u003c":{"type":"ss","cipher":"aes-128-gcm","password":"x","port":8388}}`,
		`{"name":"scalar"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("merged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Mappings of many keys decode in linear time, a later key replacing an earlier in its place.
func TestDecodeManyKeys(t *testing.T) {
	const keys = 200_000
	var body strings.Builder
	for n := range keys {
		fmt.Fprintf(&body, "k%d: %d\n", n, n)
	}
	body.WriteString("k0: last\n")
	value, err := Decode([]byte(body.String()))
	if err != nil {
		t.Fatal(err)
	}
	o := AsObject(value)
	if len(o) != keys || o[0] != (Field{"k0", "last"}) || o[keys-1] != (Field{fmt.Sprintf("k%d", keys-1), keys - 1}) {
		t.Errorf("%d keys, first %v, last %v", len(o), o[0], o[len(o)-1])
	}
}

// Past a few keys, fields finds them by a map rather than by scanning them all.
func TestFieldsIndex(t *testing.T) {
	var f fields
	for n := range 100 {
		f.set(fmt.Sprintf("k%d", n), n)
		f.add("k0", "merged")
	}
	if f.index == nil || len(f.index) != len(f.o) {
		t.Fatalf("index %v for %d fields", f.index, len(f.o))
	}
	f.set("k50", "set")
	if at := f.find("k50"); at != 50 || f.o[at].Value != "set" || f.o[0].Value != 0 {
		t.Errorf("k50 at %d: %v; k0 %v", at, f.o[at], f.o[0])
	}
}
