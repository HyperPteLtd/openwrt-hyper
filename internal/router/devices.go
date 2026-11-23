package router

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Device struct {
	MAC          string   `json:"mac"`
	Name         string   `json:"name,omitempty"`
	Addresses    []string `json:"addresses"`
	Reachability string   `json:"reachability"`
}
type neighbor struct {
	IP        string   `json:"dst"`
	MAC       string   `json:"lladdr"`
	Interface string   `json:"dev"`
	State     []string `json:"state"`
}

// iproute2 builds without JSON output still return neighbour records as
// "address dev interface lladdr mac state", including IPv6 and router flags.
func readNeighbors(ctx context.Context) ([]neighbor, error) {
	var rows []neighbor
	if err := jsonCmd(ctx, &rows, "ip", "-j", "neigh", "show"); err == nil {
		return rows, nil
	}
	data, err := command(ctx, "ip", "neigh", "show")
	if err != nil {
		return nil, err
	}
	return parseNeighborText(string(data)), nil
}

func parseNeighborText(text string) []neighbor {
	var rows []neighbor
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		dev, mac := slices.Index(fields, "dev"), slices.Index(fields, "lladdr")
		if dev < 1 || dev+1 >= len(fields) || mac < 1 || mac+1 >= len(fields) {
			continue
		}
		rows = append(rows, neighbor{IP: fields[0], Interface: fields[dev+1], MAC: fields[mac+1], State: fields[mac+2:]})
	}
	return rows
}

func deviceMatch(address string) (obj, error) {
	if ip, e := netip.ParseAddr(address); e == nil && ip.Zone() == "" {
		return obj{"source_ip_cidr": netip.PrefixFrom(ip, ip.BitLen()).String()}, nil
	}
	if p, e := netip.ParsePrefix(address); e == nil {
		return obj{"source_ip_cidr": p.String()}, nil
	}
	if mac, e := net.ParseMAC(address); e == nil && len(mac) == 6 && mac[0]&1 == 0 {
		return obj{"source_mac_address": mac.String()}, nil
	}
	return nil, errors.New("expected a device MAC or IP prefix")
}
func Devices(ctx context.Context) ([]Device, error) {
	network, e := networkOptions(ctx)
	if len(network.LAN) == 0 {
		return nil, e
	}
	return devicesOnNetwork(ctx, network)
}

func devicesOnNetwork(ctx context.Context, network Network) ([]Device, error) {
	neighbours, e := readNeighbors(ctx)
	if e != nil {
		return nil, e
	}
	all := map[string]*Device{}
	add := func(mac, addr, iface string) *Device {
		x, e := net.ParseMAC(mac)
		if e != nil || len(x) != 6 {
			return nil
		}
		ip, e := netip.ParseAddr(addr)
		if e != nil {
			return nil
		}
		match := false
		for name, prefs := range network.LAN {
			if iface != "" && iface != name {
				continue
			}
			for _, p := range prefs {
				if p.Contains(ip) {
					match = true
					break
				}
			}
		}
		if !match {
			return nil
		}
		id := x.String()
		v := all[id]
		if v == nil {
			v = &Device{MAC: id, Reachability: "unknown"}
			all[id] = v
		}
		if !slices.Contains(v.Addresses, addr) {
			v.Addresses = append(v.Addresses, addr)
		}
		return v
	}
	for _, n := range neighbours {
		d := add(n.MAC, n.IP, n.Interface)
		if d == nil {
			continue
		}
		if slices.Contains(n.State, "REACHABLE") {
			d.Reachability = "reachable"
		} else if d.Reachability == "unknown" && slices.Contains(n.State, "FAILED") {
			d.Reachability = "unreachable"
		}
	}
	if f, e := os.Open("/tmp/dhcp.leases"); e == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			s := strings.Fields(sc.Text())
			if len(s) < 4 {
				continue
			}
			expiry, e := strconv.ParseInt(s[0], 10, 64)
			if e != nil || expiry != 0 && expiry < time.Now().Unix() {
				continue
			}
			if d := add(s[1], s[2], ""); d != nil && s[3] != "*" {
				d.Name = s[3]
			}
		}
	}
	out := make([]Device, 0, len(all))
	for _, d := range all {
		slices.Sort(d.Addresses)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out, nil
}
