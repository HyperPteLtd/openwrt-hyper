package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

func saveStartupProfile(t testing.TB, store *subscription.Store, p subscription.Profile) {
	t.Helper()
	ticket, err := store.Begin(p.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ticket, subscription.Candidate{Profile: p, Decision: "ready"}); err != nil {
		t.Fatal(err)
	}
}

func compileStartupProfile(i Intent, p subscription.Profile) ([]byte, error) {
	return Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true},
		Network{Method: "tproxy", Resolver: "192.0.2.53"}, "synthetic-startup-secret")
}

func TestStartupRechecksReferencedSources(t *testing.T) {
	store := subscription.OpenStore(t.TempDir())
	p, other := baseProfile(t, "primary"), baseProfile(t, "other")
	saveStartupProfile(t, store, p)
	saveStartupProfile(t, store, other)
	i := initial()
	i.Source = p.Source
	i.Policy.Groups = []LocalGroup{{ID: "local:group", Members: []NodeRef{{Source: other.Source, ID: other.Nodes[0].ID}}}}
	snapshot := store.SnapshotReader()
	loaded, err := LoadProfile(i, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := compileStartupProfile(i, loaded)
	if err != nil {
		t.Fatal(err)
	}
	compiles := 0
	check := func() error {
		return checkStartupSources(i, store, snapshot, cfg, func(p subscription.Profile) ([]byte, error) {
			compiles++
			return compileStartupProfile(i, p)
		})
	}
	if err := check(); err != nil || compiles != 0 {
		t.Fatal("unchanged sources rebuilt", err)
	}
	if _, err := store.Begin(other.Source); err != nil {
		t.Fatal(err)
	}
	// A refresh's bookkeeping goes to the source's status file, not the profiles.
	if err := check(); err != nil || compiles != 0 {
		t.Fatal("refresh bookkeeping rebuilt", err)
	}
	saveStartupProfile(t, store, other)
	if err := check(); err != nil || compiles != 1 {
		t.Fatal("equivalent source update did not receive an exact check", err)
	}
	other.Nodes[0].Outbound["server_port"] = 4321
	saveStartupProfile(t, store, other)
	if err := check(); err == nil || compiles != 2 {
		t.Fatal("changed foreign outbound accepted")
	}
	if err := store.Revoke(other.Source); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || compiles != 2 {
		t.Fatal("revoked foreign source accepted")
	}
}

// Measures initial load/compile and both authorization checks, using warm files.
func BenchmarkStartupSources1000(b *testing.B) {
	store := subscription.OpenStore(b.TempDir())
	i := initial()
	i.Source = "benchmark"
	var uris strings.Builder
	for n := range 1000 {
		fmt.Fprintf(&uris, "http://192.0.2.1:%d#node-%d\n", 10000+n, n)
	}
	p, err := subscription.Parse(i.Source, []byte(uris.String()))
	if err != nil || len(p.Nodes) != 1000 {
		b.Fatal("fixture must contain 1000 nodes", err)
	}
	p.Local = true
	saveStartupProfile(b, store, p)
	compile := func(p subscription.Profile) ([]byte, error) { return compileStartupProfile(i, p) }
	b.Run("full-rebuild", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var original []byte
			for check := range 3 {
				p, err := LoadProfile(i, store)
				if err != nil {
					b.Fatal(err)
				}
				cfg, err := compile(p)
				if err != nil {
					b.Fatal(err)
				}
				if check == 0 {
					original = cfg
				} else if !bytes.Equal(original, cfg) {
					b.Fatal("configuration changed")
				}
			}
		}
	})
	b.Run("verified-snapshots", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			snapshot := store.SnapshotReader()
			p, err := LoadProfile(i, snapshot)
			if err != nil {
				b.Fatal(err)
			}
			cfg, err := compile(p)
			if err != nil {
				b.Fatal(err)
			}
			for range 2 {
				if err := checkStartupSources(i, store, snapshot, cfg, compile); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
