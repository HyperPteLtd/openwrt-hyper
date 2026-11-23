package proxy

import (
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/links/model"
)

// Problem says why a proxy is left out: Code is a model.Warn* code.
type Problem struct {
	Code, Reason string
}

// Name reads a proxy's name as mihomo does: from "name", or else from a key that folds to it
// ("Name", "NAME"), which Normalize renames.
func Name(p model.Object) string {
	value, set := p.Get("name")
	for index := 0; !set && index < len(p); index++ {
		if strings.ReplaceAll(strings.ToLower(strings.TrimSpace(p[index].Key)), "_", "-") == "name" {
			value, set = p[index].Value, true
		}
	}
	name, _ := model.Text(value)
	return name
}

// Normalize checks a mihomo proxy and repairs what mihomo would refuse but means one thing:
// the spellings older Clash versions and other clients write, numbers and booleans written as
// strings, legacy fields and missing defaults. Valid fields keep their value and place. It
// returns the proxy and the options it removed, or a Problem when the proxy must be left out.
func Normalize(p model.Object) (model.Object, []string, *Problem) {
	n := &normalizer{p: p.Clone()}
	if problem := n.run(); problem != nil {
		return nil, n.removed, problem
	}
	return n.p, n.removed, nil
}

type normalizer struct {
	p       model.Object
	removed []string
}

func invalid(reason string) *Problem { return &Problem{model.WarnInvalidProxy, reason} }

// Other clients' names for mihomo's proxy types.
var typeAliases = map[string]string{
	"shadowsocks":  "ss",
	"shadowsocksr": "ssr",
	"socks":        "socks5",
	"socks5h":      "socks5",
	"hy2":          "hysteria2",
	"hy":           "hysteria",
	"wg":           "wireguard",
	"https":        "http",
}

// Every proxy type the mihomo core builds (adapter/parser.go).
var types = set("ss ssr socks5 http naive nowhere vmess vless vless-reverse snell trojan hysteria hysteria2 " +
	"wireguard tuic shadowquic gost-relay direct dns reject rematch ssh mieru anytls sudoku masque " +
	"trusttunnel openvpn tailscale zerotier easytier")

// Types without a server of their own.
var serverless = set("direct dns reject rematch vless-reverse tailscale zerotier easytier")

// What each type requires besides name, server and port.
var credentials = map[string][]string{
	"ss":          {"cipher", "password"},
	"ssr":         {"cipher", "password", "obfs", "protocol"},
	"vmess":       {"uuid", "alterId", "cipher"},
	"vless":       {"uuid"},
	"trojan":      {"password"},
	"snell":       {"psk"},
	"nowhere":     {"password"},
	"anytls":      {"password"},
	"hysteria":    {"up", "down"},
	"mieru":       {"username", "password", "transport"},
	"ssh":         {"username"},
	"wireguard":   {"private-key"},
	"masque":      {"private-key", "public-key"},
	"sudoku":      {"key"},
	"openvpn":     {"ca"},
	"zerotier":    {"network"},
	"shadowquic":  {},
	"gost-relay":  {},
	"trusttunnel": {},
}

// Field types, by mihomo's key. A value of another type is converted when it means one thing.
var (
	boolKeys = set("udp tfo mptcp tls skip-cert-verify xudp packet-addr global-padding authenticated-length " +
		"reduce-rtt fast-open disable-sni udp-over-tcp udp-over-stream reuse quic morph disable-mtu-discovery " +
		"ephemeral remote-dns-resolve disable-reuse accept-routes exit-node-allow-lan-access zero-rtt")
	intKeys = set("port alterId version mtu heartbeat-interval request-timeout max-udp-relay-packet-size " +
		"max-open-streams cwnd recv-window recv-window-conn workers persistent-keepalive udp-over-tcp-version " +
		"udp-over-stream-version idle-session-check-interval idle-session-timeout min-idle-session udp-mtu " +
		"handshake-timeout setup-timeout obfs-min-packet-size obfs-max-packet-size insecure-concurrency " +
		"refresh-server-ip-interval max-datagram-frame-size up-speed down-speed keep-alive-interval")
	textKeys = set("name type server password uuid cipher username psk userkey sni servername flow encryption " +
		"packet-encoding network client-fingerprint fingerprint obfs obfs-password auth auth-str up down ports " +
		"protocol obfs-param protocol-param private-key public-key pre-shared-key ip ipv6 congestion-controller " +
		"udp-relay-mode token transport multiplexing handshake-mode traffic-pattern port-range plugin mode pin " +
		"ip-version dialer-proxy key")
	listKeys = set("alpn allowed-ips host-key host-key-algorithms dns")
)

