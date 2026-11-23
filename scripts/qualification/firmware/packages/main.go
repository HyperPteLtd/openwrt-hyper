// Command packages resolves matching prerequisites with the official image's signed package indexes.
//
//	go run ./scripts/qualification/firmware/packages [--docker-context C] [--firewall fw3|fw4] VERSION DIR
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

const script = `set -eu
mkdir -p /packages /var/lock
if [ "$HYPER_TEST_FW" = fw3 ]; then
 set -- firewall iptables-zz-legacy ip6tables-zz-legacy iptables-mod-extra iptables-mod-tproxy ip6tables-mod-nat
else
 set -- kmod-nft-tproxy
fi
if command -v apk >/dev/null; then
 apk update
 apk fetch --recursive --output /packages ip-full kmod-tun "$@"
 i=0
 for url in $(cat /etc/apk/repositories.d/distfeeds.list);do
  case "$url" in */kmods/*|*/base/*|*/targets/*/packages/packages.adb)
   i=$((i+1));wget -q -O /packages/index-$i "$url";;
  esac
 done
else
 opkg update
 if [ "$HYPER_TEST_FW" = fw3 ]; then
  opkg remove --force-depends firewall4
 fi
 opkg --cache /packages install ip-full kmod-tun "$@"
fi
`

func main() {
	context := flag.String("docker-context", "default", "Docker context")
	firewall := flag.String("firewall", "fw4", "fw3 or fw4")
	flag.Parse()
	if flag.NArg() != 2 || !slices.Contains([]string{"23.05.6", "24.10.8", "25.12.6"}, flag.Arg(0)) ||
		(*firewall != "fw3" && *firewall != "fw4") {
		fmt.Fprintln(os.Stderr, "usage: packages [--docker-context C] [--firewall fw3|fw4] 23.05.6|24.10.8|25.12.6 DIR")
		os.Exit(2)
	}
	version, dir := flag.Arg(0), flag.Arg(1)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	docker := func(args ...string) *exec.Cmd {
		return exec.Command("docker", append([]string{"--context", *context}, args...)...)
	}
	out, err := docker("create", "-e", "HYPER_TEST_FW="+*firewall, "--platform", "linux/aarch64_generic",
		"--entrypoint", "/bin/sh", "openwrt/rootfs:aarch64_generic-"+version, "-c", script).Output()
	if err != nil {
		fail(err)
	}
	cid := strings.TrimSpace(string(out))
	defer docker("rm", "-f", cid).Run()
	start := docker("start", "-a", cid)
	start.Stdout, start.Stderr = os.Stdout, os.Stderr
	if err := start.Run(); err != nil {
		fail(err)
	}
	status, err := docker("inspect", "-f", "{{.State.ExitCode}}", cid).Output()
	if err != nil || strings.TrimSpace(string(status)) != "0" {
		fail(fmt.Errorf("firmware prerequisite resolution failed"))
	}
	cp := docker("cp", cid+":/packages/.", dir)
	cp.Stderr = os.Stderr
	if err := cp.Run(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "packages:", err)
	os.Exit(1)
}
