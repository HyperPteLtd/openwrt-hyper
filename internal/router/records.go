package router

import (
	"bytes"
	"context"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

const recordLimit = 512

type Event struct {
	Count   int    `json:"count,omitempty"`
	Time    int64  `json:"time"`
	Source  string `json:"source"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"` // a core error's redacted operation or words (coreDetail), or the plugins a start stopped
	Folded  bool   `json:"folded,omitempty"` // counts lines of more different details than detailVariants
}
type Records struct {
	minTime     int64
	routeSince  int64  // the current core started; earlier routes name another configuration
	source      string // the subscription the running core uses
	recording   bool   // connection records are on; see Policy.RecordConnections
	failures    uint64 // numbers the records of failed dials, which the core gives no ID
	mu          sync.Mutex
	events      []Event
	connections []control.Connection
	index       map[string]int
	pos         int
	ruleHits    map[string]*RuleHit

	// The connection poll's pace; see pollWait.
	viewed time.Time     // the last read of the connection records
	view   chan struct{} // wakes an idle poll for a new reader
}

// RuleHit counts the connections a rule set matched since the records were last
// cleared, beyond the recordLimit connections that are kept.
type RuleHit struct {
	Count int   `json:"count"`
	Last  int64 `json:"last"`
}

const ruleHitLimit = 1024

func NewRecords() *Records {
	return &Records{index: map[string]int{}, ruleHits: map[string]*RuleHit{}, view: make(chan struct{}, 1)}
}

// Each connection poll parses the core's full snapshot. It runs every second only while
// someone reads the records (the records pages poll every 3.5 s, the overview's routes every
// 2.5 s), otherwise every five: connections shorter than that may then go unrecorded and
// uncounted in the rule hits, and a close is noted up to five seconds late. Failed dials
// come from the core log and are unaffected.
const (
	viewedPoll = time.Second
	idlePoll   = 5 * time.Second
	viewWindow = 10 * time.Second
)

// Viewed notes a read of the connection records. A reader arriving while the poll is idle
// wakes it at once.
func (r *Records) Viewed() {
	r.mu.Lock()
	idle := time.Since(r.viewed) > viewWindow
	r.viewed = time.Now()
	r.mu.Unlock()
	if idle {
		select {
		case r.view <- struct{}{}:
		default:
		}
	}
}

// pollWait waits until the next connection snapshot is due.
func (r *Records) pollWait(ctx context.Context) error {
	r.mu.Lock()
	d := idlePoll
	if time.Since(r.viewed) <= viewWindow {
		d = viewedPoll
	}
	r.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	case <-r.view:
	}
	return nil
}

// RuleHits returns the per-rule-set connection counts.
func (r *Records) RuleHits() map[string]RuleHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]RuleHit, len(r.ruleHits))
	for k, v := range r.ruleHits {
		out[k] = *v
	}
	return out
}
func (r *Records) Log(source, level, message string) { r.LogDetail(source, level, message, "") }

// detailVariants is how many entries of one core or DNS reason a minute keeps apart by their
// detail. Further details fold into one entry without a detail, marked Folded, so that a cause
// the core reports with a different detail each time cannot fill the log.
const detailVariants = 4

// LogDetail records an event with a redacted operation shown under it.
func (r *Records) LogDetail(source, level, message, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixMilli()
	// Core and DNS repeats within a minute fold into one recent entry, which moves to the end.
	folded := false
	if source == "core" || source == "dns" {
		for {
			variants := 0
			for i := len(r.events) - 1; i >= 0 && i >= len(r.events)-32; i-- {
				e := r.events[i]
				if e.Source != source || e.Level != level || e.Message != message || now < e.Time || now-e.Time > 60000 {
					continue
				}
				if e.Detail != detail {
					variants++
					continue
				}
				e.Count = max(e.Count, 1) + 1
				e.Time = now
				e.Folded = e.Folded || folded
				copy(r.events[i:], r.events[i+1:])
				r.events[len(r.events)-1] = e
				return
			}
			if detail == "" || variants < detailVariants {
				break
			}
			detail, folded = "", true
		}
	}
	if len(r.events) == recordLimit {
		copy(r.events, r.events[1:])
		r.events = r.events[:recordLimit-1]
	}
	r.events = append(r.events, Event{Time: now, Source: source, Level: level, Message: message, Detail: detail, Folded: folded})
}

