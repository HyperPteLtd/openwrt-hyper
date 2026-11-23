// Inspect the generated configuration of the running dnsmasq instances before
// installing a default upstream. Never echo raw directives or resolver URLs.
package netguard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const upstreamFile = "99-hyper-upstream.conf"

type dnsInstance struct {
	includes, excludes, dirs []string
	seen                     map[string]bool
	disabled, forwardings    bool
	issue                    string
}

func configNameAllowed(name string, filters []string) bool {
	if name == "" || name[0] == '.' || strings.HasSuffix(name, "~") || (name[0] == '#' && strings.HasSuffix(name, "#")) {
		return false
	}
	included, limited := false, false
	for _, filter := range filters {
		if suffix, ok := strings.CutPrefix(filter, "*"); ok {
			limited = true
			included = included || strings.HasSuffix(name, suffix)
		} else if filter != "" && strings.HasSuffix(name, filter) {
			return false
		}
	}
	return !limited || included
}

func defaultServer(value string) bool {
	if !strings.HasPrefix(value, "/") {
		return true
	}
	domains := value[:strings.LastIndex(value, "/")]
	return slices.Contains(strings.Split(domains, "/"), "#")
}

func (d *dnsInstance) readFile(name string, own ownership, depth int) bool {
	canonical, err := filepath.EvalSymlinks(name)
	if err != nil || depth > 20 {
		return false
	}
	if d.seen[canonical] {
		return true
	}
	if len(d.seen) >= 1024 {
		return false
	}
	d.seen[canonical] = true
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if filepath.Base(name) == upstreamFile {
		if info.Size() <= 4096 {
			if data, err := os.ReadFile(name); err == nil && own.dnsFile(name, data) {
				return true
			}
		}
		d.issue = "dns_file_collision"
		return true
	}
	f, err := os.Open(canonical)
	if err != nil {
		return false
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimPrefix(line, "--"), "=")
		value = strings.TrimSpace(value)
		if quoted, ok := strings.CutPrefix(value, "\""); ok && strings.HasSuffix(quoted, "\"") {
			value = strings.TrimSuffix(quoted, "\"")
		}
		key = strings.TrimSpace(key)
		switch key {
		case "port":
			d.disabled = value == "0"
		case "interface":
			d.includes = append(d.includes, strings.Split(value, ",")...)
		case "except-interface":
			d.excludes = append(d.excludes, strings.Split(value, ",")...)
		case "server", "local", "address":
			switch {
			case !defaultServer(value):
			case depth == 0 && key == "server":
				// LuCI's DNS forwardings: the init script writes the section's server list only
				// into the main file, and the takeover sets them aside like OpenClash does.
				d.forwardings = true
			default:
				// Other plugins and hand edits, which the takeover cannot set aside.
				d.issue = "dns_default_upstream"
			}
		case "servers-file":
			// dnsmasq can reload this independently on SIGHUP.
			d.issue = "dns_servers_file"
		case "conf-script":
			d.issue = "dns_dynamic_config"
		case "conf-file":
			if !filepath.IsAbs(value) || !d.readFile(value, own, depth+1) {
				return false
			}
		case "conf-dir":
			parts := strings.Split(value, ",")
			dir := filepath.Clean(parts[0])
			if !filepath.IsAbs(dir) {
				return false
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return false
			}
			if configNameAllowed(upstreamFile, parts[1:]) {
				d.dirs = append(d.dirs, dir)
			}
			for _, entry := range entries {
				if entry.IsDir() || !configNameAllowed(entry.Name(), parts[1:]) {
					continue
				}
				if !d.readFile(filepath.Join(dir, entry.Name()), own, depth+1) {
					return false
				}
			}
		}
	}
	return lines.Err() == nil
}

// dnsmasq accepts an optional trailing wildcard on interface names.
func matchesInterface(pattern, dev string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(dev, prefix)
	}
	return pattern == dev
}

