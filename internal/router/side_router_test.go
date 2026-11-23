package router

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

const routeHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

func TestSideRouterFollowsThePreferredIPv4DefaultRoute(t *testing.T) {
	lan := map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.1.2/24")}}
	for name, c := range map[string]struct {
		route string
		want  bool
	}{
		"own WAN": {"pppoe-wan\t00000000\t0100000A\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
			"br-lan\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n", false},
		"main router on the LAN": {"br-lan\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n", true},
		"lower metric wins": {"br-lan\t00000000\t0101A8C0\t0003\t0\t0\t20\t00000000\t0\t0\t0\n" +
			"eth1\t00000000\t0100000A\t0003\t0\t0\t10\t00000000\t0\t0\t0\n", false},
		"unreachable default is skipped": {"*\t00000000\t00000000\t0201\t0\t0\t0\t00000000\t0\t0\t0\n" +
			"br-lan\t00000000\t0101A8C0\t0003\t0\t0\t10\t00000000\t0\t0\t0\n", true},
		"no default": {"br-lan\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n", false},
	} {
		if got := sideRouter(lan, []byte(routeHeader+c.route)); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestBecomingASideRouterNeedsANewTakeover(t *testing.T) {
	i := initial()
	lan := map[string][]netip.Prefix{"br-lan": {netip.MustParsePrefix("192.168.1.2/24")}}
	n := Network{Method: "tproxy", LAN: lan}
	side := n
	side.SideRouter = true
	if networkKey(i, n) == networkKey(i, side) {
		t.Fatal("the masquerade would never be applied")
	}
	s := &System{}
	ctx := context.Background()
	s.observe(ctx, n, nil)
	if changed, takeover := s.observe(ctx, side, nil); !changed || !takeover {
		t.Fatal("a notice missed the new side router", changed, takeover)
	}
}

func TestStoppedFW4KeepsATakeoverAppliedOnFW4(t *testing.T) {
	state := t.TempDir()
	gone := false
	table := func(name string) bool {
		if name != "hyper" {
			t.Fatal(name)
		}
		return gone
	}
	write := func(name, body string) {
		if e := os.WriteFile(filepath.Join(state, name), []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if heldOnFW4(state, table) {
		t.Fatal("no takeover yet")
	}
	write("backend", "fw4\n")
	if heldOnFW4(state, table) {
		t.Fatal("a backend without ownership")
	}
	write("owned", "")
	if !heldOnFW4(state, table) {
		t.Fatal("the running takeover on fw4 was not kept")
	}
	gone = true
	if heldOnFW4(state, table) {
		t.Fatal("a wiped table still counted")
	}
	gone = false
	write("backend", "fw3\n")
	if heldOnFW4(state, table) {
		t.Fatal("fw3 counted as fw4")
	}
}