// Core stderr is free form, and can contain signed URLs or full config values.
// Keep a bounded event: a reason, and at most the redacted plain words of the line
// (deniedOperation, coreDetail), never its text as written.
type logSink struct {
	r        *Records
	line     []byte
	terminal string // read after the process and its stderr reader have exited
	crashed  bool   // the Go runtime began a crash report (crashHeader); the process ends with it
}

// os/exec can deliver partial lines and multiple lines in one write. Keep a
// bounded prefix while waiting for the delimiter, even for malformed output.
func (s *logSink) Write(p []byte) (int, error) {
	count := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		n := end
		if n < 0 {
			n = len(p)
		}
		s.line = append(s.line, p[:min(n, 4096-len(s.line))]...)
		if end < 0 {
			break
		}
		s.flush()
		p = p[end+1:]
	}
	return count, nil
}
func (s *logSink) flush() {
	if line := strings.TrimSpace(string(s.line)); line != "" {
		s.recordLine(line)
	}
	s.line = s.line[:0]
}

// Mihomo may color its log headers even when stderr is a pipe.
var logColor = regexp.MustCompile("\x1b\\[[0-9;]*m")

// A Go crash report starts with a line of its own, without a log header: an unrecovered panic, a
// fatal runtime error or a signal (SIGSEGV: segmentation violation, SIGABRT: abort). The runtime
// then writes the goroutines' stacks until the process ends: their headers, frames (a function and
// its arguments), file positions, the signal and the registers. crashDump knows those lines
// without the report's first line too.
var (
	crashHeader = regexp.MustCompile(`^(panic: |fatal error: |sig[a-z]+: )`)
	crashDump   = regexp.MustCompile(`^(goroutine \d+ |created by |runtime stack:|\[signal |\.\.\.additional frames elided|pc=0x|[a-z0-9]{2,6}\s+0x[0-9a-f]+$|\S+\.go:\d+( |$)|[a-z0-9_./*()\[\],-]+\(.*\)$)`)
)

// corePermissionReason names what the core was denied. The core runs as root with every
// capability (coreCommand), so a denial comes from the firewall or a kernel setting (such as
// ping_group_range for ICMP sockets), not from file ownership or a missing capability.
func corePermissionReason(line string) string {
	switch {
	case strings.Contains(line, "ping_group_range"), strings.Contains(line, "listen ip4:icmp"), strings.Contains(line, "listen ip6:ipv6-icmp"), strings.Contains(line, "icmp socket"):
		return "core_icmp_socket_permission"
	case strings.Contains(line, "listenlocalconn"):
		// TProxy answers UDP from a socket bound to the original destination, often a port below 1024.
		return "core_tproxy_udp_reply_permission"
	case strings.Contains(line, "ip_transparent"), strings.Contains(line, "transparent socket"), strings.Contains(line, "set transparent"), strings.Contains(line, "ipv6_transparent"):
		return "core_tproxy_socket_permission"
	case strings.Contains(line, "so_mark"), strings.Contains(line, "set routing mark"):
		return "core_route_mark_permission"
	case strings.Contains(line, "listen tcp"), strings.Contains(line, "listen udp"), strings.Contains(line, "bind:"):
		return "core_listener_permission"
	case strings.Contains(line, "pthread_create"), strings.Contains(line, "failed to create new os thread"):
		return "core_thread_creation_failed"
	case strings.Contains(line, "/proc/"), strings.Contains(line, "find process"), strings.Contains(line, "process name"):
		return "core_process_lookup_permission"
	case strings.Contains(line, "connect:"), strings.Contains(line, "sendto"), strings.Contains(line, "sendmsg"),
		strings.Contains(line, "dial tcp"), strings.Contains(line, "dial udp"), strings.Contains(line, "write udp"), strings.Contains(line, "write tcp"):
		return "core_outbound_blocked"
	case strings.Contains(line, "open /"), strings.Contains(line, "stat /"), strings.Contains(line, "mkdir /"), strings.Contains(line, "rename /"):
		return "core_file_permission"
	default:
		return "permission_denied"
	}
}

var connectionNoise = map[string]bool{"connection_refused": true, "connection_reset": true, "connection_closed": true,
	"io_timeout": true, "deadline_exceeded": true, "dial_failed": true, "handshake_failed": true}

