package router

import (
	"fmt"
	"strings"
	"testing"
)

// Lines a core started with a template-routed profile wrote (China shortcut on, fake-ip DNS,
// http:// test address that closes the connection after one answer), and the forms mihomo
// writes for providers and rule sets. "" means the line is dropped.
func TestCoreLogClassifiesMihomoLines(t *testing.T) {
	const at = `time="2026-11-22T09:51:42.596400247-08:00" `
	for _, c := range []struct{ line, reason, detail string }{
		// The classical sets the fake-ip filter lists on purpose, at every load.
		{at + `level=warning msg="Disney Plus provider is Classical, only matching domain rules in fake-ip-filter"`, "", ""},
		{at + `level=warning msg="YouTube (classical) provider is Classical, only matching domain rules in fake-ip-filter"`, "", ""},
		{at + `level=warning msg="规则 provider is Classical, only matching it contain domain rule"`, "", ""},
		{at + `level=warning msg="Steam provider is Classical, only matching it contain ip rule"`, "", ""},
		// A node that passed its first test request, and the advice paired with it, which says
		// "proxy providers" without one failing.
		{at + `level=error msg="node:7a52b59ae9ef9f8a902b78b674dde5eb failed to get the second response from http://127.0.0.1:28204/generate_204: Head \"http://127.0.0.1:28204/generate_204\": read tcp 127.0.0.1:36064->127.0.0.1:28290: use of closed network connection"`, "", ""},
		{at + `level=error msg="node:1fae3fcd39769a54d040013102625210 failed to get the second response from http://127.0.0.1:28204/generate_204: Head \"http://127.0.0.1:28204/generate_204\": EOF"`, "", ""},
		{at + `level=warning msg="It is recommended to use HTTPS for provider.health-check.url and group.url to ensure better reliability. Due to some proxy providers hijacking test addresses and not being compatible with repeated HEAD requests, using HTTP may result in failed tests."`, "", ""},
		{at + `level=warning msg="Mihomo shutting down"`, "", ""},
		// Providers and rule sets, by the forms mihomo writes, ahead of the error words.
		{at + `level=error msg="initial proxy provider airport error: open ./providers/airport.yaml: no such file or directory"`, "node_provider_issue", ""},
		{at + `level=error msg="initial rule provider Netflix error: open ./rulesets/0e167bcf6e445d42194a8ef64a0187c8a0581c1608071fbb351788bf2a81bf3d.mrs: no such file or directory"`, "rule_provider_issue", ""},
		{at + `level=error msg="initial rule provider geosite-cn error: invalid mrs file"`, "rule_provider_issue", ""},
		{at + `level=error msg="[Provider] Netflix pull error: dial tcp 203.0.113.9:443: connect: connection refused"`, "rule_provider_issue", ""},
		{at + `level=warning msg="parse classical rule [PROCESS-NAME,steam.exe] error: unsupported rule type on classical rule-set: PROCESS-NAME"`, "rule_provider_issue", ""},
		{at + `level=warning msg="skip invalid domain from rule provider: invalid domain \"a/b\""`, "rule_provider_issue", ""},
		// Anything else keeps a redacted detail.
		{at + `level=warning msg="[CacheFile] write cache to /etc/hyper/core/cache.db failed: timeout"`, "core_diagnostic", "cachefile write cache to /etc/hyper/... failed: timeout"},
		{at + `level=warning msg="[Rule] rematch proxy group:af60f450c0dd13e8eab38ef9bfaed433 failed to update metadata: 🇭🇰 香港 01 sub.example.com 10.0.0.1:53"`, "core_diagnostic", "rule rematch proxy <group> failed to update metadata: 01 <host> <addr>"},
		{`ERROR panic: runtime error: index out of range [3] with length 3`, "core_diagnostic", "panic: runtime error: index out of range 3 with length 3"},
	} {
		r := NewRecords()
		s := logSink{r: r}
		_, _ = s.Write([]byte(c.line + "\n"))
		logs := r.Logs(0, 10, "", "", "")
		if c.reason == "" {
			if len(logs) != 0 {
				t.Errorf("%s\n logged %+v", c.line, logs)
			}
			continue
		}
		if len(logs) != 1 || logs[0].Message != c.reason || logs[0].Detail != c.detail {
			t.Errorf("%s\n got %+v\nwant %s %q", c.line, logs, c.reason, c.detail)
		}
	}
}