func (d *dnsInstance) scope(managed []string) (active, shared bool) {
	for _, dev := range managed {
		if slices.ContainsFunc(d.excludes, func(p string) bool { return matchesInterface(p, dev) }) {
			continue
		}
		if len(d.includes) == 0 || slices.ContainsFunc(d.includes, func(p string) bool { return matchesInterface(p, dev) }) {
			active = true
		}
	}
	for _, p := range d.includes {
		if p != "lo" && (!slices.Contains(managed, p) || strings.HasSuffix(p, "*")) {
			shared = true
		}
	}
	return active, shared
}

var dnsInstanceID = regexp.MustCompile("^[A-Za-z0-9_-]{1,48}$")
var uciSection = regexp.MustCompile("^[A-Za-z0-9_]+$")

// The network helper sets forwardings aside only in a section it can tell from the directory,
// exactly like this (dns_section): the instance's own default one, or the default up to
// OpenWrt 23.05, which every instance shares, while no other instance exists.
func forwardingsMovable(dir, id string, instances int) bool {
	return uciSection.MatchString(id) && (dir == "/tmp/dnsmasq."+id+".d" || dir == "/tmp/dnsmasq.d" && instances == 1)
}

func dnsIssue(out io.Writer, code, id string) error {
	if !dnsInstanceID.MatchString(id) {
		id = ""
	}
	fmt.Fprintln(out, strings.TrimSpace("missing "+code+" "+id))
	return fmt.Errorf("DNS takeover prerequisites unavailable")
}

func DNSDirectories(managed []string, out io.Writer) error {
	return dnsDirectories("/var/etc/dnsmasq.conf.*", networkOwnership("/var/run/hyper/network"), managed, out)
}

func dnsDirectories(pattern string, own ownership, managed []string, out io.Writer) error {
	configs, err := filepath.Glob(pattern)
	if err != nil || len(managed) == 0 {
		return dnsIssue(out, "dns_no_instance", "")
	}
	type instance struct {
		id          string
		dirs        []string
		forwardings bool
	}
	var selected []instance
	unmanaged := map[string]bool{}
	for _, cfg := range configs {
		id := strings.TrimPrefix(filepath.Base(cfg), "dnsmasq.conf.")
		d := &dnsInstance{seen: map[string]bool{}}
		if !d.readFile(cfg, own, 0) {
			return dnsIssue(out, "dns_config_unreadable", id)
		}
		if d.disabled {
			continue
		}
		hit, shared := d.scope(managed)
		if !hit {
			for _, dir := range d.dirs {
				real, err := filepath.EvalSymlinks(dir)
				if err == nil {
					unmanaged[real] = true
				}
			}
			continue
		}
		if shared {
			return dnsIssue(out, "dns_shared_instance", id)
		}
		if d.issue != "" {
			return dnsIssue(out, d.issue, id)
		}
		selected = append(selected, instance{id, d.dirs, d.forwardings})
	}
	if len(selected) == 0 {
		return dnsIssue(out, "dns_no_instance", "")
	}
	var dirs []string
	for _, inst := range selected {
		chosen := ""
		for _, dir := range inst.dirs {
			if !(strings.HasPrefix(dir, "/tmp/") || strings.HasPrefix(dir, "/var/")) || strings.ContainsAny(dir, " \t\r\n") {
				continue
			}
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() {
				continue
			}
			real, err := filepath.EvalSymlinks(dir)
			if err == nil && !unmanaged[real] {
				chosen = dir
				break
			}
		}
		if chosen == "" {
			return dnsIssue(out, "dns_include_directory", inst.id)
		}
		// Forwardings the helper cannot set aside would stay beside the core.
		if inst.forwardings && !forwardingsMovable(chosen, inst.id, len(configs)) {
			return dnsIssue(out, "dns_default_upstream", inst.id)
		}
		if !slices.Contains(dirs, chosen) {
			dirs = append(dirs, chosen)
		}
	}
	for _, dir := range dirs {
		fmt.Fprintln(out, dir)
	}
	return nil
}