// ordinary marks core warnings that normal operation produces all the time (OpenClash shows
// them only in its raw core log): one connection or packet failing, which the core retries
// and reports up to ten times per connection, a group starting its health check after
// repeated failures (node failover follows up on those) and the core stopping. Failed dials
// become connection records; the rest are dropped.
var ordinary = []string{"[tcp] dial ", "[udp] dial ", "[udp] resolve ip error", "[udp] destination ip not valid",
	"[udp] dosniff error", "[udp] parse metadata failed", "[metadata] ", "[icmp] failed to connect", "isn't a *net.udpaddr",
	"nil *net.udpaddr", "activate health check", "mihomo shutting down",
	// The core notes at every load that a classical rule set is read only for its domain rules
	// in dns.fake-ip-filter, or only for its domain or IP rules in a DNS policy or sniffer list.
	// The plugin causes the first on purpose: with the China shortcut the fake-ip filter lists
	// the sets of the rules that route by name (Compile), and the built-in template alone puts
	// 25 classical ones there.
	"only matching domain rules in fake-ip-filter", "only matching it contain ip rule", "only matching it contain domain rule",
	// A health check over http:// whose test address does not answer a second request on the
	// same connection (some providers hijack test addresses). The first answer already passed
	// the node, and its delay is what the core reports; the core then advises https for test
	// addresses ("... Due to some proxy providers hijacking test addresses ..."). One pair per
	// node at every check, and nothing failed.
	"failed to get the second response from", "it is recommended to use https for provider.health-check.url"}

func (s *logSink) recordLine(line string) {
	if strings.Contains(line, "\x1b") {
		line = logColor.ReplaceAllString(line, "")
	}
	original := line
	line = strings.ToLower(line)
	// A crash report's first line is logged; the stack dump after it is not. Its frames and file
	// names hold words the reasons below look for (dns, sniffer, geodata, dial).
	if s.crashed && !strings.Contains(line, "level=") || crashDump.MatchString(line) {
		return
	}
	crash := crashHeader.MatchString(line)
	s.crashed = s.crashed || crash
	level := "warn"
	fatal := crash
	// The runtime's own words before a crash (runtime: program exceeds 10000-thread limit).
	if crash || strings.HasPrefix(line, "runtime: ") || strings.HasPrefix(line, "runtime/cgo: ") {
		level = "error"
	}
	// Only a leading log level (optionally after a timestamp) can classify a
	// process exit. Free-form DNS names, server names and paths stay private.
	for i, field := range strings.Fields(line) {
		if i > 3 {
			break
		}
		field = strings.TrimPrefix(field, "level=")
		field, _, _ = strings.Cut(field, "[")
		switch strings.Trim(field, "[]:") {
		case "fatal", "panic":
			level, fatal = "error", true
		case "error":
			level = "error"
		case "warn", "warning":
			level = "warn"
		case "info", "debug", "trace":
			return
		default:
			continue
		}
		break
	}
	denied := strings.Contains(line, "permission denied") || strings.Contains(line, "operation not permitted")
	// Out of descriptors (EMFILE for the core, or ENFILE for the whole system): the core can
	// neither accept nor open connections, which browsers report as a connection closed
	// unexpectedly. A failed dial still becomes a connection record, and the cause is logged.
	exhausted := strings.Contains(line, "too many open files")
	if !fatal && !denied && slices.ContainsFunc(ordinary, func(n string) bool { return strings.Contains(line, n) }) {
		if c, ok := control.FailedDial(original, time.Now().UnixMilli()); ok {
			s.r.Failed(c)
		}
		if !exhausted {
			return
		}
	}
	source := "core"
	if strings.Contains(line, "dns:") || strings.Contains(line, "dns/") {
		source = "dns"
	}
	reason := "core_diagnostic"
	if denied {
		reason = corePermissionReason(line)
	} else if exhausted {
		reason = "core_file_limit_reached"
	} else {
		// The provider and rule set needles are the forms mihomo writes ("initial proxy provider
		// %s error", "initial rule provider %s error", "[Provider] %s pull error", a rule set's
		// bad entry, a rule-set reference), not a word that a longer phrase may hold. They come
		// first: they say what failed, and the error words after them how. The plugin's
		// configurations carry no proxy providers (the subscription helper expands them into
		// nodes), so the fetcher's "[Provider] <name>" lines name rule sets. Running out of memory
		// (the runtime's "fatal error: runtime: out of memory", or ENOMEM) comes before them all.
		for _, x := range []struct{ needle, reason string }{{"out of memory", "core_out_of_memory"}, {"cannot allocate memory", "core_out_of_memory"},
			{"initial proxy provider ", "node_provider_issue"}, {"parse proxy provider ", "node_provider_issue"},
			{"initial rule provider ", "rule_provider_issue"}, {"[provider] ", "rule_provider_issue"}, {"from rule provider:", "rule_provider_issue"}, {"rule provider type error", "rule_provider_issue"},
			{"parse classical rule [", "rule_provider_issue"}, {"invalid ipcidr:[", "rule_provider_issue"}, {"rule-set", "rule_provider_issue"}, {"rule set [", "rule_provider_issue"},
			{"address already in use", "listen_address_in_use"}, {"connection refused", "connection_refused"}, {"network is unreachable", "network_unreachable"}, {"i/o timeout", "io_timeout"}, {"context deadline exceeded", "deadline_exceeded"}, {"no such host", "dns_name_not_found"}, {"certificate", "tls_certificate_error"}, {"handshake", "handshake_failed"}, {"no space left", "disk_full"}, {"invalid config", "invalid_configuration"}, {"failed to create new os thread", "core_thread_creation_failed"}, {"pthread_create failed", "core_thread_creation_failed"}, {"thread exhaustion", "core_thread_creation_failed"}, {"-thread limit", "core_thread_creation_failed"},
			{"no route to host", "network_unreachable"}, {"connection reset", "connection_reset"}, {"broken pipe", "connection_reset"}, {"unexpected eof", "connection_closed"}, {": eof", "connection_closed"},
			{"geoip", "geodata_unavailable"}, {"geosite", "geodata_unavailable"}, {"mmdb", "geodata_unavailable"},
			// What failed to start: a listener, the TUN device, the configuration. Their lines hold
			// the words below ("Start DNS server", "DNS NameServer[0]", "dialer-proxy").
			{"start dns server", "core_listener_failed"}, {"listen err", "core_listener_failed"}, {"server error: listen ", "core_listener_failed"}, {"listener: listen ", "core_listener_failed"},
			{"start tun listening", "core_tun_issue"}, {"parse config error", "invalid_configuration"}, {"dialer-proxy", "invalid_configuration"},
			{"sniff", "sniffer_issue"}, {"[tun]", "core_tun_issue"}, {"tun device", "core_tun_issue"}, {"/dev/net/tun", "core_tun_issue"}, {"dial ", "dial_failed"}, {"dns", "dns_query_failed"}} {
			if strings.Contains(line, x.needle) {
				reason = x.reason
				break
			}
		}
	}
	// A single connection failing at the target is ordinary traffic; it matters only when the
	// core itself is going down.
	if !fatal && connectionNoise[reason] {
		return
	}
	if fatal && reason != "core_diagnostic" {
		s.terminal = reason
	}
	detail := ""
	switch reason {
	case "permission_denied":
		detail = deniedOperation(original)
	case "core_diagnostic", "core_listener_failed", "invalid_configuration":
		// Which listener, or what in the configuration.
		detail = coreDetail(original)
	}
	s.r.LogDetail(source, level, reason, detail)
}