// mihomo matches keys without case and with "_" as "-"; canonical spells them as it writes them.
var canonical = func() map[string]string {
	out := map[string]string{}
	for _, keys := range []map[string]bool{boolKeys, intKeys, textKeys, listKeys} {
		for key := range keys {
			out[strings.ToLower(key)] = key
		}
	}
	for _, key := range strings.Fields("plugin-opts obfs-opts ws-opts h2-opts http-opts grpc-opts xhttp-opts " +
		"reality-opts ech-opts shadow-tls-opts restls-opts jls-opts smux peers amnezia-wg-option ss-opts " +
		"realm-opts certificate name-cert-verify headers interface-name routing-mark") {
		out[key] = key
	}
	return out
}()

// Other spellings of mihomo's keys.
var keyAliases = map[string]string{
	"alter-id":            "alterId",
	"aid":                 "alterId",
	"allow-insecure":      "skip-cert-verify",
	"allowinsecure":       "skip-cert-verify",
	"insecure":            "skip-cert-verify",
	"tls-skip-verify":     "skip-cert-verify",
	"obfs-pass":           "obfs-password",
	"obfs-param":          "obfs-param",
	"obfsparam":           "obfs-param",
	"protocolparam":       "protocol-param",
	"protoparam":          "protocol-param",
	"method":              "cipher",
	"encrypt-method":      "cipher",
	"server-port":         "port",
	"fast-open":           "fast-open",
	"udp-relay":           "udp",
	"publickey":           "public-key",
	"privatekey":          "private-key",
	"presharedkey":        "pre-shared-key",
	"preshared-key":       "pre-shared-key",
	"allowedips":          "allowed-ips",
	"congestion-control":  "congestion-controller",
	"heartbeat":           "heartbeat-interval",
	"hopinterval":         "hop-interval",
	"mport":               "ports",
	"server-ports":        "ports",
	"auth_str":            "auth-str",
	"authstr":             "auth-str",
	"service-name":        "grpc-service-name",
	"servicename":         "grpc-service-name",
	"client_fingerprint":  "client-fingerprint",
	"clientfingerprint":   "client-fingerprint",
	"dialerproxy":         "dialer-proxy",
	"udp-over-tcp-enable": "udp-over-tcp",
}

func (n *normalizer) remove(key, why string) {
	if _, set := n.p.Get(key); set {
		n.p.Delete(key)
		n.removed = append(n.removed, key+" "+why)
	}
}

func (n *normalizer) run() *Problem {
	if problem := n.keys(); problem != nil {
		return problem
	}
	kind := n.p.String("type")
	if problem := n.values(kind); problem != nil {
		return problem
	}
	n.legacy(kind)
	n.local(kind)
	if problem := n.typed(kind); problem != nil {
		return problem
	}
	if problem := n.required(kind); problem != nil {
		return problem
	}
	return n.validate(kind)
}

