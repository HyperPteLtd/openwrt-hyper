package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

func uci(ctx context.Context, k string) string {
	b, _ := command(ctx, "uci", "-q", "get", "hyper.main."+k)
	return strings.TrimSpace(string(b))
}

type netifAddress struct {
	Address string `json:"address"`
	Mask    int    `json:"mask"`
}
type netifStatus struct {
	Up        bool           `json:"up"`
	Device    string         `json:"l3_device"`
	V4        []netifAddress `json:"ipv4-address"`
	V6        []netifAddress `json:"ipv6-address"`
	Delegated []struct {
		Local netifAddress `json:"local-address"`
	} `json:"ipv6-prefix-assignment"`
}

func validLANDevice(name string) bool {
	return name != "" && len(name) <= 15 && name != "lo" && strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") == ""
}

// Keep the last applied device precedence available to the firewall if the
// core exits. Smart-mode @proxy exceptions follow explicit device routes,
// just as they do in Compile's default-direct rule.
func deviceGuardPolicy(i Intent) (string, error) {
	var rules, exceptions strings.Builder
	defaultDirect := i.Mode == "smart" && i.Policy.DefaultDirect
	for _, d := range i.Policy.Devices {
		if i.Mode == "global" && d.Target != "@direct" || d.Target == "@proxy" && !defaultDirect {
			continue
		}
		match, err := deviceMatch(d.Address)
		if err != nil {
			return "", err
		}
		family, address := "mac", match["source_mac_address"]
		if raw, ok := match["source_ip_cidr"].(string); ok {
			prefix := netip.MustParsePrefix(raw)
			address = prefix.Masked().String()
			family = "4"
			if prefix.Addr().Is6() {
				family = "6"
			}
		}
		target := "block"
		if d.Target == "@direct" {
			target = "direct"
		}
		dst := &rules
		if d.Target == "@proxy" {
			dst = &exceptions
		}
		fmt.Fprintf(dst, "%s %s %s\n", family, address, target)
	}
	rules.WriteString(exceptions.String())
	if defaultDirect {
		rules.WriteString("default direct\n")
	} else {
		rules.WriteString("default block\n")
	}
	return rules.String(), nil
}

// directDevices lists the devices the firewall leaves out of the takeover because their rule
// sends them direct, as OpenClash's LAN blacklist does: consoles, IPTV and download boxes keep
// full speed and their NAT type instead of passing through the core. The core takes the first
// device rule that matches, so a direct rule is listed only when no earlier rule can send some
// of its devices elsewhere. A MAC rule's device may hold any address, so IP and MAC rules shadow
// each other whenever both kinds route elsewhere first. Unlisted direct devices still go direct
// through the core.
func directDevices(i Intent) (string, error) {
	var out strings.Builder
	var routed []netip.Prefix
	routedMACs, listed := map[string]bool{}, map[string]bool{}
	for _, d := range i.Policy.Devices {
		// As in Compile: global mode keeps only direct devices, and @proxy only exempts a device from default direct.
		if i.Mode == "global" && d.Target != "@direct" || d.Target == "@proxy" {
			continue
		}
		match, err := deviceMatch(d.Address)
		if err != nil {
			return "", err
		}
		mac := stringOption(match, "source_mac_address")
		var prefix netip.Prefix
		if mac == "" {
			prefix = netip.MustParsePrefix(stringOption(match, "source_ip_cidr")).Masked()
		}
		if d.Target != "@direct" {
			if mac != "" {
				routedMACs[mac] = true
			} else {
				routed = append(routed, prefix)
			}
			continue
		}
		var line string
		switch {
		case mac != "":
			if len(routed) > 0 || routedMACs[mac] {
				continue
			}
			line = "mac " + mac
		case len(routedMACs) > 0 || slices.ContainsFunc(routed, prefix.Overlaps):
			continue
		case prefix.Addr().Is4():
			line = "4 " + prefix.String()
		default:
			line = "6 " + prefix.String()
		}
		if !listed[line] {
			listed[line] = true
			out.WriteString(line + "\n")
		}
	}
	return out.String(), nil
}
func telegramAllowance(t *Telegram, n Network) (string, string, error) {
	if t == nil {
		return "", "", nil
	}
	if !validLANDevice(t.Interface) || !t.Address.IsValid() || t.Port < 1024 || len(t.Allow) == 0 || len(t.Allow) > 128 {
		return "", "", errors.New("invalid Telegram LAN permission")
	}
	var local netip.Prefix
	for _, p := range n.LAN[t.Interface] {
		if p.Addr() == t.Address {
			local = p
			break
		}
	}
	if !local.IsValid() {
		return "", "", errors.New("Telegram address is not assigned to managed interface")
	}
	parts := make([]string, 0, len(t.Allow))
	for _, p := range t.Allow {
		if !p.IsValid() || !local.Contains(p.Addr()) || p.Bits() < local.Bits() {
			return "", "", errors.New("Telegram range outside managed LAN")
		}
		parts = append(parts, p.Masked().String())
	}
	xtFamily := "4"
	family := "ip"
	if t.Address.Is6() {
		family = "ip6"
		xtFamily = "6"
	}
	var legacy strings.Builder
	// iptables-restore inserts each INPUT rule at the head, so emit the deny first.
	fmt.Fprintf(&legacy, "%s - - %s %d\n", xtFamily, t.Address, t.Port)
	for _, prefix := range parts {
		fmt.Fprintf(&legacy, "%s %s %s %s %d\n", xtFamily, t.Interface, prefix, t.Address, t.Port)
	}
	accept := fmt.Sprintf("iifname %q %s saddr { %s } %s daddr %s tcp dport %d accept comment %q\n", t.Interface, family, strings.Join(parts, ", "), family, t.Address, t.Port, "hyper-owned-v1-input")
	deny := fmt.Sprintf("%s daddr %s tcp dport %d drop comment %q\n", family, t.Address, t.Port, "hyper-owned-v1-input")
	return accept + deny, legacy.String(), nil
}
func interfacePrefixes(n netifStatus) ([]netip.Prefix, error) {
	if !n.Up || !validLANDevice(n.Device) {
		return nil, errors.New("managed LAN interface unavailable")
	}
	res := []netip.Prefix{}
	addresses := append(slices.Clone(n.V4), n.V6...)
	for _, d := range n.Delegated {
		addresses = append(addresses, d.Local)
	}
	for _, v := range addresses {
		ip, err := netip.ParseAddr(v.Address)
		if err != nil || !ip.IsGlobalUnicast() {
			continue
		}
		p := netip.PrefixFrom(ip, v.Mask)
		if p.IsValid() && !slices.Contains(res, p) {
			res = append(res, p)
		}
	}
	if len(res) == 0 {
		return nil, errors.New("managed LAN has no usable address")
	}
	return res, nil
}

