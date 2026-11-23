// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package mihomo

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"github.com/HyperPteLtd/openwrt-hyper/internal/uci"
)

// Modes the UI offers. "dns" is smart routing with no LAN interception (DNS protection only).
const (
	ModeSmart  = "smart"
	ModeGlobal = "global"
)

// Interception methods: hyperd picks one at each start (TProxy where the firewall has it).
const (
	MethodTProxy = "tproxy"
	MethodTUN    = "tun"
)

// LAN access control (control.lan_proxy_mode).
const (
	LANAll          = "disabled"      // every device proxied
	LANExceptListed = "except_listed" // lan_direct_mac_addrs stay direct
	LANListedOnly   = "listed_only"   // only lan_proxy_mac_addrs proxied
)

var (
	DefaultForeignDNS  = []string{"https://1.1.1.1/dns-query"}
	DefaultDomesticDNS = []string{"223.5.5.5"}
)

// Settings is what the renderer needs from /etc/config/hyper.
type Settings struct {
	Mode    string // smart | global
	DNSOnly bool   // smart DNS split, no LAN interception
	Method  string // tproxy | tun
	// IPv6 interception follows the router's IPv6 uplink (decided at start, not a setting).
	IPv6 bool
	// FakeIP follows interception: whenever LAN traffic reaches the core, lookups get fake
	// addresses it maps back (not a setting; DNS-only mode keeps real ones).
	FakeIP   bool
	LogLevel string // mihomo level
	MainNode string // node id

	// Upstreams; "wan" entries are replaced by the WAN resolver at render time.
	ForeignDNS, DomesticDNS []string

	// The router's own traffic goes through the core (firewall side; also exempted from
	// listed_only so the router is not sent direct by the device rules).
	RouterSelf bool
	LANMode    string
	DirectMACs []string
	ProxyMACs  []string
	// Destinations that are always proxied in smart mode (Telegram ranges by default).
	ProxyCIDRs []string

	Mark          int // routing mark of the core's own sockets
	MixedPort     int
	TProxyPort    int
	DNSPort       int
	ControlPort   int
	TunName       string
	TunAddr4      string
	TunAddr6      string
	TunMTU        int
	DisabledRules []string // server rule keys switched off
}

var tunName = lazyre.New(`^[A-Za-z0-9_.-]{1,15}$`)

// FromUCI reads and validates Settings.
func FromUCI(c *uci.Config) (Settings, error) {
	s := Settings{
		Mode: ModeSmart, Method: MethodTProxy, LogLevel: "warning",
		MainNode:   c.Get("config", "main_node"),
		DNSOnly:    c.Get("config", "dns_only") == "1",
		RouterSelf: c.Get("control", "proxy_router_self") != "0",
		LANMode:    LANAll, TunName: "hypertun0", TunAddr4: "172.19.0.1/30", TunAddr6: "fdfe:dcba:9876::1/126",
		DisabledRules: c.Section("rules").List("disabled"),
	}
	s.FakeIP = !s.DNSOnly
	switch m := c.Get("config", "routing_mode"); m {
	case "", ModeSmart:
	case ModeGlobal:
		s.Mode = ModeGlobal
	default:
		return s, fmt.Errorf("unsupported routing mode %q", m)
	}
	switch l := c.Get("config", "log_level"); l {
	case "", "warn", "warning":
	case "trace", "debug":
		s.LogLevel = "debug"
	case "info", "error", "silent":
		s.LogLevel = l
	default:
		return s, fmt.Errorf("unsupported log level %q", l)
	}
	s.ForeignDNS = dnsList(c.Section("config").List("dns_server"), DefaultForeignDNS)
	s.DomesticDNS = dnsList(c.Section("config").List("china_dns_server"), DefaultDomesticDNS)

	ctl := c.Section("control")
	switch m := ctl.Get("lan_proxy_mode"); m {
	case "", LANAll:
	case LANExceptListed, LANListedOnly:
		s.LANMode = m
	default:
		return s, fmt.Errorf("unsupported LAN proxy mode %q", m)
	}
	var err error
	if s.DirectMACs, err = macs(ctl.List("lan_direct_mac_addrs")); err != nil {
		return s, err
	}
	if s.ProxyMACs, err = macs(ctl.List("lan_proxy_mac_addrs")); err != nil {
		return s, err
	}
	for _, v := range append(ctl.List("wan_proxy_ipv4_ips"), ctl.List("wan_proxy_ipv6_ips")...) {
		p, err := parsePrefix(v)
		if err != nil {
			return s, fmt.Errorf("wan proxy address %q: %w", v, err)
		}
		s.ProxyCIDRs = append(s.ProxyCIDRs, p.String())
	}

	infra := c.Section("infra")
	ports := []struct {
		dst  *int
		key  string
		def  int
		low  int
		high int
	}{
		{&s.Mark, "self_mark", 100, 1, 1<<31 - 1},
		{&s.MixedPort, "mixed_port", 5330, 1, 65535},
		{&s.TProxyPort, "tproxy_port", 5332, 1, 65535},
		{&s.DNSPort, "dns_port", 5333, 1, 65535},
		{&s.ControlPort, "controller_port", 5334, 1, 65535},
		{&s.TunMTU, "tun_mtu", 9000, 1280, 65535},
	}
	for _, p := range ports {
		*p.dst = p.def
		if v := infra.Get(p.key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < p.low || n > p.high {
				return s, fmt.Errorf("infra.%s: invalid value %q", p.key, v)
			}
			*p.dst = n
		}
	}
	if v := infra.Get("tun_name"); v != "" {
		if !tunName().MatchString(v) {
			return s, fmt.Errorf("infra.tun_name: invalid value %q", v)
		}
		s.TunName = v
	}
	for _, a := range []struct {
		dst *string
		key string
	}{{&s.TunAddr4, "tun_addr4"}, {&s.TunAddr6, "tun_addr6"}} {
		if v := infra.Get(a.key); v != "" {
			if _, err := netip.ParsePrefix(v); err != nil {
				return s, fmt.Errorf("infra.%s: %w", a.key, err)
			}
			*a.dst = v
		}
	}
	return s, nil
}

func dnsList(values, fallback []string) []string {
	var out []string
	for _, v := range values {
		out = append(out, strings.Fields(v)...)
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func macs(list []string) ([]string, error) {
	var out []string
	for _, v := range list {
		m, err := net.ParseMAC(v)
		if err != nil || len(m) != 6 {
			return nil, fmt.Errorf("invalid MAC address %q", v)
		}
		out = append(out, m.String())
	}
	return out, nil
}

func parsePrefix(v string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(v); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, errors.New("expected an address or prefix")
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}
