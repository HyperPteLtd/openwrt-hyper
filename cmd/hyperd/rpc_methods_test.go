package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
)

// rpcd lists, forwards and authorises the same methods. Other proxy plugins are stopped on start
// without asking, so nothing confirms a conflict any more.
func TestRPCMethodsMatchACL(t *testing.T) {
	script, err := os.ReadFile("../../files/usr/libexec/rpcd/hyper")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(script), "<<'JSON'\n")
	list, _, _ := strings.Cut(rest, "\nJSON\n")
	var signatures map[string]json.RawMessage
	if err := json.Unmarshal([]byte(list), &signatures); err != nil {
		t.Fatal(err)
	}
	var listed []string
	for method := range signatures {
		listed = append(listed, method)
	}
	forward := regexp.MustCompile(`case "\$2" in ([a-z_|]+)\) exec /usr/sbin/hyperd call`).FindStringSubmatch(string(script))
	if forward == nil {
		t.Fatal("rpcd call dispatch not found")
	}
	forwarded := strings.Split(forward[1], "|")

	b, err := os.ReadFile("../../files/usr/share/rpcd/acl.d/hyper.json")
	if err != nil {
		t.Fatal(err)
	}
	var acl map[string]struct {
		Read, Write struct {
			Ubus map[string][]string `json:"ubus"`
		}
	}
	if err := json.Unmarshal(b, &acl); err != nil {
		t.Fatal(err)
	}
	granted := slices.Concat(acl["hyper"].Read.Ubus["hyper"], acl["hyper"].Write.Ubus["hyper"])

	for _, set := range [][]string{listed, forwarded, granted} {
		slices.Sort(set)
	}
	if !slices.Equal(listed, forwarded) || !slices.Equal(listed, granted) {
		t.Fatalf("listed %v\nforwarded %v\ngranted %v", listed, forwarded, granted)
	}
	if !slices.Contains(listed, "conflicts") || slices.Contains(listed, "confirm_conflicts") {
		t.Fatal(listed)
	}
	s := &app{m: router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming), sys: router.NewSystem()}
	if _, err := s.handle(context.Background(), "confirm_conflicts", request{Token: "token"}); err == nil || err.Error() != "unknown operation" {
		t.Fatal(err)
	}
}