// What the log keeps of a core line goes through redact. Each pattern replaces what it finds with
// a placeholder (or a space), never by deleting it into the words around it.
var (
	// Literal escapes in a quoted message; a newline in one reads as a space.
	detailEscapes = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, " ", `\r`, " ", `\t`, " ")
	// A run of other than plain ASCII (a name in Chinese, an emoji) reads as a punycode label
	// (punycode), so that a name with such a label (airport.中国, 机场.example.com) is a host name;
	// what remains of them is left out at the end (detailPunycode).
	detailNonASCII = regexp.MustCompile(`[^\x00-\x7f]+`)
	detailPunycode = regexp.MustCompile(`(?i)\bxn--[a-z0-9-]*`)

	// The log header before a message: a logrus time, or the standard log's date and time, and a
	// level with any timer after it.
	detailHeader  = regexp.MustCompile(`(?i)^(time="[^"]*"\s+|\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(\.\d+)?\s+)?((level=)?(fatal error|fatal|panic|error|warn|warning)(\[[^\]]*\])?:?\s+)?`)
	detailMessage = regexp.MustCompile(`msg=(?:"((?:[^"\\]|\\.)*)"|([^\s"]+))`)

	// Links (with any credentials, query or signature in them) keep their scheme.
	detailURL = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s"]*[^\s"':,.)\]]`)
	// The value of a setting whose name says it is secret: token=, private_key=, password: ...
	detailSecret = regexp.MustCompile(`(?i)((?:^|[^a-z0-9])(?:[a-z0-9]+[_-])*(?:token|key|password|passwd|secret|pass|auth|sig|uuid|sid|short-id|psk|passphrase|pubkey|publickey|privatekey|cookie|authorization)"?\s*[=:]\s*"?)(?:(?:bearer|basic)\s+)?[^\s"',;&)\]]+`)
	// A query, up to the end of its word.
	detailQuery = regexp.MustCompile(`\?[^\s"']*`)
	// A user (and password) before @, which marks the name after it (detailAfter).
	detailUser = regexp.MustCompile(`[^\s@"'<>]+@`)
	// The IDs the plugin names nodes and groups by.
	detailID = regexp.MustCompile(`(?i)\b(node|group):[0-9a-f]{32}\b`)

	// Addresses: an IPv6 one in brackets (with a zone and port), a run of hex digits, colons and
	// dots not inside a word that netip reads as an IPv6 address (ipv6Address), IPv4 with a port,
	// and MAC addresses.
	detailBracket = regexp.MustCompile(`(?i)\[[0-9a-f.]*:[0-9a-f:.]*(%[^\]\s]*)?\](:\d+)?`)
	detailIPv6    = regexp.MustCompile(`(?i)(^|[^0-9a-z_.])[0-9a-f:.]*:[0-9a-f.]*:[0-9a-f:.]*(%[0-9a-z_.-]+)?`)
	detailIPv4    = regexp.MustCompile(`\d{1,3}(\.\d{1,3}){3}(:\d+)?`)
	detailMAC     = regexp.MustCompile(`(?i)\b[0-9a-f]{2}([:-][0-9a-f]{2}){5}\b`)

	// Host names: two labels or more whose last starts with a letter and has two characters or
	// more (example.com, host.lan2, myvps.v2), or is punycode, with any port. File names and
	// dotted settings (cache.db, dns.fake-ip-filter) go with them.
	detailHost = regexp.MustCompile(`(?i)\b[a-z0-9_-]+(\.[a-z0-9_-]+)*\.(xn--[a-z0-9-]+|[a-z][a-z0-9_-]*[a-z0-9])(:\d{1,5})?\b`)
	// A name of one label, where nothing else tells it from a word: with a port (my-nas:53), the
	// fixed localhost, after an arrow (src --> my-nas), a lookup or a user@, or a name with a
	// hyphen or underscore after dial or to.
	detailNamePort  = regexp.MustCompile(`(?i)\b[a-z][a-z0-9_-]*:\d{1,5}\b`)
	detailLocalhost = regexp.MustCompile(`(?i)\blocalhost\b`)
	detailAfter     = regexp.MustCompile(`(?i)(--?>|\blookup|\x01)\s*[a-z0-9_][a-z0-9_.%-]*`)
	detailDialTo    = regexp.MustCompile(`(?i)\b(dial|to)\s+[a-z][a-z0-9]*([_-][a-z0-9]+)+\b`)

	// A path after a host or address, deep paths (two directories are kept), and Windows paths.
	detailHostPath = regexp.MustCompile(`(<host>|<addr>)/[^\s"']*`)
	detailPath     = regexp.MustCompile(`(/[^/\s:]+/[^/\s:]+)/[^\s:]+`)
	detailWinPath  = regexp.MustCompile(`(?i)\b[a-z]:\\[^\s"']*`)
	// Runs of key, ID and digest characters; secretLike tells them from words and paths.
	detailToken = regexp.MustCompile(`[A-Za-z0-9_+/=-]{16,}`)

	detailSafe = regexp.MustCompile(`[^a-z0-9 _./:<>-]`)
)