// ipv6Uplink reports whether a socket without a chosen source address, as the core opens, can
// route to the IPv6 internet. Source-specific defaults ("default from <prefix>", common with
// PPPoE on OpenWrt) do not count. Without such a route the core would dial unreachable IPv6
// node and site addresses, so IPv6 is taken over only with such a route; there is no
// user setting. The lookup sends no traffic.
func ipv6Uplink(ctx context.Context) bool {
	return exec.CommandContext(ctx, "ip", "-6", "route", "get", "2400:3200::1").Run() == nil
}

// chinaShortcut reports whether the firewall sends mainland addresses straight out. Global mode
// proxies everything, as its name says, so the shortcut only applies in smart mode.
func chinaShortcut(i Intent, n Network) bool { return n.ChinaDirect && i.Mode == "smart" }

// applyArgs are the network helper's apply arguments. The firewall takes over IPv6 exactly when
// the core was configured for it, and lets out the sockets of gid, the group the core runs in
// (coregroup): the helper never resolves a group of its own.
func applyArgs(i Intent, n Network, gid int) []string {
	family := "ipv4"
	if n.Family6 {
		family = "ipv6"
	}
	args := []string{"apply", family}
	if chinaShortcut(i, n) {
		args = append(args, "china")
	}
	if n.SideRouter {
		args = append(args, "side-router")
	} else {
		args = append(args, "main-router")
	}
	return append(args, "gid="+strconv.Itoa(gid))
}

// fw4Active matches the network helper, which captures the router's own traffic only on fw4.
func fw4Active(ctx context.Context) bool {
	return heldOnFW4(RunDir+"/network", nftTableGone) ||
		exec.CommandContext(ctx, "nft", "list", "chain", "inet", "fw4", "input").Run() == nil
}

// heldOnFW4 reports a takeover the helper applied on fw4 whose table is still in place. A
// stopped fw4 (fw4 flush) leaves that table running alone until fw4 starts and replays it, so
// it still counts as fw4: otherwise Fake-IP and the China shortcut would flip and restart
// into a takeover that cannot start without fw4.
func heldOnFW4(state string, gone func(string) bool) bool {
	backend, e := os.ReadFile(state + "/backend")
	if e != nil || strings.TrimSpace(string(backend)) != "fw4" {
		return false
	}
	if _, e = os.Stat(state + "/owned"); e != nil {
		return false
	}
	return !gone("hyper")
}

