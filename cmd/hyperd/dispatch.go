package main

import (
	"os"
	"path/filepath"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cli/kernelcli"
	"github.com/HyperPteLtd/openwrt-hyper/internal/cli/netcli"
	"github.com/HyperPteLtd/openwrt-hyper/internal/cli/subscriptioncli"
)

// The universal installer retains one executable for the router's CPU.
// The installed command names keep their original argument and exit conventions.
func main() {
	switch filepath.Base(os.Args[0]) {
	case "hyper-kernel":
		kernelcli.Main()
	case "hyper-net":
		netcli.Main()
	case "hyper-subscription":
		subscriptioncli.Main()
	default:
		runDaemon()
	}
}