// coreMessage returns a core line's message: the quoted or bare msg= value of a logrus line, or
// the line without its header, with its escapes read.
func coreMessage(line string) string {
	msg := detailHeader.ReplaceAllString(line, "")
	if m := detailMessage.FindStringSubmatch(line); m != nil {
		msg = m[1] + m[2]
	}
	return detailEscapes.Replace(msg)
}

// redact takes out of core words what could identify the network or the subscription: links,
// secret settings, queries, users, node and group IDs, addresses, host names (and the paths after
// them), deep paths and tokens; and leaves plain ASCII in lower case, so that names a
// subscription chose (rule set tags, often Chinese or with emoji) are left out unless they are
// plain words.
func redact(msg string) string {
	msg = punycode(msg)
	msg = detailURL.ReplaceAllString(msg, "$1<url>")
	msg = detailSecret.ReplaceAllString(msg, "${1}<secret>")
	msg = detailQuery.ReplaceAllString(msg, " ")
	msg = detailUser.ReplaceAllString(msg, "\x01")
	msg = detailID.ReplaceAllString(msg, "<$1>")
	msg = detailBracket.ReplaceAllString(msg, "<addr>")
	msg = detailIPv6.ReplaceAllStringFunc(msg, ipv6Address)
	msg = detailIPv4.ReplaceAllString(msg, "<addr>")
	msg = detailMAC.ReplaceAllString(msg, "<addr>")
	msg = detailHost.ReplaceAllString(msg, "<host>")
	msg = detailNamePort.ReplaceAllString(msg, "<host>")
	msg = detailLocalhost.ReplaceAllString(msg, "<host>")
	msg = detailAfter.ReplaceAllString(msg, "$1 <host>")
	msg = detailDialTo.ReplaceAllString(msg, "$1 <host>")
	msg = detailHostPath.ReplaceAllString(msg, "$1")
	msg = detailPath.ReplaceAllString(msg, "$1/...")
	msg = detailWinPath.ReplaceAllString(msg, "<path>")
	msg = detailToken.ReplaceAllStringFunc(msg, func(s string) string {
		if secretLike(s) {
			return "<id>"
		}
		return s
	})
	msg = detailPunycode.ReplaceAllString(msg, " ")
	return strings.Join(strings.Fields(detailSafe.ReplaceAllString(strings.ToLower(msg), " ")), " ")
}

