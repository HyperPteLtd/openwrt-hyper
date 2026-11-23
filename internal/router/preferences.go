package router

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Preferences struct {
	UpdateHours int `json:"update_hours"`
	// UpdateAt is the hour of the day (router time) the account is read at instead of every
	// UpdateHours; -1 clears it. RouterSelf takes the router's own connections over too.
	// LogLevel is the core's. AutoCheck looks for a new plugin version daily, AutoUpdate also
	// installs it. Omitted, each keeps its saved value.
	UpdateAt    *int     `json:"update_at,omitempty"`
	RouterSelf  *bool    `json:"router_self,omitempty"`
	LogLevel    string   `json:"log_level,omitempty"`
	AutoCheck   *bool    `json:"auto_check,omitempty"`
	AutoUpdate  *bool    `json:"auto_update,omitempty"`
	Enabled     bool     `json:"enabled"`
	Method      string   `json:"method"`
	DNS         bool     `json:"dns"`
	FailOpen    *bool    `json:"fail_open,omitempty"`
	BypassCN    *bool    `json:"bypass_cn,omitempty"`
	DNSDomestic []string `json:"dns_domestic,omitempty"`
	DNSForeign  []string `json:"dns_foreign,omitempty"`
	Networks    []string `json:"networks"`
}

var networkName = regexp.MustCompile("^[A-Za-z0-9_]{1,32}$")

func (p Preferences) Validate() error {
	if p.UpdateHours < 0 || p.UpdateHours > 720 {
		return errors.New("invalid subscription refresh period")
	}
	if p.Method != "tproxy" && p.Method != "tun" {
		return errors.New("unsupported network method")
	}
	if p.UpdateAt != nil && (*p.UpdateAt < -1 || *p.UpdateAt > 23) {
		return errors.New("invalid subscription refresh hour")
	}
	if p.LogLevel != "" && !slices.Contains(LogLevels, p.LogLevel) {
		return errors.New("invalid core log level")
	}
	for _, list := range [][]string{p.DNSDomestic, p.DNSForeign} {
		if list != nil && (len(list) == 0 || len(list) > 4) {
			return errors.New("choose one to four DNS upstreams")
		}
		for _, v := range list {
			if !validUpstream(v) {
				return errors.New("invalid DNS upstream")
			}
		}
	}
	if len(p.Networks) == 0 || len(p.Networks) > 16 {
		return errors.New("choose at least one LAN network")
	}
	seen := map[string]bool{}
	for _, n := range p.Networks {
		if !networkName.MatchString(n) || n == "wan" || n == "wan6" || n == "loopback" || seen[n] {
			return errors.New("invalid or duplicate LAN network")
		}
		seen[n] = true
	}
	return nil
}

// SameNetwork reports whether two preference sets produce the same takeover.
func (p Preferences) SameNetwork(o Preferences) bool {
	return p.Method == o.Method && p.DNS == o.DNS && slices.Equal(p.Networks, o.Networks) &&
		sameBit(p.FailOpen, o.FailOpen) && sameBit(p.BypassCN, o.BypassCN) && sameBit(p.RouterSelf, o.RouterSelf) &&
		(p.LogLevel == "" || o.LogLevel == "" || p.LogLevel == o.LogLevel) &&
		sameList(p.DNSDomestic, o.DNSDomestic) && sameList(p.DNSForeign, o.DNSForeign)
}

// An omitted list keeps its saved value.
func sameList(a, b []string) bool { return a == nil || b == nil || slices.Equal(a, b) }

