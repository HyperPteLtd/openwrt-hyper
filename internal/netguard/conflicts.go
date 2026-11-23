// Package netguard detects ownership conflicts before the router takes over
// transparent traffic. It never directly modifies a foreign network resource:
// a recognised proxy plugin is only stopped and disabled through its own init script.
package netguard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Resource struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Service string `json:"service,omitempty"`
}
type Result struct {
	Blocked   bool       `json:"blocked"`
	Resources []Resource `json:"resources"`
	// The plugins a resolve stopped and kept from starting at boot.
	Stopped []string `json:"stopped,omitempty"`
}

var services = []string{"openclash", "passwall2", "passwall", "homeproxy", "v2raya", "shadowsocksr", "nikki", "momo"}
var markers = map[string][]string{"openclash": {"openclash"}, "passwall2": {"passwall2", "psw2_"}, "passwall": {"passwall", "psw_"}, "homeproxy": {"homeproxy", "homeproxy_"}, "v2raya": {"v2raya"}, "shadowsocksr": {"shadowsocksr", "ssrplus", "ssr_"}, "nikki": {"nikki"}, "momo": {"momo_"}}

func owner(text string) string {
	lower := strings.ToLower(text)
	for _, name := range services {
		for _, mark := range markers[name] {
			if strings.Contains(lower, mark) {
				return name
			}
		}
	}
	return ""
}

type collector struct {
	seen map[Resource]bool
	own  ownership
}