// sideRouter reports a side router (旁路由): the preferred IPv4 default route of the main table
// (/proc/net/route) leaves through a managed LAN, so the main router sits on that LAN. The
// network helper is told the result, so the takeover and networkKey agree.
func sideRouter(lan map[string][]netip.Prefix, route []byte) bool {
	device, best := "", uint64(math.MaxUint64)
	for _, line := range strings.Split(string(route), "\n") {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		// Like the helper's "ip route show default", skip unreachable, prohibit and blackhole.
		flags, e := strconv.ParseUint(f[3], 16, 16)
		metric, e2 := strconv.ParseUint(f[6], 10, 64)
		if e != nil || e2 != nil || flags&0x0200 != 0 || f[0] == "*" || metric >= best {
			continue
		}
		device, best = f[0], metric
	}
	_, managed := lan[device]
	return managed
}
func networkOptions(ctx context.Context) (Network, error) {
	n := Network{Method: uci(ctx, "method"), DNS: uci(ctx, "dns") == "1", Family6: ipv6Uplink(ctx),
		Domestic: upstreams(uci(ctx, "dns_domestic"), DomesticDNS), Foreign: upstreams(uci(ctx, "dns_foreign"), ForeignDNS)}
	fw4 := fw4Active(ctx)
	n.RouterSelf = uci(ctx, "router_self") != "0"
	n.LogLevel = logLevel(uci(ctx, "log_level"))
	n.FakeIP = n.DNS && fw4 && n.RouterSelf
	n.FailOpen = uci(ctx, "fail_open") != "0"
	n.ChinaDirect = uci(ctx, "bypass_cn") == "1" && fw4
	var e error
	n.LAN, e = managedLAN(strings.Fields(uci(ctx, "network")), n.Family6, func(name string) (netifStatus, error) {
		var status netifStatus
		return status, jsonCmd(ctx, &status, "ubus", "call", "network.interface."+name, "status")
	})
	if e != nil {
		return n, e
	}
	route, _ := os.ReadFile("/proc/net/route")
	n.SideRouter = sideRouter(n.LAN, route)
	// No WAN resolver is published while PPPoE redials, with peerdns off and no custom DNS, or on
	// a side router; the core then bootstraps from the public default nameservers alone.
	b, _ := os.ReadFile("/tmp/resolv.conf.d/resolv.conf.auto")
	n.Resolver = wanResolver(b)
	return n, nil
}

// managedLAN maps the usable managed interfaces to their addresses. One that is down, has no
// device or no address of a taken-over family (a guest Wi-Fi switched off) is left out, as
// devices() in the network helper does, so the rest are still taken over; its hotplug notice
// changes networkKey once it comes up. Only an invalid name or no usable interface fails.
func managedLAN(names []string, family6 bool, status func(string) (netifStatus, error)) (map[string][]netip.Prefix, error) {
	lan := map[string][]netip.Prefix{}
	skipped := errors.New("no managed LAN")
	for _, name := range names {
		if strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
			return nil, errors.New("invalid managed LAN")
		}
		s, err := status(name)
		if err != nil {
			skipped = errors.New("managed LAN interface unavailable")
			continue
		}
		ps, err := interfacePrefixes(s)
		// The helper probes only the families it takes over.
		if err == nil && !family6 && !slices.ContainsFunc(ps, func(p netip.Prefix) bool { return p.Addr().Is4() }) {
			err = errors.New("managed LAN has no usable address")
		}
		if err != nil {
			skipped = err
			continue
		}
		lan[s.Device] = append(lan[s.Device], ps...)
	}
	if len(lan) == 0 {
		return nil, skipped
	}
	return lan, nil
}

// wanResolver returns the first global unicast nameserver netifd published, or "" for none.
func wanResolver(resolv []byte) string {
	for _, line := range strings.Split(string(resolv), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "nameserver" {
			continue
		}
		if addr, err := netip.ParseAddr(fields[1]); err == nil && addr.IsGlobalUnicast() {
			return addr.String()
		}
	}
	return ""
}

// NetworkChanged reports whether the network differs from what the last start, reload or
// notice saw, and whether the difference needs a new takeover rather than an in-place reload.
// Hotplug reports every interface, and most events (address lifetimes, router advertisements,
// interfaces nobody uses) change nothing.
func (s *System) NetworkChanged(ctx context.Context) (changed, takeover bool) {
	n, e := networkOptions(ctx)
	return s.observe(ctx, n, e)
}

// observe records the network a start or reload is built from. A read cut short by its
// context proves nothing and counts as a change.
func (s *System) observe(ctx context.Context, n Network, e error) (changed, takeover bool) {
	if ctx.Err() != nil {
		return true, true
	}
	all := fmt.Sprint(n, e)
	// Upstreams reach only the core configuration, not networkKey.
	n.Resolver, n.Domestic, n.Foreign = "", nil, nil
	seen := [2]string{all, fmt.Sprint(n, e)}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed, takeover = seen[0] != s.network[0], seen[1] != s.network[1]
	s.network = seen
	return
}