// secretLike tells a key, ID or digest from words, settings and paths: base64 signs ('+', or
// '=' padding), or a part (between '/', '=', '-' and '_') of hex digits of 12 or more with a
// digit (digests, REALITY short IDs, the end of a UUID), of 8 or more with two separate runs of
// digits (random letters and digits), or of 16 or more letters changing case at least every
// third (random letters). Algorithm names (chacha20-ietf-poly1305) and CamelCase identifiers
// stay.
func secretLike(run string) bool {
	trimmed := strings.TrimRight(run, "=")
	if strings.Contains(run, "+") || len(trimmed) < len(run) && len(trimmed) >= 12 {
		return true
	}
	for _, part := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '=' || r == '-' || r == '_' }) {
		hex, digits, runs, changes := true, 0, 0, 0
		for i := 0; i < len(part); i++ {
			c := part[i]
			hex = hex && (isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')
			if isDigit(c) {
				digits++
				if i == 0 || !isDigit(part[i-1]) {
					runs++
				}
			} else if i > 0 && !isDigit(part[i-1]) && isUpper(c) != isUpper(part[i-1]) {
				changes++
			}
		}
		if hex && digits > 0 && len(part) >= 12 || runs >= 2 && digits < len(part) && len(part) >= 8 || len(part) >= 16 && changes*3 >= len(part) {
			return true
		}
	}
	return false
}

// punycode replaces each run of other than plain ASCII with the label xn--0, apart from the
// letters and digits next to it: 规则provider reads xn--0 provider.
func punycode(msg string) string {
	runs := detailNonASCII.FindAllStringIndex(msg, -1)
	if runs == nil {
		return msg
	}
	var b strings.Builder
	last := 0
	for _, r := range runs {
		b.WriteString(msg[last:r[0]])
		if r[0] > 0 && isAlnum(msg[r[0]-1]) {
			b.WriteByte(' ')
		}
		b.WriteString("xn--0")
		if r[1] < len(msg) && isAlnum(msg[r[1]]) {
			b.WriteByte(' ')
		}
		last = r[1]
	}
	b.WriteString(msg[last:])
	return b.String()
}

// ipv6Address replaces the IPv6 address in what detailIPv6 found: after the character before it,
// if any, and before a colon or dot that ends a sentence.
func ipv6Address(s string) string {
	for i := range min(2, len(s)) {
		for _, a := range []string{s[i:], strings.TrimRight(s[i:], ".:")} {
			if _, err := netip.ParseAddr(a); err == nil {
				return s[:i] + "<addr>" + s[i+len(a):]
			}
		}
	}
	return s
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isDigit(c) || isUpper(c) || c >= 'a' && c <= 'z' }