// keys canonicalizes key spellings and the type.
func (n *normalizer) keys() *Problem {
	for _, field := range append(model.Object(nil), n.p...) {
		folded := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(field.Key)), "_", "-")
		target := canonical[folded]
		if alias, ok := keyAliases[folded]; ok {
			target = alias
		}
		if target == "" || target == field.Key {
			continue
		}
		if _, taken := n.p.Get(target); taken {
			n.p.Delete(field.Key) // the canonical spelling wins
			continue
		}
		n.p.Rename(field.Key, target)
	}
	text, _ := model.Text(n.p.Value("type"))
	kind := strings.ToLower(strings.TrimSpace(text))
	if kind == "" {
		return invalid("no type")
	}
	if kind == "https" {
		n.p.Set("tls", true)
	}
	if alias, ok := typeAliases[kind]; ok {
		kind = alias
	}
	if !types[kind] {
		return &Problem{model.WarnUnsupportedProxy, "type " + kind}
	}
	n.p.Set("type", kind)
	return nil
}

// values converts what mihomo's decoder (common/structure) refuses or misreads into the type
// it decodes a key into. It reads numbers as booleans and numeric strings as numbers, so those
// stay as written.
func (n *normalizer) values(kind string) *Problem {
	for index := 0; index < len(n.p); index++ {
		key, value := n.p[index].Key, n.p[index].Value
		converted, keep := value, true
		switch {
		case key == "hop-interval":
			// Seconds; Hysteria 2 also takes a range of them, "10-30".
			text, isText := value.(string)
			switch {
			case kind == "hysteria2" && isText && strings.Trim(text, "0123456789-") == "" && text != "":
			case AcceptedInt(value) && !isText:
			default:
				converted, keep = duration(value)
			}
		case key == "heartbeat-interval":
			// Milliseconds.
			if !AcceptedInt(value) {
				d, err := time.ParseDuration(text(value))
				converted, keep = int(d/time.Millisecond), err == nil && d > 0
			}
		case boolKeys[key]:
			if _, ok := value.(bool); !ok && !isInt(value) {
				converted, keep = model.Bool(value)
			}
		case intKeys[key]:
			if !AcceptedInt(value) {
				if converted, keep = model.Int(value); !keep && key != "port" {
					converted, keep = duration(value)
				}
				if !keep && key == "port" {
					return invalid("port " + describe(value))
				}
			}
		case textKeys[key]:
			switch value.(type) {
			case string, int:
			case nil:
				if key == "name" {
					converted = ""
				} else {
					n.p.Delete(key)
					index--
					continue
				}
			default:
				converted, keep = model.Text(value)
				if !keep && key == "name" {
					converted, keep = "", true // named later, among the others
				} else if !keep && key == "server" {
					return invalid("server " + describe(value))
				}
			}
		case listKeys[key]:
			if _, ok := value.([]any); !ok {
				converted = strings_(value)
			}
		}
		if !keep {
			n.remove(key, "has a value mihomo cannot read")
			index--
			continue
		}
		n.p[index].Value = converted
	}
	return nil
}

func isInt(value any) bool {
	_, ok := value.(int)
	return ok
}

// AcceptedInt reports a number mihomo's decoder reads: a number, or a string strconv.ParseInt
// reads in base 0.
func AcceptedInt(value any) bool {
	switch v := value.(type) {
	case int, float64:
		return true
	case string:
		_, err := strconv.ParseInt(v, 0, 64)
		return err == nil
	}
	return false
}

// local removes the options a subscription must not set: the client's own interface and
// routing mark, and files on the client's disk.
func (n *normalizer) local(kind string) {
	for _, key := range []string{"interface-name", "routing-mark"} {
		n.remove(key, "is the client's setting")
	}
	keyed := kind == "wireguard" || kind == "masque"
	n.p = stripFiles(n.p, func(key string) {
		n.removed = append(n.removed, key+" is a local file")
	}, keyed, 0)
	if kind == "tailscale" || kind == "zerotier" {
		n.remove("state-dir", "is the client's setting")
	}
	if kind == "zerotier" {
		n.remove("planet", "is a local file")
	}
}

