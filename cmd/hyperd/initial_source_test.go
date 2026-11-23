package main

import (
	"encoding/json"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"path/filepath"
	"testing"
)

func TestInitialSourceSelectedWithoutStartingOrOverwriting(t *testing.T) {
	store := subscription.OpenStore(t.TempDir())
	disk := router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}
	m := router.New(disk, nil, router.DefaultTiming)
	for _, id := range []string{"first", "second"} {
		if err := store.Configure(subscription.Source{ID: id, Name: id, Kind: "file"}); err != nil {
			t.Fatal(err)
		}
		if selected, err := selectInitialSource(m, store, id); err != nil || selected {
			t.Fatal("unrefreshed or second source selected", err)
		}
		_, err := (subscription.Fetcher{}).Refresh(t.Context(), store, id, []byte("http://sample.example:8080#test"), subscription.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		selected, err := selectInitialSource(m, store, id)
		if err != nil || selected != (id == "first") {
			t.Fatal(id, selected, err)
		}
	}
	saved, err := disk.Load()
	if err != nil || saved.Source != "first" || saved.Enabled {
		t.Fatal(saved, err)
	}
	legacy := router.New(router.Disk{Path: filepath.Join(t.TempDir(), "old.json")}, nil, router.DefaultTiming)
	if picked, err := selectInitialSource(legacy, store, ""); !picked || err != nil || legacy.Status().Intent.Enabled {
		t.Fatal(picked, err)
	}
}

func TestManagerReviewsNativeSnapshotBeforeFirstImport(t *testing.T) {
	manager := &app{m: router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming)}
	profile, err := subscription.Parse("first", []byte("http://sample.example:8080#test"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.reviewSource(t.Context(), request{Source: "first", Args: body})
	if err != nil || token == "" {
		t.Fatalf("first native subscription review failed: %v", err)
	}
	profile.SchemaVersion--
	body, _ = json.Marshal(profile)
	if _, err = manager.reviewSource(t.Context(), request{Source: "first", Args: body}); err == nil {
		t.Fatal("obsolete snapshot accepted")
	}
}
