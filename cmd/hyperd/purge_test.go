package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
)

func TestPurgeDeletesWhatHyperKeeps(t *testing.T) {
	paths := purgePaths()
	for _, want := range []string{
		"/etc/hyper/kernel", "/etc/hyper/mihomo-kernel", "/tmp/hyper/kernel", "/tmp/hyper/mihomo-kernel",
		"/etc/hyper/sources", "/etc/hyper/private", "/etc/hyper/rulesets", "/etc/hyper/cnlists",
		// Geodata, up to 64 MiB a file.
		"/etc/hyper/geodata",
	} {
		if !slices.Contains(paths, want) {
			t.Error("purge keeps", want)
		}
	}
	// Each path is removed recursively: none may reach beyond Hyper's own directories.
	for _, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !strings.HasPrefix(p, router.Root+"/") && !strings.HasPrefix(p, "/tmp/hyper") {
			t.Error("purge reaches", p)
		}
	}
}
