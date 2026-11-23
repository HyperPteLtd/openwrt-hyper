package router

import (
	"slices"
	"testing"
)

func TestFirewallCapabilityReport(t *testing.T) {
	env := parseFirewallCapabilities("fw3 iptables-legacy v1.8.8\nipv6 1\nmissing xt_tproxy\ntproxy 0\ntun 1\n")
	if env.Firewall4 || env.FirewallBackend != "fw3" || env.FirewallTProxy || !env.FirewallTUN || !slices.Equal(env.FirewallPackages, []string{"iptables-mod-tproxy"}) {
		t.Fatal(env)
	}
	// An unavailable optional mode does not add recommendations to the selected mode.
	env = parseFirewallCapabilities("fw4 nftables v1.1.1\nipv6 1\ntproxy 1\ntun 0")
	if !env.Firewall4 || len(env.FirewallPackages) > 0 || !env.FirewallTProxy || env.FirewallTUN {
		t.Fatal(env)
	}
	env = parseFirewallCapabilities("ambiguous\nmissing firewall_mixed\n")
	if env.FirewallBackend != "ambiguous" || env.FirewallTProxy || env.FirewallTUN || len(env.FirewallPackages) > 0 || len(env.NetworkRequirements) != 1 {
		t.Fatal(env)
	}
	env = parseFirewallCapabilities("fw3 iptables-legacy v1.8.8\nipv6 1\ndns_intercept 1\nmissing xt_redirect6\nmissing xt_redirect6\nmissing example.invalid/private-token\ntproxy 0\ntun 0")
	if !slices.Equal(env.FirewallPackages, []string{"ip6tables-mod-nat"}) || len(env.NetworkRequirements) != 1 {
		t.Fatal(env)
	}
}

func TestIPNeighborsWithoutJSON(t *testing.T) {
	rows := parseNeighborText("192.0.2.1 dev lan lladdr 12:34:56:78:9a:bc REACHABLE\nfd00::1 dev lan lladdr 12:34:56:78:9a:bc router STALE\n192.0.2.2 dev lan FAILED\n")
	if len(rows) != 2 || rows[0].MAC != "12:34:56:78:9a:bc" || rows[0].Interface != "lan" || !slices.Contains(rows[0].State, "REACHABLE") || rows[1].IP != "fd00::1" || !slices.Contains(rows[1].State, "STALE") {
		t.Fatal(rows)
	}
}

func TestDNSIssueRetainsOnlyInstanceAndCategory(t *testing.T) {
	for _, test := range []struct{ line, want string }{
		{"dns_default_upstream lan", "dns_default_upstream lan"},
		{"dns_no_instance", "dns_no_instance"},
		{"dns_default_upstream 10.0.0.53:53", ""},
		{"dns_servers_file lan /etc/private", ""},
		{"dns_unknown cfgabc", ""},
	} {
		env := parseFirewallCapabilities("fw4 nftables v1\nmissing " + test.line + "\n")
		if env.DNSIssue != test.want || len(env.FirewallPackages) != 0 {
			t.Fatalf("diagnosis %q parsed as %+v", test.line, env)
		}
	}
}