func stripFiles(o model.Object, removed func(string), keyed bool, depth int) model.Object {
	out := o[:0:0]
	for _, field := range o {
		key := field.Key
		text, isText := field.Value.(string)
		switch {
		case strings.HasSuffix(key, "-path") || strings.HasSuffix(key, "-file") || key == "path-in-bundle":
			removed(key)
			continue
		case (key == "certificate" || key == "private-key" || key == "ca") && isText && text != "" &&
			!strings.Contains(text, "-----BEGIN") && !(depth == 0 && keyed && key == "private-key"):
			removed(key)
			continue
		}
		if nested, ok := field.Value.(model.Object); ok && depth < 8 {
			field.Value = stripFiles(nested, removed, false, depth+1)
		}
		out = append(out, field)
	}
	return out
}

// legacy rewrites the fields older Clash versions and other clients write into mihomo's.
func (n *normalizer) legacy(kind string) {
	p := &n.p
	// The server may carry the port, or brackets around IPv6.
	if server := strings.TrimSpace(p.String("server")); server != "" {
		if host, port, ok := splitHostPort(server); ok {
			server = host
			if _, set := p.Get("port"); !set {
				p.InsertAfter("server", "port", port)
			}
		}
		p.Set("server", strings.TrimSuffix(strings.TrimPrefix(server, "["), "]"))
	}
	// Clash wrote ws-path and ws-headers (and VLESS ws-headers) before ws-opts.
	if path, headers := p.Value("ws-path"), p.Value("ws-headers"); path != nil || headers != nil {
		if _, set := p.Get("ws-opts"); !set && (kind == "vmess" || kind == "vless" || kind == "trojan") {
			opts := model.New("path", text(path), "headers", model.AsObject(headers))
			p.Set("ws-opts", opts)
			if _, set := p.Get("network"); !set {
				p.Set("network", "ws")
			}
		}
		p.Delete("ws-path")
		p.Delete("ws-headers")
	}
	if network := strings.ToLower(p.String("network")); network != "" {
		p.Set("network", network)
	}
	// Some clients put the uTLS fingerprint where mihomo expects a certificate's.
	if fingerprint := strings.ToLower(p.String("fingerprint")); utls[fingerprint] {
		if _, set := p.Get("client-fingerprint"); !set {
			p.Rename("fingerprint", "client-fingerprint")
			p.Set("client-fingerprint", fingerprint)
		} else {
			p.Delete("fingerprint")
		}
	}
	if fingerprint := p.String("client-fingerprint"); fingerprint != "" {
		p.Set("client-fingerprint", strings.ToLower(fingerprint))
	}
	// VMess and VLESS name the TLS server "servername", the others "sni".
	switch kind {
	case "vmess", "vless":
		if _, set := p.Get("servername"); !set {
			p.Rename("sni", "servername")
		}
		p.Delete("sni")
	default:
		if _, set := p.Get("sni"); !set {
			p.Rename("servername", "sni")
		}
		p.Delete("servername")
	}
	if alpn, ok := p.Value("alpn").(string); ok {
		p.Set("alpn", strings_(alpn))
	}
	if reality := model.AsObject(p.Value("reality-opts")); reality != nil {
		reality = renameKeys(reality, map[string]string{"publickey": "public-key", "public-key": "public-key", "pbk": "public-key", "shortid": "short-id", "short-id": "short-id", "sid": "short-id"})
		if id, ok := reality.Value("short-id").(float64); ok {
			reality.Set("short-id", strconv.FormatFloat(id, 'f', -1, 64))
		}
		p.Set("reality-opts", reality)
		// mihomo takes REALITY without a public-key as plain TLS (reality.go), but AnyTLS refuses
		// a short-id or ML-KEM setting without one, which validate reports.
		mlkem, _ := model.Bool(reality.Value("support-x25519mlkem768"))
		if text(reality.Value("public-key")) == "" && (kind != "anytls" || text(reality.Value("short-id")) == "" && !mlkem) {
			n.remove("reality-opts", "has no public-key, so mihomo does not use REALITY")
		} else if _, set := p.Get("client-fingerprint"); !set {
			p.Set("client-fingerprint", "chrome") // REALITY needs a uTLS fingerprint
		}
	}
	n.transport()
}

