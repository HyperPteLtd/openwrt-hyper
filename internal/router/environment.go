package router

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Environment struct {
	Firewall4           bool     `json:"firewall4"`
	FirewallBackend     string   `json:"firewall_backend"`
	FirewallEngine      string   `json:"firewall_engine,omitempty"`
	FirewallVersion     string   `json:"firewall_version,omitempty"`
	FirewallTProxy      bool     `json:"firewall_tproxy"`
	FirewallTUN         bool     `json:"firewall_tun"`
	NetworkRequirements []string `json:"network_requirements,omitempty"`
	DNSIssue            string   `json:"dns_issue,omitempty"`
	FirewallPackages    []string `json:"firewall_packages,omitempty"`
	FreeBytes           *uint64  `json:"free_bytes,omitempty"`
}

func EnvironmentStatus() Environment {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, networkHelper, "capabilities").Output()
	env := Environment{FirewallBackend: "unavailable"}
	if err == nil {
		env = parseFirewallCapabilities(string(output))
	}

	var fs unix.Statfs_t
	if err := unix.Statfs("/etc", &fs); err == nil {
		n := uint64(fs.Bavail) * uint64(fs.Bsize)
		env.FreeBytes = &n
	}
	return env
}

// AutoMethod picks the capture method: TProxy where the firewall supports it, otherwise TUN.
func AutoMethod() string {
	env := EnvironmentStatus()
	if !env.FirewallTProxy && env.FirewallTUN {
		return "tun"
	}
	return "tproxy"
}

// StoredMethod returns the method EnsureMethod recorded at startup, probing only when it is missing.
func StoredMethod(ctx context.Context) string {
	if m := uci(ctx, "method"); m == "tproxy" || m == "tun" {
		return m
	}
	return AutoMethod()
}

// EnsureMethod stores the automatic capture method so the network helper uses it too.
func EnsureMethod(ctx context.Context) {
	if m := AutoMethod(); uci(ctx, "method") != m {
		_ = exec.CommandContext(ctx, "uci", "-q", "set", "hyper.main.method="+m).Run()
		_ = exec.CommandContext(ctx, "uci", "-q", "commit", "hyper").Run()
	}
}

// The helper reports identifiers exclusively. Diagnostics never relay command
// output, UCI values, rules, or filenames into logs or the LuCI API.
var networkRequirements = map[string]struct{ description, provider string }{
	"method":            {"unsupported capture method", ""},
	"firewall_mixed":    {"both fw3 and fw4 are active", ""},
	"firewall_inactive": {"no active fw3 or fw4 firewall", ""},
	"iproute":           {"numeric policy routing and marked route lookup", "ip-tiny / ip-full"},
	"dnsmasq":           {"dnsmasq service with configurable upstream", "dnsmasq / dnsmasq-full"},
	"nslookup":          {"DNS lookup command", "nslookup"},
	"nft_json":          {"JSON inspection of the active fw4 ruleset", "nftables-json"},
	"nft_rules":         {"fw4 policy marking and destination lookup", ""},
	"nft_tproxy":        {"fw4 TProxy target", "kmod-nft-tproxy"},
	"nft_redirect":      {"fw4 DNS redirection", "kmod-nft-nat"},
	"xtables4":          {"legacy IPv4 rule inspection and restore", "iptables-zz-legacy"},
	"xtables6":          {"legacy IPv6 rule inspection and restore", "ip6tables-zz-legacy"},
	"fw3_active4":       {"active fw3 IPv4 chains", ""},
	"fw3_active6":       {"active fw3 IPv6 chains", ""},
	"xt_redirect4":      {"fw3 IPv4 DNS redirection", ""},
	"xt_redirect6":      {"fw3 IPv6 DNS redirection", "ip6tables-mod-nat"},
	"xt_addrtype":       {"fw3 destination address type match", "iptables-mod-extra"},
	"xt_tproxy":         {"fw3 TProxy target", "iptables-mod-tproxy"},
	"xt_rules":          {"fw3 filter and policy marking rules", ""},
	"tun_device":        {"usable /dev/net/tun device", "kmod-tun"},
}

// Only a DNS directive category and a validated dnsmasq instance identifier
// may cross this boundary. Addresses, paths and raw command errors stay local.
var dnsIssuePattern = regexp.MustCompile(`^dns_(default_upstream|servers_file|dynamic_config|no_instance|include_directory|shared_instance|config_unreadable|file_collision)( [a-zA-Z0-9_-]{1,48})?$`)

func parseFirewallCapabilities(report string) Environment {
	env := Environment{FirewallBackend: "unavailable"}
	header, rest, _ := strings.Cut(strings.TrimSpace(report), "\n")
	fields := strings.Fields(header)
	if len(fields) == 1 && fields[0] == "ambiguous" {
		env.FirewallBackend = "ambiguous"
	} else if len(fields) == 3 && (fields[0] == "fw3" || fields[0] == "fw4") {
		env.FirewallBackend, env.FirewallEngine, env.FirewallVersion = fields[0], fields[1], fields[2]
		env.Firewall4 = fields[0] == "fw4"
	}
	for _, line := range strings.Split(rest, "\n") {
		switch line {
		case "tun 1":
			env.FirewallTUN = true
		case "tproxy 1":
			env.FirewallTProxy = true
		default:
			if code, ok := strings.CutPrefix(line, "missing "); ok {
				if dnsIssuePattern.MatchString(code) {
					env.DNSIssue = code
					continue
				}
				req, known := networkRequirements[code]
				if known && !slices.Contains(env.NetworkRequirements, req.description) {
					env.NetworkRequirements = append(env.NetworkRequirements, req.description)
					if req.provider != "" && !slices.Contains(env.FirewallPackages, req.provider) {
						env.FirewallPackages = append(env.FirewallPackages, req.provider)
					}
				}
			}
		}
	}
	return env
}

func checkNetworkRequirements(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probe, networkHelper, "prerequisites").Output()
	if err == nil {
		return nil
	}
	if probe.Err() != nil {
		return probe.Err()
	}
	env := parseFirewallCapabilities(string(output))
	if env.DNSIssue != "" {
		return errors.New(env.DNSIssue)
	}
	if len(env.NetworkRequirements) == 0 {
		return errors.New("could not verify network prerequisites")
	}
	reason := "network prerequisites unavailable: " + strings.Join(env.NetworkRequirements, ", ")
	if len(env.FirewallPackages) > 0 {
		reason += " (providers: " + strings.Join(env.FirewallPackages, ", ") + ")"
	}
	return errors.New(reason)
}