// An omitted switch keeps its saved value.
func sameBit(a, b *bool) bool { return a == nil || b == nil || *a == *b }
func ReadPreferences(ctx context.Context) (Preferences, error) {
	p := Preferences{Enabled: uci(ctx, "enabled") == "1", Method: uci(ctx, "method"), DNS: uci(ctx, "dns") == "1", Networks: strings.Fields(uci(ctx, "network"))}
	p.UpdateHours, _ = strconv.Atoi(uci(ctx, "update_hours"))
	failOpen := uci(ctx, "fail_open") != "0"
	p.FailOpen = &failOpen
	// Narrows what the firewall hands to the core, so it stays off unless chosen.
	cn := uci(ctx, "bypass_cn") == "1"
	p.BypassCN = &cn
	p.DNSDomestic = upstreams(uci(ctx, "dns_domestic"), DomesticDNS)
	p.DNSForeign = upstreams(uci(ctx, "dns_foreign"), ForeignDNS)
	at := UpdateHour(uci(ctx, "update_at"))
	p.UpdateAt = &at
	self, check, update := uci(ctx, "router_self") != "0", uci(ctx, "auto_check") != "0", uci(ctx, "auto_update") == "1"
	p.RouterSelf, p.AutoCheck, p.AutoUpdate = &self, &check, &update
	p.LogLevel = logLevel(uci(ctx, "log_level"))
	return p, p.Validate()
}
func SavePreferences(ctx context.Context, p Preferences) error {
	if e := p.Validate(); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(RunDir, "uci-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	bit := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	var b strings.Builder
	if p.FailOpen != nil {
		b.WriteString("set hyper.main.fail_open='" + bit(*p.FailOpen) + "'\n")
	}
	if p.BypassCN != nil {
		b.WriteString("set hyper.main.bypass_cn='" + bit(*p.BypassCN) + "'\n")
	}
	for k, v := range map[string]*bool{"router_self": p.RouterSelf, "auto_check": p.AutoCheck, "auto_update": p.AutoUpdate} {
		if v != nil {
			b.WriteString("set hyper.main." + k + "='" + bit(*v) + "'\n")
		}
	}
	if p.UpdateAt != nil && *p.UpdateAt < 0 {
		b.WriteString("delete hyper.main.update_at\n")
	} else if p.UpdateAt != nil {
		b.WriteString("set hyper.main.update_at='" + strconv.Itoa(*p.UpdateAt) + "'\n")
	}
	if p.LogLevel != "" {
		b.WriteString("set hyper.main.log_level='" + p.LogLevel + "'\n")
	}
	for k, list := range map[string][]string{"dns_domestic": p.DNSDomestic, "dns_foreign": p.DNSForeign} {
		if list != nil {
			b.WriteString("delete hyper.main." + k + "\n")
			for _, v := range list {
				b.WriteString("add_list hyper.main." + k + "='" + v + "'\n")
			}
		}
	}
	for k, v := range map[string]string{"update_hours": strconv.Itoa(p.UpdateHours), "enabled": bit(p.Enabled), "method": p.Method, "dns": bit(p.DNS)} {
		b.WriteString("set hyper.main." + k + "='" + v + "'\n")
	}
	// Options from earlier builds that are no longer user settings: bypass lists, the DNS takeover
	// and DNS mode switches, IPv6, common ports and the tuning switches that now always use the
	// recommended value.
	for _, k := range []string{"bypass_ip", "bypass_port", "bypass_source", "proxy_ip", "dns_intercept", "ipv6", "common_ports",
		"dns_mode", "local_proxy", "block_quic", "fake_ip", "sniff", "tcp_concurrent", "unified_delay"} {
		b.WriteString("delete hyper.main." + k + "\n")
	}
	b.WriteString("delete hyper.main.network\n")
	for _, v := range p.Networks {
		b.WriteString("add_list hyper.main.network='" + v + "'\n")
	}
	b.WriteString("commit hyper\n")
	// -t stages changes in this job's directory and commits to /etc/config.
	// -P enables UCI's no-commit mode and would discard the update on cleanup.
	cmd := exec.CommandContext(ctx, "uci", "-q", "-t", tmp, "batch")
	cmd.Stdin = strings.NewReader(b.String())
	if cmd.Run() != nil {
		return errors.New("could not save network preferences")
	}
	return nil
}

var upstreamPattern = regexp.MustCompile(`^((https|tls|quic)://[A-Za-z0-9.:\[\]_-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?|[0-9.]+|[0-9A-Fa-f:]+)$`)

// validUpstream accepts DoH, DoT, DoQ or a plain resolver address; quotes and
// spaces are excluded so the value is safe in UCI and YAML.
func validUpstream(v string) bool { return len(v) <= 200 && upstreamPattern.MatchString(v) }

// LogLevels are the core's, quietest first.
var LogLevels = []string{"silent", "error", "warning", "info", "debug"}

// logLevel is a saved level, warning when unset or unknown.
func logLevel(saved string) string {
	if slices.Contains(LogLevels, saved) {
		return saved
	}
	return "warning"
}

// UpdateHour is a saved refresh hour, -1 when none.
func UpdateHour(saved string) int {
	if h, err := strconv.Atoi(saved); err == nil && h >= 0 && h <= 23 && saved == strconv.Itoa(h) {
		return h
	}
	return -1
}

var (
	DomesticDNS = []string{"https://223.5.5.5/dns-query", "https://120.53.53.53/dns-query"}
	ForeignDNS  = []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"}
)

func upstreams(saved string, fallback []string) []string {
	var r []string
	for _, v := range strings.Fields(saved) {
		if validUpstream(v) && len(r) < 4 {
			r = append(r, v)
		}
	}
	if len(r) == 0 {
		return slices.Clone(fallback)
	}
	return r
}