func (c *collector) add(kind, name, service string) { c.seen[Resource{kind, name, service}] = true }
func (c *collector) sorted() []Resource {
	r := make([]Resource, 0, len(c.seen))
	for v := range c.seen {
		r = append(r, v)
	}
	sort.Slice(r, func(i, j int) bool {
		if r[i].Kind != r[j].Kind {
			return r[i].Kind < r[j].Kind
		}
		if r[i].Name != r[j].Name {
			return r[i].Name < r[j].Name
		}
		return r[i].Service < r[j].Service
	})
	return r
}
func binary(name string) string {
	for _, d := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin"} {
		p := filepath.Join(d, name)
		i, e := os.Stat(p)
		if e == nil && i.Mode().IsRegular() && i.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, errors.New("evidence exceeds limit")
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var out boundedOutput
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("cannot inspect resource")
	}
	return out.Bytes(), nil
}
func (c *collector) inspectCmd(ctx context.Context, name string, required bool, parse func([]byte) error, args ...string) {
	p := binary(name)
	if p == "" {
		if required {
			c.add("unverified", name, "")
		}
		return
	}
	data, e := command(ctx, p, args...)
	if e != nil || parse(data) != nil {
		c.add("unverified", name+" "+strings.Join(args, " "), "")
	}
}
func (c *collector) serviceList(data []byte) error {
	var list map[string]struct {
		Instances map[string]struct {
			Running bool `json:"running"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, service := range services {
		for _, i := range list[service].Instances {
			if i.Running {
				c.add("service", service, service)
				break
			}
		}
	}
	return nil
}
func (c *collector) nft(data []byte) error {
	var list struct {
		Entries []map[string]json.RawMessage `json:"nftables"`
	}
	if e := json.Unmarshal(data, &list); e != nil {
		return e
	}
	if list.Entries == nil {
		return errors.New("invalid ruleset")
	}
	for _, row := range list.Entries {
		for kind, raw := range row {
			var obj struct {
				Name, Table, Family, Comment string
				Expr                         []json.RawMessage
			}
			if e := json.Unmarshal(raw, &obj); e != nil {
				return e
			}
			if kind == "table" && obj.Name == "hyper" && obj.Family == "inet" && (obj.Comment != "hyper-owned-v1" || !c.own.activeNft) {
				c.add("nft", "inet hyper (unknown owner)", "")
			}
			if kind == "table" && obj.Name == "hyper_hold" && obj.Family == "inet" && (obj.Comment != "hyper-fault-v1" || !c.own.holdNft) {
				c.add("nft", "inet hyper_hold (unknown owner)", "")
			}
			for _, label := range []string{obj.Name, obj.Table, obj.Comment} {
				if who := owner(label); who != "" {
					c.add("nft", obj.Family+" "+kind+" "+who, who)
				}
			}
			for _, expr := range obj.Expr {
				var edge struct{ Jump, Goto *struct{ Target string } }
				if json.Unmarshal(expr, &edge) == nil {
					for _, v := range []*struct{ Target string }{edge.Jump, edge.Goto} {
						if v != nil {
							if who := owner(v.Target); who != "" {
								c.add("nft", obj.Family+" jump "+who, who)
							}
						}
					}
				}
			}
		}
	}
	return nil
}
func (c *collector) xt(family string, data []byte) error {
	fam := "4"
	if strings.HasPrefix(family, "ip6") {
		fam = "6"
	}
	allowed := map[string]int{}
	for rule, count := range c.own.xtRules[fam] {
		allowed[rule] = count
	}
	table := ""
	for _, line := range strings.Split(string(data), "\n") {
		cols := strings.Fields(line)
		if len(cols) == 0 {
			continue
		}
		if strings.HasPrefix(cols[0], "*") {
			table = strings.TrimPrefix(cols[0], "*")
			continue
		}
		for n, field := range cols {
			isChain := n == 0 && strings.HasPrefix(field, ":") || n > 0 && (cols[n-1] == "-A" || cols[n-1] == "-j" || cols[n-1] == "-g")
			if isChain {
				if chain := strings.TrimPrefix(field, ":"); table == "mangle" && reservedChain(chain) {
					key := xtKey(cols)
					if n == 0 {
						key = cols[0]
					}
					if allowed[key] <= 0 {
						c.add("xtables", family+" "+chain+" (unknown owner)", "")
					}
				}
				if who := owner(field); who != "" {
					c.add("xtables", family+" "+who, who)
				}
			}
		}
		key := xtKey(cols)
		if strings.HasPrefix(cols[0], ":") {
			key = cols[0]
		}
		if table == "mangle" && allowed[key] > 0 {
			allowed[key]--
		}
	}
	return nil
}
func number(s string) uint64 {
	v, e := strconv.ParseUint(s, 0, 32)
	if e != nil {
		return ^uint64(0)
	}
	return v
}
func (c *collector) routes(family string, b []byte) error {
	matched := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		reserved := strings.TrimSuffix(f[0], ":") == "21998"
		for i := 1; i < len(f); i++ {
			if (f[i-1] == "lookup" || f[i-1] == "table") && number(f[i]) == 21998 {
				reserved = true
			}
			if f[i-1] == "fwmark" {
				v, mask, has := strings.Cut(f[i], "/")
				m := uint64(0xffffffff)
				if has {
					m = number(mask)
				}
				if number(v)&m == 0x4f57&m {
					reserved = true
				}
			}
		}
		if reserved {
			key := strings.Join(f, " ")
			if c.own.reservedRoute(family, f) && !matched[key] {
				matched[key] = true
				continue
			}
			c.add("route", family+" reserved mark/table/preference", "")
		}
	}
	return nil
}
func (c *collector) processes(proc string) {
	entries, e := os.ReadDir(proc)
	if e != nil {
		c.add("unverified", "processes", "")
		return
	}
	for _, v := range entries {
		if _, e := strconv.Atoi(v.Name()); e != nil {
			continue
		}
		dir := filepath.Join(proc, v.Name())
		p, _ := os.Readlink(filepath.Join(dir, "exe"))

		comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
		name := strings.ToLower(strings.TrimSpace(string(comm)))
		// Service launchers often use sh or name their procd children by role.
		args, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		if supervisedCore(proc, v.Name(), p, args, "/usr/sbin/hyperd") {
			c.own.collectDescriptors(dir)
			continue
		}
		var service string
		for _, a := range bytes.Split(args, []byte{0}) {
			s := string(a)
			for _, known := range services {
				for _, prefix := range []string{"/tmp/etc/", "/usr/share/", "/usr/lib/", "/etc/init.d/"} {
					if s == prefix+known || strings.HasPrefix(s, prefix+known+"/") {
						service = known
					}
				}
			}
		}
		if who := owner(filepath.Base(p)); who != "" {
			service = who
		}
		if service != "" {
			c.add("process", service, service)
			continue
		}
		switch name {
		case "mihomo", "clash", "sing-box", "xray", "v2ray", "ss-redir", "ssr-redir", "ipt2socks":
			c.add("process", name, "")
		}
	}
}
func (c *collector) ports(proc string) {
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f, e := os.Open(filepath.Join(proc, "net", proto))
		if os.IsNotExist(e) && strings.HasSuffix(proto, "6") {
			continue
		}
		if e != nil {
			c.add("unverified", proto+" listeners", "")
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 4 || strings.HasPrefix(proto, "tcp") && f[3] != "0A" {
				continue
			}
			_, port, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			n, e := strconv.ParseInt(port, 16, 32)
			if e != nil {
				continue
			}
			if len(f) > 9 && c.own.sockets[f[9]] {
				continue
			}
			if n >= 19761 && n <= 19764 && (strings.HasPrefix(proto, "tcp") || n == 19762 || n == 19764) {
				c.add("listener", proto+"/"+strconv.FormatInt(n, 10), "")
			}
		}
		if sc.Err() != nil {
			c.add("unverified", proto+" listeners", "")
		}
		f.Close()

	}
}
func (c *collector) dns() {
	todo := []string{"/etc/dnsmasq.conf", "/etc/dnsmasq.d", "/tmp/dnsmasq.d"}
	extra, _ := filepath.Glob("/tmp/etc/dnsmasq.conf.*")
	todo = append(todo, extra...)
	seen := map[string]bool{}
	for len(todo) > 0 {
		p := filepath.Clean(todo[0])
		todo = todo[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		if len(seen) > 1024 {
			c.add("unverified", "dnsmasq include limit", "")
			return
		}
		if !(strings.HasPrefix(p, "/etc/") || strings.HasPrefix(p, "/tmp/") || strings.HasPrefix(p, "/var/etc/") || strings.HasPrefix(p, "/usr/share/dnsmasq/")) {
			c.add("unverified", "dnsmasq include outside config roots", "")
			continue
		}
		info, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			dest, se := filepath.EvalSymlinks(p)
			if se == nil {
				todo = append(todo, dest)
				continue
			}
		}
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			c.add("unverified", "dnsmasq include", "")
			continue
		}
		if info.IsDir() {
			es, e := os.ReadDir(p)
			if e != nil {
				c.add("unverified", "dnsmasq directory", "")
				continue
			}
			for _, v := range es {
				if !strings.HasPrefix(v.Name(), ".") {
					todo = append(todo, filepath.Join(p, v.Name()))
				}
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > 4<<20 {
			c.add("unverified", "oversize dnsmasq configuration", "")
			continue
		}
		b, e := os.ReadFile(p)
		if e != nil {
			c.add("unverified", "dnsmasq config", "")
			continue
		}
		if c.own.dnsFile(p, b) {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line[0] == '#' {
				continue
			}
			key, value, _ := strings.Cut(line, "=")
			if who := owner(p); who != "" {
				c.add("dnsmasq", who, who)
			}
			// Upstreams are not conflicts, even a local resolver (SmartDNS, AdGuard Home): DNS
			// protection sets LuCI's forwardings aside and dns-directories refuses the rest.
			if key == "conf-dir" || key == "conf-file" {
				next, _, _ := strings.Cut(value, ",")
				if who := owner(next); who != "" {
					c.add("dnsmasq", who, who)
				}
				if next != "" {
					if !filepath.IsAbs(next) {
						next = filepath.Join(filepath.Dir(p), next)
					}
					todo = append(todo, next)
				}
			}
		}
	}
}

func Scan(ctx context.Context) []Resource {
	c := collector{seen: map[Resource]bool{}, own: networkOwnership("/var/run/hyper/network")}
	c.processes("/proc")
	c.ports("/proc")
	c.dns()
	c.inspectCmd(ctx, "ubus", true, c.serviceList, "call", "service", "list")
	status, err := command(ctx, "/usr/libexec/hyper/network", "environment")
	backend, _, _ := strings.Cut(strings.TrimSpace(string(status)), " ")
	if err != nil || (backend != "fw4" && backend != "fw3") {
		c.add("unverified", "running firewall backend", "")
	}
	c.inspectCmd(ctx, "nft", backend == "fw4", c.nft, "-j", "list", "ruleset")
	for _, family := range []string{"ip", "ip6"} {
		seenLegacy := false
		for _, suffix := range []string{"tables-legacy-save", "tables-save"} {
			name := family + suffix
			path := binary(name)
			if path == "" {
				continue
			}
			version, _ := command(ctx, path, "-V")
			legacy := strings.Contains(string(version), "(legacy)")
			if seenLegacy && legacy {
				continue
			}
			c.inspectCmd(ctx, name, true, func(b []byte) error { return c.xt(name, b) })
			if legacy {
				seenLegacy = true
			}
		}
		tables, readErr := os.ReadFile("/proc/net/" + family + "_tables_names")
		if !seenLegacy && ((backend == "fw3" && family == "ip") || len(bytes.TrimSpace(tables)) > 0 || (readErr != nil && !os.IsNotExist(readErr))) {
			c.add("unverified", family+"tables legacy rules", "")
		}
	}
	for _, f := range []string{"-4", "-6"} {
		if f == "-6" {
			if _, e := os.Stat("/proc/net/if_inet6"); os.IsNotExist(e) {
				continue
			}
		}
		for _, q := range [][]string{{"-N", f, "rule", "show"}, {"-N", f, "route", "show", "table", "all"}} {
			c.inspectCmd(ctx, "ip", true, func(b []byte) error { return c.routes(f, b) }, q...)
		}
	}
	if _, e := os.Stat("/sys/class/net/owtun0"); e == nil && !c.own.tun {
		c.add("interface", "owtun0", "")
	}
	return c.sorted()
}

// Checker clears the way for a takeover. Stop and Disable act on one recognised proxy plugin
// through its init script.
type Checker struct {
	Dir           string
	Scan          func(context.Context) []Resource
	Stop, Disable func(context.Context, string) error
	// Settle bounds the wait for a stopped plugin's processes to exit: procd ends instances
	// after the init script has returned.
	Settle time.Duration
}

// A recognised plugin without an init script has nothing to stop; its leftovers stay found.
var errNotInstalled = errors.New("service not installed")

func initScript(ctx context.Context, service, action string) error {
	if !slices.Contains(services, service) {
		return errors.New("unknown service")
	}
	path := "/etc/init.d/" + service
	if i, e := os.Stat(path); e != nil || !i.Mode().IsRegular() || i.Mode()&0111 == 0 {
		return errNotInstalled
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, action)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

func SystemChecker() Checker {
	return Checker{
		Dir:  "/var/run/hyper/conflicts",
		Scan: Scan,
		Stop: func(ctx context.Context, s string) error { return initScript(ctx, s, "stop") },
		Disable: func(ctx context.Context, s string) error {
			if e := initScript(ctx, s, "disable"); e != nil {
				return e
			}
			// disable reports only its last rm; enabled tells whether a boot link is left.
			if initScript(ctx, s, "enabled") == nil {
				return errors.New("service still enabled")
			}
			return nil
		},
		Settle: 10 * time.Second,
	}
}

// Resolve stops every recognised proxy plugin the scan attributes something to and disables
// its autostart, so it cannot take the traffic back after a reboot, then scans again. Whatever
// is still found blocks the takeover: unknown processes, foreign or ownerless rules, leftovers
// of an uninstalled plugin and incomplete inspections are not Hyper's to remove.
func (c Checker) Resolve(ctx context.Context) (Result, error) {
	if e := os.MkdirAll(c.Dir, 0700); e != nil {
		return Result{Blocked: true}, e
	}
	lock, e := os.OpenFile(filepath.Join(c.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return Result{Blocked: true}, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return Result{Blocked: true}, e
	}
	found := c.Scan(ctx)
	seen := map[string]bool{}
	var acted []string
	var enabled []Resource
	for _, r := range found {
		s := r.Service
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		// Init scripts often exit non-zero for nothing; the rescan decides whether it stopped.
		if errors.Is(c.Stop(ctx, s), errNotInstalled) {
			continue
		}
		acted = append(acted, s)
		if c.Disable(ctx, s) != nil {
			enabled = append(enabled, Resource{"autostart", s, s})
		}
	}
	if len(acted) > 0 {
		found = c.settle(ctx, acted)
	}
	r := Result{Resources: append(found, enabled...)}
	for _, s := range acted {
		if !running(found, s) && !slices.Contains(enabled, Resource{"autostart", s, s}) {
			r.Stopped = append(r.Stopped, s)
		}
	}
	r.Blocked = len(r.Resources) > 0
	return r, nil
}

// running reports whether a plugin still has a live service instance or process.
func running(found []Resource, service string) bool {
	return slices.ContainsFunc(found, func(r Resource) bool {
		return r.Service == service && (r.Kind == "service" || r.Kind == "process")
	})
}

func (c Checker) settle(ctx context.Context, stopped []string) []Resource {
	deadline := time.Now().Add(c.Settle)
	for {
		found := c.Scan(ctx)
		if !slices.ContainsFunc(stopped, func(s string) bool { return running(found, s) }) || !time.Now().Before(deadline) {
			return found
		}
		select {
		case <-ctx.Done():
			return found
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Run prints what holds the network. With --resolve it first stops and disables the
// conflicting plugins; either way it fails while anything is left.
func Run(ctx context.Context, args []string, out io.Writer) error {
	var r Result
	var e error
	switch {
	case slices.Equal(args, []string{"conflicts"}):
		found := Scan(ctx)
		r = Result{Blocked: len(found) > 0, Resources: found}
	case slices.Equal(args, []string{"conflicts", "--resolve"}):
		r, e = SystemChecker().Resolve(ctx)
	default:
		return errors.New("usage: hyper-net conflicts [--resolve]")
	}
	if w := json.NewEncoder(out).Encode(r); w != nil {
		return w
	}
	if e != nil {
		return e
	}
	if r.Blocked {
		return errors.New("network takeover blocked by existing resources")
	}
	return nil
}
