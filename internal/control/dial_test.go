package control

import (
	"slices"
	"testing"
)

func TestFailedDialReadsTheCoreReport(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Connection
	}{
		{`time="2026-11-21T06:00:00.000000000Z" level=warning msg="[TCP] dial @global (match Match/) 192.168.1.23:51234 --> www.example.com:443 error: dial tcp 203.0.113.9:8443: i/o timeout"`,
			Connection{Source: "192.168.1.23", Destination: "www.example.com:443", Network: "tcp", Outbound: "@global", Rule: "Match,"}},
		{`WARN[0000] [UDP] dial DIRECT (match RuleSet/geosite-cn) [fd00::23]:5353 --> [2400:3200::1]:443 error: network is unreachable`,
			Connection{Source: "fd00::23", Destination: "[2400:3200::1]:443", Network: "udp", Outbound: "@direct", Rule: "RuleSet,geosite-cn"}},
		{`level=warning msg="[TCP] dial group:streaming (match AND/((IN-TYPE,TPROXY),(SRC-IP-CIDR,192.168.1.0/24))) 192.168.1.5:40000(curl, uid=0) --> 198.51.100.7:80 error: connect: connection refused"`,
			Connection{Source: "192.168.1.5", Destination: "198.51.100.7:80", Network: "tcp", Outbound: "group:streaming", Rule: "AND,((IN-TYPE,TPROXY),(SRC-IP-CIDR,192.168.1.0/24))"}},
		{`level=warning msg="[TCP] dial node:abc mihomo --> dns.example:443 error: EOF"`,
			Connection{Source: "mihomo", Destination: "dns.example:443", Network: "tcp", Outbound: "node:abc"}},
	} {
		got, ok := FailedDial(tc.line, 7)
		tc.want.Created, tc.want.Closed, tc.want.Result, tc.want.Chain = 7, 7, "failed", []string{tc.want.Outbound}
		if !ok || got.Source != tc.want.Source || got.Destination != tc.want.Destination || got.Network != tc.want.Network ||
			got.Outbound != tc.want.Outbound || got.Rule != tc.want.Rule || got.Result != "failed" || got.Created != 7 || got.Closed != 7 ||
			!slices.Equal(got.Chain, tc.want.Chain) {
			t.Fatalf("%s\n got %+v\nwant %+v", tc.line, got, tc.want)
		}
	}
	if _, ok := FailedDial(`level=warning msg="[Provider] get subscription-userinfo: EOF"`, 1); ok {
		t.Fatal("another warning read as a failed dial")
	}
}