func TestCoreDetailRedacts(t *testing.T) {
	for in, want := range map[string]string{
		// Links lose all but their scheme: credentials, host, path, query and signature.
		`level=error msg="download failed: get \"https://user:secret@sub.example.com/link?token=9f8e7d6c5b4a\": unexpected status 403"`: "download failed: get https://<url> : unexpected status 403",
		// Two-label names, user@ prefixes, IPv6 and IPv4 addresses with ports.
		`level=warning msg="sync with admin@example.com via [2001:db8::1]:443 and 192.0.2.1:53 failed"`: "sync with <host> via <addr> and <addr> failed",
		// Node IDs, deep paths and long tokens with digits; plain words of any length stay.
		`level=warning msg="node:0123456789abcdef0123456789abcdef read /usr/share/hyper/rules/x.mrs key 6b1f0e9a2c4d8e7f6a5b4c3d2e1f0a9b global-client-fingerprint"`: "<node> read /usr/share/... key <id> global-client-fingerprint",
		// Nothing but plain ASCII: a name in Chinese or with emoji is left out, its plain words stay.
		`level=warning msg="🇯🇵 日本 provider failed"`: "provider failed",
		`warn[0000] something odd happened`:         "something odd happened",
	} {
		if got := coreDetail(strings.ToLower(in)); got != want {
			t.Errorf("%s\n got %q\nwant %q", in, got, want)
		}
	}
	long := coreDetail(`level=warning msg="` + strings.Repeat("word ", 60) + `"`)
	if len(long) > 120 || !strings.HasPrefix(long, "word word") || strings.HasSuffix(long, " ") {
		t.Errorf("not capped: %q", long)
	}
}

// One cause reported with a different detail each time keeps a few entries, then folds into
// one without a detail; repeats of one detail fold as before.
func TestCoreLogFoldsVaryingDetails(t *testing.T) {
	r := NewRecords()
	s := logSink{r: r}
	for i := range 20 {
		_, _ = fmt.Fprintf(&s, "level=warning msg=\"[CacheFile] skip storage for key k%d: payload exceeds %d bytes\"\n", i, 1000+i)
	}
	for range 3 {
		_, _ = s.Write([]byte(`level=warning msg="[CacheFile] skip storage for key k0: payload exceeds 1000 bytes"` + "\n"))
	}
	logs := r.Logs(0, 100, "", "", "")
	total, plain := 0, 0
	for _, e := range logs {
		if e.Message != "core_diagnostic" {
			t.Fatal(e)
		}
		total += max(e.Count, 1)
		if e.Detail == "" {
			plain++
		}
		if e.Folded != (e.Detail == "") {
			t.Errorf("only the entry without a detail is folded: %+v", e)
		}
	}
	if len(logs) != detailVariants+1 || plain != 1 || total != 23 {
		t.Fatalf("%d entries, %d without a detail, %d counted: %+v", len(logs), plain, total, logs)
	}
	if e := logs[0]; e.Detail != "cachefile skip storage for key k0: payload exceeds 1000 bytes" || e.Count != 4 {
		t.Fatalf("repeats of one detail did not fold: %+v", e)
	}
}

