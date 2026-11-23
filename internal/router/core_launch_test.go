package router

import (
	"slices"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
)

// The manager starts the core itself, with no launcher or jail in between, in TUN mode too.
func TestCoreStartsWithoutALauncher(t *testing.T) {
	launch := kernel.Launch{Path: "/etc/hyper/mihomo-kernel/slots/a/mihomo", Config: "/etc/hyper/mihomo-kernel/configs/c.yaml"}
	work := RunDir + "/core"
	cmd := coreCommand(launch, work, 65534)
	if cmd.Path != launch.Path || !slices.Equal(cmd.Args, []string{launch.Path, "-d", coreHome, "-f", launch.Config}) {
		t.Fatal(cmd.Path, cmd.Args)
	}
	if cmd.Dir != work || !slices.Equal(cmd.Env, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=" + work, "HOME=" + work}) {
		t.Fatal(cmd.Dir, cmd.Env)
	}
}