// transport repairs the V2Ray transport options.
func (n *normalizer) transport() {
	p := &n.p
	if ws := model.AsObject(p.Value("ws-opts")); ws != nil {
		ws = ws.Clone()
		if headers := model.AsObject(ws.Value("headers")); headers != nil {
			ws.Set("headers", singleHeaders(headers))
		}
		if value, set := ws.Get("max-early-data"); set && !AcceptedInt(value) {
			if i, ok := model.Int(value); ok {
				ws.Set("max-early-data", i)
			} else {
				ws.Delete("max-early-data")
			}
		}
		for _, key := range []string{"v2ray-http-upgrade", "v2ray-http-upgrade-fast-open"} {
			if value, ok := ws.Value(key).(string); ok {
				b, _ := model.Bool(value)
				ws.Set(key, b)
			}
		}
		p.Set("ws-opts", ws)
	}
	if h2 := model.AsObject(p.Value("h2-opts")); h2 != nil {
		h2 = h2.Clone()
		if host, ok := h2.Value("host").(string); ok {
			h2.Set("host", strings_(host))
		}
		p.Set("h2-opts", h2)
	}
	if http := model.AsObject(p.Value("http-opts")); http != nil {
		http = http.Clone()
		if path, ok := http.Value("path").(string); ok {
			http.Set("path", []string{path})
		}
		if headers := model.AsObject(http.Value("headers")); headers != nil {
			lists := make(model.Object, 0, len(headers))
			for _, field := range headers {
				if value, ok := field.Value.(string); ok {
					field.Value = []string{value}
				}
				lists = append(lists, field)
			}
			http.Set("headers", lists)
		}
		p.Set("http-opts", http)
	}
	if grpc := model.AsObject(p.Value("grpc-opts")); grpc != nil {
		p.Set("grpc-opts", renameKeys(grpc, map[string]string{"servicename": "grpc-service-name", "service-name": "grpc-service-name", "grpc-service-name": "grpc-service-name"}))
	}
}