// Words of real core lines that identify the network or the subscription, as the core writes
// them (recordLine hands coreDetail the line in its own case).
func TestCoreDetailRedactsIdentifyingWords(t *testing.T) {
	const at = `time="2026-11-22T09:51:42.596400247-08:00" level=warning msg=`
	for _, c := range []struct{ in, want string }{
		// IPv6 in all its forms, IPv4 inside a word, MAC addresses; a time is not an address.
		{`x failed to reach 2408:8256:abcd:: now`, "x failed to reach <addr> now"},
		{`x failed to reach 2001:db8:: now`, "x failed to reach <addr> now"},
		{`prefix 2408:8256:3a1f::/48 bad`, "prefix <addr> bad"},
		{`x 64:ff9b::192.0.2.1 y ::ffff:192.0.2.1 z`, "x <addr> y <addr> z"},
		{`x [fe80::1%br-lan]:53 y fe80::1%br-lan z`, "x <addr> y <addr> z"},
		{`x 2001:DB8:0:0:8:800:200C:417A y 240e:0388:6a10:2b00:4d3c:1a2b:9f00:0001`, "x <addr> y <addr>"},
		{`x ::1 and :: and 1::`, "x <addr> and <addr> and <addr>"},
		{`ipnode_192.168.1.20 failed src=192.168.1.20:5353`, "ipnode_<addr> failed src <addr>"},
		{`src:2001:db8::1 mac aa:bb:cc:dd:ee:ff at 10:00:00`, "src:<addr> mac <addr> at 10:00:00"},
		// Host names: underscores, a last label ending in a digit, punycode and names in Chinese,
		// ports, and names of one label where the line says they are one.
		{`x my_host.example.com y`, "x <host> y"},
		{`x sub.xn--fiqs8s y xn--fiqs8s.xn--55qx5d`, "x <host> y <host>"},
		{`x airport.中国 y mysub.公司 z 机场.example.com`, "x <host> y <host> z <host>"},
		{`x example.com1 y host.lan2 z myvps.v2`, "x <host> y <host> z <host>"},
		{`x Sub.Example.COM:8443 y`, "x <host> y"},
		{`[UDP] 192.168.1.20:5353 --> my-nas error`, "udp <addr> --> <host> error"},
		{`dial to my-nas failed: lookup nas on localhost`, "dial to <host> failed: lookup <host> on <host>"},
		{`x my-nas:53 y`, "x <host> y"},
		// Links, queries, paths after a host or address, users and passwords.
		{`x get \"https://user:p@ss@sub.example.com/x\" y`, "x get https://<url> y"},
		{`x sub.example.com/s/AbCdEfGhIjK y`, "x <host> y"},
		{`x 1.2.3.4:443/api/v1/client/subscribe?token=abcdefghijklmnopqr y`, "x <addr> y"},
		{`x example.com/link?token=AbC9xyz&flag=1 y`, "x <host> y"},
		{`x user:secretpw@1.2.3.4 y user:sec/ret@host.example.com z alice@host`, "x <addr> y <host> z <host>"},
		{`x 'C:\\Users\\alice\\secret.txt' y`, "x <path> y"},
		// Secrets: by the name of their setting, and keys, IDs and digests by their characters.
		{`x token=Zm9vYmFy/YmF6cXV4+cXV1eA== y`, "x token <secret> y"},
		{`x password: hunter2 uuid b831381d-6324-4d53-ad4f-8cda48b30811 y`, "x password: <secret> uuid <id> y"},
		{`set private_key=0123456789abcdef; Authorization: Bearer abc.def`, "set private_key <secret> authorization: <secret>"},
		{`x short-id 0123456789abcdef y`, "x short-id <id> y"},
		{`x pubkey 4pU5i_Gk_Sm3cXjXiCMyY8UyHZsQ9-eOzN1pF0w3V1M y`, "x pubkey <id> y"},
		{`x pubkey AbCdEfGhIjKlMnOpQrStUv y Zm9vYmFy/YmF6cXV4cXV1eA z`, "x pubkey <id> y <id> z"},
		// Words, algorithm names and identifiers stay.
		{`cipher chacha20-ietf-poly1305 2022-blake3-aes-128-gcm global-client-fingerprint UpdateServerAddr Http2Server`,
			"cipher chacha20-ietf-poly1305 2022-blake3-aes-128-gcm global-client-fingerprint updateserveraddr http2server"},
		{`规则provider is Classical`, "provider is classical"},
		// Escapes read as spaces.
		{`first line\nsecond line sub.example.com\tthird`, "first line second line <host> third"},
	} {
		if got := coreDetail(at + `"` + c.in + `"`); got != c.want {
			t.Errorf("%s\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// The header: the standard log's date and time, a bare msg, and fields after msg.
	for in, want := range map[string]string{
		`2026/11/22 10:00:00 sub.example.com quic failed`:    "<host> quic failed",
		at + `bare.example.com`:                              "<host>",
		at + `"x" extra="secret.example.com"`:                "x",
		`WARN[0000] something at sub.example.com`:            "something at <host>",
		`level=warning msg="unterminated sub.example.com`:    "msg unterminated <host>",
		`fatal error: concurrent map writes`:                 "concurrent map writes",
		`ERROR panic: runtime error: index out of range [3]`: "panic: runtime error: index out of range 3",
	} {
		if got := coreDetail(in); got != want {
			t.Errorf("%s\n got %q\nwant %q", in, got, want)
		}
	}
}

// A permission error keeps its operation, redacted as coreDetail redacts: the values of secret
// settings, links, users and one-label names go too.
func TestDeniedOperationRedacts(t *testing.T) {
	const at = `time="2026-11-22T09:51:42.596400247-08:00" `
	for _, c := range []struct{ line, reason, detail string }{
		{at + `level=error msg="initial rule provider X error: Get \"https://sub.example.com/x?token=s3cr3tt0k3n\": permission denied"`,
			"permission_denied", "initial rule provider x error: get https://<url>"},
		{at + `level=error msg="[Provider] X pull error: read https://alice:pw@sub.example.com/x?token=s3cr3tt0k3n: permission denied"`,
			"permission_denied", "provider x pull error: read https://<url>"},
		{at + `level=warning msg="[UDP] dial node:7a52b59ae9ef9f8a902b78b674dde5eb 192.168.1.20:5353 --> my-nas:53 error: permission denied"`,
			"permission_denied", "udp dial <node> <addr> --> <host> error"},
		{at + `level=warning msg="exec /usr/bin/ssh-agent alice@host: permission denied"`,
			"permission_denied", "exec /usr/bin/... <host>"},
		{at + `level=error msg="Start TUN listening error: configure tun interface: operation not permitted"`,
			"permission_denied", "start tun listening error: configure tun interface"},
		{at + `level=warning msg="[WG](node:7a52b59ae9ef9f8a902b78b674dde5eb)UpdateServerAddr failed to update wireguard ipc conf: set private_key=0123456789abcdef: operation not permitted"`,
			"permission_denied", "wg <node> updateserveraddr failed to update wireguard ipc conf: set private_key <secret>"},
		{at + `level=warning msg="[TCP] dial DIRECT 192.168.1.20:5353 --> nas.lan:445 error: dial tcp 192.168.1.5:445: connect: permission denied"`,
			"core_outbound_blocked", ""},
	} {
		r := NewRecords()
		s := logSink{r: r}
		_, _ = s.Write([]byte(c.line + "\n"))
		logs := r.Logs(0, 10, "", "", "")
		if len(logs) != 1 || logs[0].Message != c.reason || logs[0].Detail != c.detail {
			t.Errorf("%s\n got %+v\nwant %s %q", c.line, logs, c.reason, c.detail)
		}
	}
	long := deniedOperation(`level=error msg="` + strings.Repeat("step ", 30) + `: permission denied"`)
	if len(long) > 96 || !strings.HasPrefix(long, "step step") {
		t.Errorf("not capped: %q", long)
	}
}

// A Go crash report logs its first line once; the stack dump after it, whose frames name the dns,
// sniffer and geodata packages, logs nothing, nor does a dump without that first line.
func TestCoreLogSkipsCrashDumps(t *testing.T) {
	const dump = `[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x1234]

goroutine 123 [running]:
github.com/metacubex/mihomo/dns.(*Resolver).exchange(0xc000123456, {0x1, 0x2})
	/home/runner/work/mihomo/mihomo/dns/resolver.go:123 +0x45
github.com/metacubex/mihomo/component/sniffer.(*Dispatcher).TCPSniff(...)
	/home/runner/work/mihomo/mihomo/component/sniffer/dispatcher.go:77 +0x1f fp=0xc0001 sp=0xc0002 pc=0x47
github.com/metacubex/mihomo/listener/sing_tun.(*ListenerHandler).dial(...)
github.com/metacubex/mihomo/component/geodata.LoadGeoSiteMatcher(...)
github.com/metacubex/mihomo/common/queue.New[go.shape.int,go.shape.string](...)
created by github.com/metacubex/mihomo/tunnel.handleTCPConn in goroutine 77
goroutine 1 gp=0xc000002380 m=0 mp=0x1 [select, 5 minutes]:
...additional frames elided...
rax    0x0
rip    0x47a1c1
`
	for _, c := range []struct {
		report, reason, detail, terminal string
		count                            int
	}{
		{"panic: runtime error: invalid memory address or nil pointer dereference\n" + dump,
			"core_diagnostic", "runtime error: invalid memory address or nil pointer dereference", "", 1},
		{"fatal error: runtime: out of memory\n\nruntime stack:\nruntime.throw({0x1, 0x2})\n\t/usr/local/go/src/runtime/panic.go:1023 +0x5c\n" + dump,
			"core_out_of_memory", "", "core_out_of_memory", 1},
		{"runtime: program exceeds 10000-thread limit\nfatal error: thread exhaustion\n" + dump,
			"core_thread_creation_failed", "", "core_thread_creation_failed", 2},
		{"SIGABRT: abort\nPC=0x47a1c1 m=3 sigcode=18446744073709551610\nruntime: g 0: unexpected return pc for runtime.sigtramp called from 0x0\n" + dump,
			"core_diagnostic", "sigabrt: abort", "", 1},
		{dump, "", "", "", 0},
	} {
		r := NewRecords()
		s := logSink{r: r}
		_, _ = s.Write([]byte(c.report))
		logs := r.Logs(0, 10, "", "", "")
		if c.reason == "" {
			if len(logs) != 0 {
				t.Errorf("a dump without its report logged %+v", logs)
			}
			continue
		}
		if len(logs) != 1 || logs[0].Message != c.reason || logs[0].Detail != c.detail || logs[0].Level != "error" || max(logs[0].Count, 1) != c.count || s.terminal != c.terminal {
			t.Errorf("%s\n got %+v, terminal %q\nwant %s %q ×%d, terminal %q", c.report[:40], logs, s.terminal, c.reason, c.detail, c.count, c.terminal)
		}
	}
	// A core log line after the report still counts; the core's own log lines name no frames.
	r := NewRecords()
	s := logSink{r: r}
	_, _ = s.Write([]byte("panic: boom\ngoroutine 1 [running]:\nlevel=error msg=\"cannot allocate memory\"\n"))
	if logs := r.Logs(0, 10, "", "", ""); len(logs) != 2 || logs[0].Message != "core_out_of_memory" {
		t.Errorf("%+v", logs)
	}
}

// What failed to start reads as a listener, the TUN device or the configuration, ahead of the
// words "dns" and "dial" in the same line; a listener or the configuration keeps which one.
func TestCoreLogNamesWhatFailedToStart(t *testing.T) {
	const at = `time="2026-11-22T09:51:42.596400247-08:00" `
	for _, c := range []struct{ line, level, reason, detail string }{
		{at + `level=error msg="Start DNS server(UDP) error: listen udp 127.0.0.1:19764: bind: cannot assign requested address"`,
			"error", "core_listener_failed", "start dns server udp error: listen udp <addr>: bind: cannot assign requested address"},
		{at + `level=error msg="Start DNS server error: listen udp 127.0.0.1:19764: bind: address already in use"`, "error", "listen_address_in_use", ""},
		{at + `level=error msg="Start TProxy server error: listen tcp [::]:19762: bind: address already in use"`, "error", "listen_address_in_use", ""},
		{at + `level=warning msg="Failed to start TProxy UDP Listener: listen udp [::]:19762: setsockopt: protocol not available"`,
			"warn", "core_listener_failed", "failed to start tproxy udp listener: listen udp <addr>: setsockopt: protocol not available"},
		{at + `level=error msg="Listener lan-tproxy listen err: listen tcp 0.0.0.0:19762: bind: cannot assign requested address"`,
			"error", "core_listener_failed", "listener lan-tproxy listen err: listen tcp <addr>: bind: cannot assign requested address"},
		{at + `level=error msg="External controller listen error: listen tcp 127.0.0.1:19761: bind: cannot assign requested address"`,
			"error", "core_listener_failed", "external controller listen error: listen tcp <addr>: bind: cannot assign requested address"},
		{at + `level=error msg="Start Mixed(http+socks) server error: listen tcp 127.0.0.1:7890: bind: cannot assign requested address"`,
			"error", "core_listener_failed", "start mixed http socks server error: listen tcp <addr>: bind: cannot assign requested address"},
		{at + `level=error msg="Start TUN listening error: configure tun interface: device or resource busy"`, "error", "core_tun_issue", ""},
		{at + `level=error msg="Start TUN listening error: create tun: invalid argument"`, "error", "core_tun_issue", ""},
		{at + `level=fatal msg="Parse config error: DNS NameServer[0] format error: dns nameserver unsupport scheme: tls"`,
			"error", "invalid_configuration", "parse config error: dns nameserver 0 format error: dns nameserver unsupport scheme: tls"},
		{at + `level=fatal msg="Parse config error: proxy group[1]: 'name' missing"`, "error", "invalid_configuration", "parse config error: proxy group 1 : name missing"},
		{at + `level=fatal msg="Parse config error: open ./rulesets/abc.mrs: no such file or directory"`,
			"error", "invalid_configuration", "parse config error: open ./rulesets/<host>: no such file or directory"},
		// The catch-all "dial " leaves a dialer-proxy configuration error alone.
		{at + `level=error msg="The group [group:af60f450c0dd13e8eab38ef9bfaed433] with dialer-proxy configuration is not allowed"`,
			"error", "invalid_configuration", "the group <group> with dialer-proxy configuration is not allowed"},
		{at + `level=warning msg="[Inbound] relay: dial tcp 203.0.113.9:443: network is down"`, "", "", ""},
	} {
		r := NewRecords()
		s := logSink{r: r}
		_, _ = s.Write([]byte(c.line + "\n"))
		logs := r.Logs(0, 10, "", "", "")
		if c.reason == "" {
			if len(logs) != 0 {
				t.Errorf("%s\n logged %+v", c.line, logs)
			}
			continue
		}
		if len(logs) != 1 || logs[0].Level != c.level || logs[0].Message != c.reason || logs[0].Detail != c.detail {
			t.Errorf("%s\n got %+v\nwant %s %s %q", c.line, logs, c.level, c.reason, c.detail)
		}
		if fatal := strings.Contains(c.line, "level=fatal"); fatal != (s.terminal == c.reason) {
			t.Errorf("%s\n exit cause %q", c.line, s.terminal)
		}
	}
}
