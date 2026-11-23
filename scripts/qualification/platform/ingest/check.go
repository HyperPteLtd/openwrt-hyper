package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"go.yaml.in/yaml/v3"
)

// Stage a private core home containing only this snapshot's exact providers.
// Raw core output can echo secrets and therefore stays in the private directory.
func verifyCore(dir, source string, p subscription.Profile) bool {
	bin := os.Getenv("HYPER_DIAGNOSTIC_CORE")
	if bin == "" {
		return false
	}
	coreDir, err := os.MkdirTemp(dir, "mihomo-")
	must(err)
	caps := kernel.TargetCapabilities{Control: kernel.Controller, Naive: os.Getenv("HYPER_TEST_NAIVE") == "1", TProxy: true, TUN: true}
	b, err := router.Compile(router.Intent{Source: source, Mode: "smart"}, p, caps, router.Network{Method: "tproxy", DNS: true, Resolver: "192.0.2.53"}, strings.Repeat("synthetic-test-", 3))
	must(err)
	must(subscription.MaterializeSets(p, filepath.Join(coreDir, "rulesets")))
	builtin, err := filepath.Abs("files/usr/share/hyper/rules")
	must(err)
	must(os.Symlink(builtin, filepath.Join(coreDir, "builtin")))
	var cfg struct{ Rules []string }
	must(yaml.Unmarshal(b, &cfg))
	ctx, end := context.WithTimeout(context.Background(), 2*time.Minute)
	defer end()
	must(subscription.PrepareGeodata(ctx, append(cfg.Rules, subscription.GeoRules(p)...), coreDir, filepath.Join(builtin, "GEODATA")))
	path := filepath.Join(coreDir, "profile.yaml")
	must(os.WriteFile(path, b, 0600))
	out, err := exec.CommandContext(ctx, bin, "-t", "-d", coreDir, "-f", path).CombinedOutput()
	must(os.WriteFile(filepath.Join(coreDir, "check.private.log"), out, 0600))
	if err != nil {
		panic(errors.New("Mihomo rejected the compiled configuration; inspect the private fixture log"))
	}
	return true
}