// typed repairs what is particular to a type.
func (n *normalizer) typed(kind string) *Problem {
	p := &n.p
	switch kind {
	case "ss":
		if cipher := strings.ToLower(p.String("cipher")); cipher != "" {
			p.Set("cipher", ssCipher(cipher))
		}
		// ClashX and Surge-era configs set obfs on the proxy itself.
		if mode := p.String("obfs"); mode != "" {
			if _, set := p.Get("plugin"); !set {
				p.Set("plugin", "obfs")
				p.Set("plugin-opts", model.New("mode", mode, "host", p.String("obfs-host")))
			}
			p.Delete("obfs")
			p.Delete("obfs-host")
		}
		if plugin := strings.ToLower(p.String("plugin")); plugin != "" {
			return n.shadowsocksPlugin(plugin)
		}
		p.Delete("plugin-opts")
	case "ssr":
		if _, set := p.Get("obfs"); !set {
			p.Set("obfs", "plain")
		}
		if _, set := p.Get("protocol"); !set {
			p.Set("protocol", "origin")
		}
		if cipher := strings.ToLower(p.String("cipher")); cipher != "" {
			p.Set("cipher", ssCipher(cipher))
		}
	case "vmess":
		// mihomo requires both; it reads the cipher in any case.
		if _, set := p.Get("alterId"); !set {
			p.InsertAfter("uuid", "alterId", 0)
		}
		switch cipher := strings.ToLower(p.String("cipher")); cipher {
		case "":
			p.InsertAfter("alterId", "cipher", "auto")
		case "chacha20-ietf-poly1305":
			p.Set("cipher", "chacha20-poly1305") // Quantumult X's name for it
		}
	case "snell":
		// Snell takes a psk, and v6 a userkey; a password is another client's word for one of them.
		if _, set := p.Get("password"); set {
			if p.String("psk") == "" {
				return &Problem{model.WarnInvalidCredential, "a password but no psk"}
			}
			n.remove("password", "is not a Snell field")
		}
		if mode := p.String("obfs"); mode != "" {
			if _, set := p.Get("obfs-opts"); !set {
				p.Set("obfs-opts", model.New("mode", mode, "host", p.String("obfs-host")))
			}
			p.Delete("obfs")
			p.Delete("obfs-host")
		}
	case "hysteria", "hysteria2":
		if ports := p.String("ports"); strings.Contains(ports, ":") {
			p.Set("ports", strings.ReplaceAll(strings.ReplaceAll(ports, ":", "-"), " ", ""))
		}
		if p.String("ports") == "" {
			p.Delete("ports")
		}
		if kind == "hysteria" {
			for _, key := range []string{"up", "down"} {
				if speed := p.String(key); speed == "" {
					if speeds, ok := model.Int(p.Value(key + "-speed")); ok && speeds > 0 {
						p.Set(key, strconv.Itoa(speeds)+" Mbps")
					}
				}
			}
		}
	case "mieru":
		if transport := strings.ToUpper(p.String("transport")); transport == "" {
			p.Set("transport", "TCP")
		} else {
			p.Set("transport", transport)
		}
	case "naive":
		// Cronet verifies certificates and picks the TLS fingerprint and ALPN itself.
		if b, _ := model.Bool(p.Value("skip-cert-verify")); b {
			return &Problem{model.WarnUnsupportedOption, "Naive cannot skip certificate verification"}
		}
		p.Delete("skip-cert-verify")
		for _, key := range []string{"client-fingerprint", "fingerprint", "alpn", "name-cert-verify"} {
			n.remove(key, "is Cronet's own")
		}
		n.remove("private-key", "is a client key, which Cronet does not present")
		if udp, _ := model.Bool(p.Value("udp")); udp {
			if over, _ := model.Bool(p.Value("udp-over-tcp")); !over {
				n.remove("udp", "needs udp-over-tcp")
			}
		}
	case "wireguard":
		return n.wireguard()
	case "tuic":
		if text(p.Value("token")) == "" && text(p.Value("uuid")) == "" {
			return invalid("no token or UUID")
		}
	}
	if version := p.String("ip-version"); version != "" {
		switch strings.ToLower(version) {
		case "dual", "ipv4", "ipv6", "ipv4-prefer", "ipv6-prefer":
			p.Set("ip-version", strings.ToLower(version))
		default:
			n.remove("ip-version", "is not one mihomo knows")
		}
	}
	return nil
}

func (n *normalizer) shadowsocksPlugin(plugin string) *Problem {
	p := &n.p
	var options model.Object
	switch opts := p.Value("plugin-opts").(type) {
	case model.Object:
		options = opts.Clone()
	case string:
		for key, value := range PluginOptions(opts) {
			options = append(options, model.Field{Key: key, Value: value})
		}
		sortFields(options)
	}
	switch plugin {
	case "obfs-local", "simple-obfs", "obfs":
		plugin = "obfs"
		options.Rename("obfs", "mode")
		options.Rename("obfs-host", "host")
	case "v2ray-plugin", "gost-plugin":
		if mode := options.String("mode"); mode == "" {
			options = append(model.Object{{Key: "mode", Value: "websocket"}}, withoutKey(options, "mode")...)
		} else if mode != "websocket" {
			return &Problem{model.WarnUnsupportedOption, plugin + " mode " + mode}
		}
		for _, key := range []string{"tls", "mux", "skip-cert-verify"} {
			if value, set := options.Get(key); set {
				b, ok := model.Bool(value)
				if !ok && value == "" {
					b = true // a flag without a value, as SIP003 writes it
				}
				options.Set(key, b)
			}
		}
	case "shadow-tls", "restls", "jls", "kcptun":
		if version, set := options.Get("version"); set {
			if v, ok := model.Int(version); ok {
				options.Set("version", v)
			}
		}
	default:
		return &Problem{model.WarnUnsupportedOption, "plugin " + plugin}
	}
	p.Set("plugin", plugin)
	p.Set("plugin-opts", options)
	return nil
}

