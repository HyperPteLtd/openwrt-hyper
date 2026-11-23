package subscription

import (
	"net/netip"
	"os"
	"testing"
)

func TestMRSRangesBuiltinCN(t *testing.T) {
	data, err := os.ReadFile("../../files/usr/share/hyper/rules/geoip-cn.mrs")
	if err != nil {
		t.Fatal(err)
	}
	ranges, err := MRSRanges(data)
	if err != nil {
		t.Fatal(err)
	}
	v4, v6 := 0, 0
	inside := func(a netip.Addr) bool {
		for _, r := range ranges {
			if r[0].BitLen() == a.BitLen() && !a.Less(r[0]) && !r[1].Less(a) {
				return true
			}
		}
		return false
	}
	for _, r := range ranges {
		if r[0].Is4() {
			v4++
		} else {
			v6++
		}
	}
	if v4 < 1000 || v6 < 100 || !inside(netip.MustParseAddr("114.114.114.114")) || inside(netip.MustParseAddr("8.8.8.8")) {
		t.Fatalf("unexpected ranges: %d v4, %d v6", v4, v6)
	}
}