// capDetail bounds a redacted detail at n bytes (plain ASCII).
func capDetail(s string, n int) string {
	if len(s) > n {
		s = strings.TrimSpace(s[:n])
	}
	return s
}

// coreDetail keeps, for the log, what a core line that no reason describes says: its message
// without the log header, redacted, at most 120 characters.
func coreDetail(line string) string {
	return capDetail(redact(coreMessage(line)), 120)
}

var deniedWords = regexp.MustCompile(`(?i)permission denied|operation not permitted`)

// deniedOperation keeps the failing operation of a core permission error, such as
// "listen udp <addr>: bind" or "open /etc/hyper/...": the last three steps of its message
// before the denial, redacted as coreDetail redacts, at most 96 characters.
func deniedOperation(line string) string {
	msg := coreMessage(line)
	at := deniedWords.FindStringIndex(msg)
	if at == nil {
		return ""
	}
	parts := strings.Split(strings.TrimRight(msg[:at[0]], " :"), ": ")
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	return capDetail(redact(strings.Join(parts, ": ")), 96)
}

func (r *Records) Observe(c control.Connection) {
	c = sliceConnection(c)
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Created != 0 && c.Created <= r.minTime {
		return
	}
	if idx, ok := r.index[c.ID]; ok {
		old := r.connections[idx]
		if c.Created != 0 {
			old = c
		}
		if c.Closed != 0 {
			old.Closed = c.Closed
		}
		if c.Result == "traffic_observed" {
			old.Result = c.Result
		}
		r.connections[idx] = old
		return
	}
	if c.Created == 0 && c.Destination == "" {
		return
	}
	r.insertLocked(c)
}

// Record turns the connection records on or off with the running policy.
func (r *Records) Record(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording = on
}

// Failed keeps a connection the core could not open. A device retrying the same destination
// through the same outbound within a minute updates its last record instead of adding more.
func (r *Records) Failed(c control.Connection) {
	c = sliceConnection(c)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recording {
		return
	}
	for k := range min(32, len(r.connections)) {
		j := (r.pos + len(r.connections) - 1 - k) % len(r.connections)
		old := &r.connections[j]
		if old.Result == "failed" && old.Source == c.Source && old.Destination == c.Destination && old.Outbound == c.Outbound && c.Created-old.Created <= 60000 {
			old.Closed = c.Closed
			return
		}
	}
	r.failures++
	c.ID = "failed:" + strconv.FormatUint(r.failures, 10)
	r.insertLocked(c)
}

func (r *Records) insertLocked(c control.Connection) {
	if set, ok := strings.CutPrefix(c.Rule, "RuleSet,"); ok && set != "" {
		hit := r.ruleHits[set]
		if hit == nil && len(r.ruleHits) < ruleHitLimit {
			hit = &RuleHit{}
			r.ruleHits[set] = hit
		}
		if hit != nil {
			hit.Count++
			hit.Last = max(hit.Last, c.Created)
		}
	}
	if len(r.connections) < recordLimit {
		r.index[c.ID] = len(r.connections)
		r.connections = append(r.connections, c)
	} else {
		delete(r.index, r.connections[r.pos].ID)
		r.index[c.ID] = r.pos
		r.connections[r.pos] = c
		r.pos = (r.pos + 1) % recordLimit
	}
}
func (r *Records) Page(kind string, offset, limit int) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit = min(max(limit, 1), 100)
	offset = max(offset, 0)
	if kind == "logs" {
		out := []Event{}
		for j := len(r.events) - 1 - offset; j >= 0 && len(out) < limit; j-- {
			out = append(out, r.events[j])
		}
		return out
	}
	out := []control.Connection{}
	for j := len(r.connections) - 1 - offset; j >= 0 && len(out) < limit; j-- {
		out = append(out, r.connections[(j+r.pos)%len(r.connections)])
	}
	return out
}

// CoreStarted scopes the overview's routes to the core that just started. A different
// subscription also drops the rule set hit counts: its sets are other lists even when
// a name repeats. The connection history stays.
func (r *Records) CoreStarted(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeSince = time.Now().UnixMilli()
	if source != r.source {
		r.ruleHits = map[string]*RuleHit{}
		r.source = source
	}
}

func (r *Records) Clear(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "logs" {
		r.events = nil
	} else {
		r.minTime = time.Now().UnixMilli()
		r.connections = nil
		r.index = map[string]int{}
		r.ruleHits = map[string]*RuleHit{}
		r.pos = 0
	}
}