func (n *normalizer) wireguard() *Problem {
	p := &n.p
	peers := model.AsList(p.Value("peers"))
	var kept []any
	for _, item := range peers {
		peer := model.AsObject(item)
		if peer == nil {
			continue
		}
		peer = peer.Clone()
		if server := strings.TrimSpace(peer.String("server")); server != "" {
			if host, port, ok := splitHostPort(server); ok {
				peer.Set("server", host)
				if _, set := peer.Get("port"); !set {
					peer.InsertAfter("server", "port", port)
				}
			}
		}
		switch ips := peer.Value("allowed-ips").(type) {
		case []any, []string:
		case string:
			peer.Set("allowed-ips", strings_(ips))
		default:
			peer.Set("allowed-ips", []string{"0.0.0.0/0", "::/0"}) // mihomo requires them
		}
		if reserved := reservedBytes(peer.Value("reserved")); reserved != nil {
			peer.Set("reserved", reserved)
		}
		if peer.String("server") == "" || peer.String("public-key") == "" {
			continue
		}
		kept = append(kept, peer)
	}
	if peers != nil {
		if len(kept) == 0 {
			return invalid("no usable WireGuard peer")
		}
		p.Set("peers", kept)
	} else {
		if p.String("public-key") == "" {
			return &Problem{model.WarnInvalidCredential, "no public key"}
		}
		if ips, ok := p.Value("allowed-ips").(string); ok {
			p.Set("allowed-ips", strings_(ips))
		}
	}
	if p.String("ip") == "" && p.String("ipv6") == "" {
		return invalid("no local address (ip or ipv6)")
	}
	if reserved := reservedBytes(p.Value("reserved")); reserved != nil {
		p.Set("reserved", reserved)
	}
	return nil
}

// required checks the fields mihomo cannot do without.
func (n *normalizer) required(kind string) *Problem {
	p := n.p
	peers := kind == "wireguard" && p.Value("peers") != nil
	if !serverless[kind] && !peers {
		server := p.String("server")
		if server == "" {
			return invalid("no server")
		}
		if strings.ContainsAny(server, " \t/\\@?#") {
			return invalid("server " + server)
		}
		port, _ := model.Int(p.Value("port"))
		ranged := p.String("ports") != "" || p.String("port-range") != ""
		switch {
		case port == 0 && ranged && (kind == "hysteria" || kind == "hysteria2" || kind == "mieru"):
		case port < 1 || port > 65535:
			if _, set := p.Get("port"); !set {
				return invalid("no port")
			}
			return invalid("port " + strconv.Itoa(port))
		}
	}
	plain := kind == "ss" && isPlain(strings.ToLower(p.String("cipher")))
	for _, key := range credentials[kind] {
		value, set := p.Get(key)
		empty := !set || value == ""
		switch {
		case empty && key == "password" && plain:
			if !set {
				n.p.Set("password", "") // mihomo requires the key
			}
		case empty && (key == "cipher" || key == "up" || key == "down" || key == "transport"):
			return invalid("no " + key)
		case empty:
			return &Problem{model.WarnInvalidCredential, "no " + key}
		}
	}
	return nil
}

// mihomo's uTLS fingerprints (component/tls/utls.go).
var utls = set("chrome firefox safari ios android edge 360 qq random randomized chrome120 firefox120 safari16 " +
	"chrome_psk chrome_psk_shuffle chrome_padding_psk_shuffle chrome_pq chrome_pq_psk")

