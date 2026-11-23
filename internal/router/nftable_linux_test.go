//go:build linux

package router

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// Run as root, or in a fresh network namespace: unshare -rn ./router.test -test.run NFTable
func TestNFTableGoneAnswersOnlyForACertainlyMissingTable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs CAP_NET_ADMIN")
	}
	if _, e := exec.LookPath("nft"); e != nil {
		t.Skip("needs nft")
	}
	name := "ow_probe_" + strconv.Itoa(os.Getpid())
	if !nftTableGone(name) {
		t.Fatal("a missing table counted as present")
	}
	if out, e := exec.Command("nft", "add", "table", "inet", name).CombinedOutput(); e != nil {
		t.Fatal(string(out))
	}
	defer exec.Command("nft", "delete", "table", "inet", name).Run()
	if nftTableGone(name) {
		t.Fatal("a present table counted as missing")
	}
	// Another family's table of that name is not the inet one.
	if out, e := exec.Command("nft", "add", "table", "ip", name+"x").CombinedOutput(); e != nil {
		t.Fatal(string(out))
	}
	defer exec.Command("nft", "delete", "table", "ip", name+"x").Run()
	if !nftTableGone(name + "x") {
		t.Fatal("an ip table counted as the inet one")
	}
}