func isPlain(cipher string) bool {
	return cipher == "none" || cipher == "plain" || cipher == "dummy"
}

// ssCipher maps other names of Shadowsocks ciphers to mihomo's.
func ssCipher(cipher string) string {
	cipher = strings.ReplaceAll(cipher, "_", "-")
	cipher = strings.TrimPrefix(cipher, "aead-")
	switch cipher {
	case "chacha20-poly1305":
		return "chacha20-ietf-poly1305"
	case "xchacha20-poly1305":
		return "xchacha20-ietf-poly1305"
	case "table", "plain", "dummy":
		return "none"
	}
	return cipher
}

// reservedBytes reads WireGuard's reserved bytes from a list, "1,2,3" or Base64.
func reservedBytes(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if i, ok := model.Int(item); ok {
				out = append(out, i)
			}
		}
		return out
	case string:
		if strings.Contains(v, ",") {
			out := []any{}
			for _, part := range strings.Split(v, ",") {
				if i, ok := model.Int(part); ok {
					out = append(out, i)
				}
			}
			return out
		}
		return v // Base64, which mihomo decodes itself
	}
	return nil
}

func splitHostPort(server string) (string, int, bool) {
	if _, err := netip.ParseAddr(strings.Trim(server, "[]")); err == nil {
		return "", 0, false
	}
	index := strings.LastIndex(server, ":")
	if index <= 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(server[index+1:])
	if err != nil {
		return "", 0, false
	}
	return strings.Trim(server[:index], "[]"), port, true
}

// duration reads seconds written as a number or a Go duration ("30s", "5m").
func duration(value any) (int, bool) {
	if i, ok := model.Int(value); ok {
		return i, true
	}
	text, _ := value.(string)
	if d, err := time.ParseDuration(strings.TrimSpace(text)); err == nil && d >= 0 {
		return int(d / time.Second), true
	}
	return 0, false
}

func describe(value any) string {
	if text, ok := model.Text(value); ok {
		return strconv.Quote(text)
	}
	if value == nil {
		return "missing"
	}
	return "is not a scalar"
}

func text(value any) string {
	s, _ := model.Text(value)
	return s
}

// strings_ reads a list of strings, or a comma-separated string, or nil.
func strings_(value any) []string {
	switch v := value.(type) {
	case string:
		var out []string
		for part := range strings.SplitSeq(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := model.Text(item); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// singleHeaders turns header lists, as sing-box and Xray write them, into the single values
// mihomo's ws-opts take.
func singleHeaders(headers model.Object) model.Object {
	out := make(model.Object, 0, len(headers))
	for _, field := range headers {
		switch v := field.Value.(type) {
		case []any:
			if len(v) > 0 {
				s, _ := model.Text(v[0])
				out = append(out, model.Field{Key: field.Key, Value: s})
			}
		case nil:
		default:
			out = append(out, field)
		}
	}
	return out
}

// renameKeys gives keys that fold (case, "_" as "-") to one of names its canonical spelling; the
// first field of a name wins.
func renameKeys(o model.Object, names map[string]string) model.Object {
	out := make(model.Object, 0, len(o))
	for _, field := range o {
		if name, ok := names[strings.ReplaceAll(strings.ToLower(field.Key), "_", "-")]; ok {
			if _, taken := out.Get(name); taken {
				continue
			}
			field.Key = name
		}
		out = append(out, field)
	}
	return out
}

func withoutKey(o model.Object, key string) model.Object {
	out := make(model.Object, 0, len(o))
	for _, field := range o {
		if field.Key != key {
			out = append(out, field)
		}
	}
	return out
}

func sortFields(o model.Object) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0 && o[j].Key < o[j-1].Key; j-- {
			o[j], o[j-1] = o[j-1], o[j]
		}
	}
}

func set(words string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(words) {
		out[word] = true
	}
	return out
}
