package proxy

import (
	"crypto/mlkem"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/links/model"
)

// The checks the mihomo core makes when it loads a proxy (adapter/outbound's constructors and
// the options they parse), which would refuse the whole config. A value it refuses is removed
// when the proxy works without it, and the proxy is left out otherwise.

// Shadowsocks methods (sing-shadowsocks2's), with the key length of the 2022 ones.
var (
	ssMethods = set("none aes-128-gcm aes-192-gcm aes-256-gcm chacha20-ietf-poly1305 xchacha20-ietf-poly1305 " +
		"chacha8-ietf-poly1305 xchacha8-ietf-poly1305 rabbit128-poly1305 aes-128-ccm aes-192-ccm aes-256-ccm " +
		"aes-128-gcm-siv aes-256-gcm-siv aegis-128l aegis-256 aez-384 deoxys-ii-256-128 lea-128-gcm lea-192-gcm " +
		"lea-256-gcm ascon128 ascon128a aes-128-ctr aes-192-ctr aes-256-ctr aes-128-cfb aes-192-cfb aes-256-cfb " +
		"rc4-md5 chacha20-ietf xchacha20 chacha20")
	ss2022 = map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32,
		"2022-blake3-chacha20-poly1305": 32, "2022-blake3-chacha8-poly1305": 32, "2022-blake3-aes-128-ccm": 16,
		"2022-blake3-aes-256-ccm": 32}
	// ShadowsocksR takes stream ciphers only.
	ssrCiphers   = set("none dummy rc4-md5 aes-128-ctr aes-192-ctr aes-256-ctr aes-128-cfb aes-192-cfb aes-256-cfb chacha20 chacha20-ietf xchacha20")
	ssrObfs      = set("plain http_simple http_post random_head tls1.2_ticket_auth tls1.2_ticket_fastauth")
	ssrProtocols = set("origin auth_sha1_v4 auth_aes128_md5 auth_aes128_sha1 auth_chain_a auth_chain_b")
	vmessCiphers = set("auto none zero aes-128-cfb aes-128-gcm chacha20-poly1305")
	muxProtocols = set("smux yamux h2mux")
	rate         = regexp.MustCompile(`^(\d+)\s*([KMGT]?)([Bb])ps$`)
)

func (n *normalizer) validate(kind string) *Problem {
	p := &n.p
	switch kind {
	case "ss":
		cipher := p.String("cipher")
		if size, ok := ss2022[cipher]; ok {
			keys := strings.Split(n.text("password"), ":")
			if len(keys) > 1 && strings.Contains(cipher, "chacha") {
				return invalid(cipher + " takes one key")
			}
			for _, key := range keys {
				if decoded, err := base64.StdEncoding.DecodeString(key); err != nil || len(decoded) != size {
					return &Problem{model.WarnInvalidCredential, "a " + cipher + " key is not " + strconv.Itoa(size) + " bytes of Base64"}
				}
			}
		} else if !ssMethods[cipher] {
			return invalid("cipher " + cipher)
		}
		n.oneOf("udp-over-tcp-version", "1", "2")
		if problem := n.plugin(); problem != nil {
			return problem
		}
	case "ssr":
		switch {
		case !ssrCiphers[strings.ToLower(p.String("cipher"))]:
			return invalid("cipher " + p.String("cipher") + ", not a stream cipher")
		case !ssrObfs[p.String("obfs")]:
			return invalid("obfs " + p.String("obfs"))
		case !ssrProtocols[p.String("protocol")]:
			return invalid("protocol " + p.String("protocol"))
		}
	case "vmess":
		if cipher := strings.ToLower(p.String("cipher")); !vmessCiphers[cipher] {
			return invalid("cipher " + cipher)
		}
	case "vless":
		if flow := p.String("flow"); len(flow) >= 16 && flow[:16] != "xtls-rprx-vision" {
			return &Problem{model.WarnUnsupportedOption, "flow " + flow}
		}
		if problem := n.vlessEncryption(); problem != nil {
			return problem
		}
	case "trojan":
		// trojan.go dials ws and grpc, and TLS for anything else: an h2 or HTTP transport would
		// load as plain TLS, which the server does not speak.
		switch network := p.String("network"); network {
		case "", "tcp", "ws", "grpc":
		default:
			return &Problem{model.WarnUnsupportedOption, "Trojan over " + network + ", which mihomo's Trojan does not carry"}
		}
	case "snell":
		if problem := n.snell(); problem != nil {
			return problem
		}
	case "hysteria":
		// mihomo reads a bare number as Mbps and refuses a speed it reads as zero.
		for _, key := range []string{"up", "down"} {
			if speed := n.text(key); bps(speed) == 0 {
				return invalid(key + " " + speed)
			}
		}
		if protocol := p.String("protocol"); protocol != "" && protocol != "udp" && protocol != "wechat-video" && protocol != "faketcp" {
			return invalid("protocol " + protocol)
		}
		if auth := n.text("auth"); auth != "" && !base64Std(auth) {
			return &Problem{model.WarnInvalidCredential, "auth is not Base64"}
		}
	case "hysteria2":
		switch obfs := n.text("obfs"); {
		case obfs == "":
		case obfs != "salamander" && obfs != "gecko":
			return &Problem{model.WarnUnsupportedOption, "obfs " + obfs}
		case n.text("obfs-password") == "":
			return &Problem{model.WarnInvalidCredential, "no obfs-password"}
		case obfs == "gecko":
			n.geckoSizes()
		}
		for _, key := range []string{"up", "down"} {
			if speed := n.text(key); speed != "" && !bandwidth(speed) {
				n.remove(key, "is not a bandwidth")
			}
		}
	case "tuic":
		n.oneOf("udp-over-stream-version", "1", "2")
	case "wireguard":
		if problem := n.wireguardKeys(); problem != nil {
			return problem
		}
	case "nowhere":
		if problem := n.nowhere(); problem != nil {
			return problem
		}
	case "naive":
		if strings.Contains(n.text("username"), ":") {
			return &Problem{model.WarnInvalidCredential, "a username with ':'"}
		}
		n.naive()
	case "zerotier":
		if problem := n.zerotier(); problem != nil {
			return problem
		}
	case "mieru":
		if transport := p.String("transport"); transport != "TCP" && transport != "UDP" {
			return invalid("transport " + transport)
		}
		n.oneOf("multiplexing", "MULTIPLEXING_OFF", "MULTIPLEXING_LOW", "MULTIPLEXING_MIDDLE", "MULTIPLEXING_HIGH")
	case "ssh":
		for _, key := range strings_(p.Value("host-key")) {
			if !authorizedKey(key) {
				return invalid("host key " + key)
			}
		}
	}
	if problem := n.tls(kind); problem != nil {
		return problem
	}
	if ports := p.String("ports"); ports != "" && !portRanges(ports) {
		return invalid("ports " + ports)
	}
	if mux := model.AsObject(p.Value("smux")); mux != nil {
		if protocol := mux.String("protocol"); protocol != "" && !muxProtocols[protocol] {
			n.remove("smux", "protocol "+protocol+" is not one mihomo knows")
		}
	}
	return nil
}

// tls checks the TLS options every protocol shares: the certificate pin, ECH and REALITY.
func (n *normalizer) tls(kind string) *Problem {
	p := &n.p
	if pin := p.String("fingerprint"); pin != "" {
		if sum, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(pin), ":", "")); err != nil || len(sum) != 32 {
			return invalid("fingerprint " + pin + ", not a SHA-256 certificate pin")
		}
	}
	if ech := model.AsObject(p.Value("ech-opts")); ech != nil {
		if config := ech.String("config"); config != "" && !base64Std(config) {
			return invalid("ech-opts config is not Base64")
		}
	}
	// VMess and VLESS take one of REALITY, ShadowTLS, Restls and JLS, each over TLS.
	var modes []string
	for _, key := range []string{"reality-opts", "shadow-tls-opts", "restls-opts", "jls-opts"} {
		if model.AsObject(p.Value(key)) != nil {
			modes = append(modes, key)
		}
	}
	if len(modes) > 1 {
		return &Problem{model.WarnUnsupportedOption, strings.Join(modes, " with ")}
	}
	if len(modes) == 1 && (kind == "vmess" || kind == "vless") {
		if on, _ := p.Value("tls").(bool); !on {
			p.Set("tls", true)
		}
		if network := p.String("network"); modes[0] != "reality-opts" && (network == "kcp" || network == "mkcp") {
			return &Problem{model.WarnUnsupportedOption, modes[0] + " over " + network}
		}
	}
	reality := model.AsObject(p.Value("reality-opts"))
	if reality == nil {
		return nil
	}
	key := text(reality.Value("public-key"))
	if key == "" {
		// legacy removed REALITY without a key, which mihomo ignores, except where AnyTLS refuses it.
		return &Problem{model.WarnInvalidCredential, "AnyTLS REALITY options without a public-key"}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		// Standard Base64, or padded: the same key in another spelling.
		if std, errStd := base64.StdEncoding.DecodeString(key); errStd == nil {
			decoded, err = std, nil
		} else if raw, errRaw := base64.RawStdEncoding.DecodeString(key); errRaw == nil {
			decoded, err = raw, nil
		} else if url, errURL := base64.URLEncoding.DecodeString(key); errURL == nil {
			decoded, err = url, nil
		}
		if err == nil && len(decoded) == 32 {
			reality = reality.Clone()
			reality.Set("public-key", base64.RawURLEncoding.EncodeToString(decoded))
			p.Set("reality-opts", reality)
		}
	}
	if err != nil || len(decoded) != 32 {
		return &Problem{model.WarnInvalidCredential, "REALITY public-key is not an X25519 key"}
	}
	id := text(reality.Value("short-id"))
	if len(id) > 16 || len(id)%2 == 1 || !hexText(id) {
		return &Problem{model.WarnInvalidCredential, "REALITY short-id " + id}
	}
	if kind == "anytls" {
		if p.Value("ech-opts") != nil {
			return &Problem{model.WarnUnsupportedOption, "AnyTLS REALITY with ECH"}
		}
		// mihomo refuses AnyTLS REALITY without a uTLS fingerprint it knows.
		if fingerprint := p.String("client-fingerprint"); !utls[fingerprint] {
			p.Set("client-fingerprint", "chrome")
			n.removed = append(n.removed, "client-fingerprint "+fingerprint+" is not one mihomo knows; chrome instead")
		}
	}
	return nil
}

// plugin checks a Shadowsocks plugin's options.
func (n *normalizer) plugin() *Problem {
	options := model.AsObject(n.p.Value("plugin-opts"))
	switch n.p.String("plugin") {
	case "obfs":
		if mode := options.String("mode"); mode != "http" && mode != "tls" {
			return &Problem{model.WarnUnsupportedOption, "obfs mode " + mode}
		}
	case "shadow-tls":
		if version, ok := model.Int(options.Value("version")); ok && (version < 0 || version > 3) {
			return &Problem{model.WarnUnsupportedOption, "ShadowTLS version " + strconv.Itoa(version)}
		}
	}
	return nil
}

// snell checks what mihomo allows of each Snell version.
func (n *normalizer) snell() *Problem {
	p := &n.p
	version, _ := model.Int(p.Value("version"))
	switch version {
	case 0, 1, 2:
		if udp, _ := model.Bool(p.Value("udp")); udp {
			n.remove("udp", "needs Snell v3 or later")
		}
	case 3, 4, 5:
	case 6:
		if psk := n.text("psk"); len(psk) < 12 || len(psk) > 255 {
			return &Problem{model.WarnInvalidCredential, "a Snell v6 psk of " + strconv.Itoa(len(psk)) + " bytes, not 12 to 255"}
		}
		if mode := model.AsObject(p.Value("obfs-opts")).String("mode"); mode == "http" || mode == "tls" {
			return &Problem{model.WarnUnsupportedOption, "Snell v6 has no obfs"}
		}
		if len(n.text("userkey")) > 255 {
			return &Problem{model.WarnInvalidCredential, "a userkey over 255 bytes"}
		}
		n.oneOf("mode", "default", "unshaped", "unsafe-raw")
		return nil
	default:
		return invalid("Snell version " + strconv.Itoa(version))
	}
	n.remove("userkey", "is Snell v6's")
	n.remove("mode", "is Snell v6's")
	return nil
}

// wireguardKeys checks the keys and addresses mihomo decodes when it loads the proxy.
func (n *normalizer) wireguardKeys() *Problem {
	p := n.p
	keys := []string{p.String("private-key"), p.String("public-key"), p.String("pre-shared-key")}
	for _, item := range model.AsList(p.Value("peers")) {
		peer := model.AsObject(item)
		keys = append(keys, peer.String("public-key"), peer.String("pre-shared-key"))
		if reserved, ok := peer.Value("reserved").([]any); ok && len(reserved) != 0 && len(reserved) != 3 {
			return invalid("reserved is not 3 bytes")
		}
	}
	for _, key := range keys {
		if key != "" && !base64Std(key) {
			return &Problem{model.WarnInvalidCredential, "a WireGuard key is not Base64"}
		}
	}
	if reserved, ok := p.Value("reserved").([]any); ok && len(reserved) != 0 && len(reserved) != 3 {
		return invalid("reserved is not 3 bytes")
	}
	for _, key := range []string{"ip", "ipv6"} {
		if address := p.String(key); address != "" {
			if _, err := netip.ParsePrefix(address); err != nil {
				if _, err := netip.ParseAddr(address); err != nil {
					return invalid(key + " " + address)
				}
			}
		}
	}
	return nil
}

// nowhere checks Nowhere's options (adapter/outbound/nowhere.go).
func (n *normalizer) nowhere() *Problem {
	p := &n.p
	if network := p.String("network"); network != "" && network != "tcp" && network != "udp" {
		return invalid("network " + network)
	}
	if pin := p.String("pin"); pin != "" && (len(pin) != 64 || strings.ToLower(pin) != pin || !hexText(pin)) {
		return invalid("pin is not 64 lowercase hexadecimal characters")
	}
	if skip, _ := model.Bool(p.Value("skip-cert-verify")); skip {
		return &Problem{model.WarnUnsupportedOption, "Nowhere cannot skip certificate verification"}
	}
	p.Delete("skip-cert-verify")
	for _, key := range []string{"client-fingerprint", "alpn"} {
		n.remove(key, "is not Nowhere's")
	}
	for _, key := range []string{"handshake-timeout", "setup-timeout"} {
		if ms, ok := model.Int(p.Value(key)); ok && (ms < 0 || ms > 300000) {
			n.remove(key, "is outside 0 to 300000 milliseconds")
		}
	}
	return nil
}

// vlessEncryption checks VLESS encryption as transport/vless/encryption's NewClient reads it:
// none, or mlkem768x25519plus with a mode, an RTT, keys and padding. "auto" is VMess's word,
// which some clients write for VLESS's none.
func (n *normalizer) vlessEncryption() *Problem {
	encryption := n.text("encryption")
	switch encryption {
	case "", "none":
		return nil
	case "auto":
		n.remove("encryption", "auto is VMess's; VLESS has none")
		return nil
	}
	parts := strings.Split(encryption, ".")
	if parts[0] != "mlkem768x25519plus" {
		return &Problem{model.WarnUnsupportedOption, "VLESS encryption " + encryption}
	}
	bad := &Problem{model.WarnInvalidCredential, "VLESS encryption is not mlkem768x25519plus.<mode>.<rtt>.<keys>"}
	if len(parts) < 4 || (parts[1] != "native" && parts[1] != "xorpub" && parts[1] != "random") ||
		(parts[2] != "1rtt" && parts[2] != "0rtt") {
		return bad
	}
	var keys int
	var paddings []string
	for _, part := range parts[3:] {
		if len(part) < 20 {
			paddings = append(paddings, part)
			continue
		}
		key, err := base64.RawURLEncoding.DecodeString(part)
		switch {
		case err != nil:
			return bad
		case len(key) == 32: // any 32 bytes are an X25519 key
		case len(key) == mlkem.EncapsulationKeySize768:
			if _, err := mlkem.NewEncapsulationKey768(key); err != nil {
				return bad
			}
		default:
			return bad
		}
		keys++
	}
	if keys == 0 || !vlessPadding(strings.Join(paddings, ".")) {
		return bad
	}
	return nil
}

// vlessPadding reports padding ParsePadding reads: lengths and gaps of three numbers each, the
// first at least 100-35-35, at most 65553 bytes in all.
func vlessPadding(padding string) bool {
	if padding == "" {
		return true
	}
	total := 0
	for index, part := range strings.Split(padding, ".") {
		fields := strings.Split(part, "-")
		if len(fields) < 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
			return false
		}
		var y [3]int
		for i := range y {
			var err error
			if y[i], err = strconv.Atoi(fields[i]); err != nil {
				return false
			}
		}
		if index == 0 && (y[0] < 100 || y[1] < 35 || y[2] < 35) {
			return false
		}
		if index%2 == 0 {
			total += max(y[1], y[2])
		}
	}
	return total <= 18+65535
}

// geckoSizes removes Gecko packet sizes sing-quic refuses: the range (512 to 1200 when unset)
// must be positive and end at 2048 at most.
func (n *normalizer) geckoSizes() {
	sizes := [2]int{512, 1200}
	for index, key := range []string{"obfs-min-packet-size", "obfs-max-packet-size"} {
		if size, ok := model.Int(n.p.Value(key)); ok && size != 0 {
			sizes[index] = size
		}
	}
	if sizes[0] <= 0 || sizes[0] > sizes[1] || sizes[1] > 2048 {
		n.remove("obfs-min-packet-size", "is outside Gecko's packet sizes")
		n.remove("obfs-max-packet-size", "is outside Gecko's packet sizes")
	}
}

// naive removes what naive_options.go refuses but the proxy works without.
func (n *normalizer) naive() {
	p := &n.p
	n.oneOf("udp-over-tcp-version", "1", "2")
	if over, _ := model.Bool(p.Value("udp-over-tcp")); !over {
		n.remove("udp-over-tcp-version", "needs udp-over-tcp")
	}
	n.oneOf("quic-congestion-control", "bbr", "bbr2", "cubic", "reno")
	quic, _ := model.Bool(p.Value("quic"))
	if !quic {
		n.remove("quic-congestion-control", "needs quic")
		n.remove("quic-session-receive-window", "needs quic")
	}
	limit := uint64(1<<31 - 1) // HTTP/2's
	if quic {
		limit = 1<<62 - 1
	}
	for _, key := range []string{"stream-receive-window", "quic-session-receive-window"} {
		if value, set := p.Get(key); set {
			if window, ok := unsigned(value); !ok || window > limit {
				n.remove(key, "is not a window of 0 to "+strconv.FormatUint(limit, 10)+" bytes")
			}
		}
	}
	if concurrency, ok := model.Int(p.Value("insecure-concurrency")); ok && concurrency < 0 {
		n.remove("insecure-concurrency", "is negative")
	}
	n.naiveHeaders()
	// Naive's certificate is the CA to trust; anything but PEM certificates is read as a path.
	if certificate := n.text("certificate"); certificate != "" &&
		(!strings.Contains(certificate, "-----BEGIN CERTIFICATE-----") || !x509.NewCertPool().AppendCertsFromPEM([]byte(certificate))) {
		n.remove("certificate", "is not PEM CA certificates")
	}
}

// naiveHeaders keeps the extra headers Naive may send: HTTP tokens for names, no line breaks in
// values, none of the headers the transport writes itself, each once.
func (n *normalizer) naiveHeaders() {
	value, set := n.p.Get("headers")
	if !set {
		return
	}
	headers, ok := value.(model.Object)
	if !ok {
		n.remove("headers", "is not a mapping")
		return
	}
	kept := make(model.Object, 0, len(headers))
	seen := map[string]bool{}
	drop := func(name, why string) { n.removed = append(n.removed, "headers "+name+" "+why) }
	for _, field := range headers {
		name, folded := field.Key, strings.ToLower(field.Key)
		text, scalar := model.Text(field.Value)
		switch {
		case !headerName(name):
			drop(strconv.Quote(name), "is not a header name")
		case !scalar || strings.ContainsAny(text, "\r\n\x00"):
			drop(name, "is not a header value")
		case managedHeaders[folded]:
			drop(name, "is written by the transport")
		case seen[folded]:
			drop(name, "repeats a header")
		default:
			seen[folded] = true
			if _, isBool := field.Value.(bool); isBool {
				field.Value = text // mihomo decodes header values as strings
			}
			kept = append(kept, field)
		}
	}
	if len(kept) == 0 {
		n.p.Delete("headers")
	} else {
		n.p.Set("headers", kept)
	}
}

var managedHeaders = set("padding proxy-authorization host connection proxy-connection transfer-encoding content-length upgrade")

// headerName reports an HTTP token that is not one of Cronet's private "-" controls.
func headerName(name string) bool {
	if name == "" || name[0] == '-' {
		return false
	}
	for _, ch := range name {
		if !('a' <= ch && ch <= 'z' || 'A' <= ch && ch <= 'Z' || '0' <= ch && ch <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", ch)) {
			return false
		}
	}
	return true
}

// zerotier checks the network ID and orbits NewZeroTier parses; everything else is optional.
func (n *normalizer) zerotier() *Problem {
	if id := n.text("network"); !zeroTierNetwork(id) {
		return invalid("ZeroTier network " + id + ", not a 16-digit network ID")
	}
	value, set := n.p.Get("orbit")
	if !set {
		return nil
	}
	var kept []any
	worlds := map[string]bool{}
	for _, item := range model.AsList(value) {
		orbit := model.AsObject(item)
		world, seed := strings.ToLower(text(orbit.Value("world"))), text(orbit.Value("seed"))
		if id, err := strconv.ParseUint(world, 16, 64); len(world) != 16 || err != nil || id == 0 || !zeroTierAddress(seed) {
			n.removed = append(n.removed, fmt.Sprintf("orbit %s/%s is not a world ID and seed node", world, seed))
			continue
		}
		if worlds[world] {
			n.removed = append(n.removed, "orbit "+world+"/"+seed+" repeats its world")
			continue
		}
		worlds[world] = true
		kept = append(kept, orbit)
	}
	if len(kept) == 0 {
		n.p.Delete("orbit")
	} else {
		n.p.Set("orbit", kept)
	}
	return nil
}

// zeroTierNetwork reports a network ID ZT.ParseNetworkID reads: 16 hexadecimal digits, not zero,
// with a controller address that is not reserved unless the network is ad hoc (0xff…).
func zeroTierNetwork(id string) bool {
	raw, err := hex.DecodeString(id)
	if len(id) != 16 || err != nil {
		return false
	}
	var network uint64
	for _, b := range raw {
		network = network<<8 | uint64(b)
	}
	controller := network >> 24 & 0xffffffffff
	return network != 0 && (network>>56 == 0xff || controller != 0 && controller>>32 != 0xff)
}

// zeroTierAddress reports a node address ZT.ParseAddress reads: 10 hexadecimal digits, not zero
// and not 0xff….
func zeroTierAddress(address string) bool {
	raw, err := hex.DecodeString(address)
	return len(address) == 10 && err == nil && raw[0] != 0xff && strings.Trim(address, "0") != ""
}

// text reads an option as mihomo decodes it into a string; numbers count.
func (n *normalizer) text(key string) string {
	return text(n.p.Value(key))
}

// oneOf removes an option whose value is none of values.
func (n *normalizer) oneOf(key string, values ...string) {
	value, set := n.p.Get(key)
	if !set {
		return
	}
	text, _ := model.Text(value)
	for _, allowed := range values {
		if text == allowed {
			return
		}
	}
	n.remove(key, "is "+text+", not one mihomo knows")
}

func bandwidth(speed string) bool {
	if _, err := strconv.Atoi(speed); err == nil {
		return true
	}
	return rate.MatchString(speed)
}

// bps reads a speed as mihomo's utils.StringToBps does, in bytes per second: a bare number is
// Mbps, and 0 is a speed it cannot read.
func bps(speed string) uint64 {
	if mbps, err := strconv.Atoi(speed); err == nil {
		speed = strconv.Itoa(mbps) + " Mbps"
	}
	m := rate.FindStringSubmatch(speed)
	if m == nil {
		return 0
	}
	n := uint64(1)
	switch m[2] {
	case "T":
		n *= 1000
		fallthrough
	case "G":
		n *= 1000
		fallthrough
	case "M":
		n *= 1000
		fallthrough
	case "K":
		n *= 1000
	}
	v, _ := strconv.ParseUint(m[1], 10, 64)
	n *= v
	if m[3] == "b" {
		n /= 8
	}
	return n
}

// unsigned reads a value as mihomo's decoder reads it into a uint64; a negative number wraps.
func unsigned(value any) (uint64, bool) {
	switch v := value.(type) {
	case int:
		return uint64(v), true
	case float64:
		if v < 0 {
			return 1 << 63, true // past any limit, as a wrapped negative is
		}
		return uint64(v), true
	case string:
		u, err := strconv.ParseUint(v, 0, 64)
		return u, err == nil
	}
	return 0, false
}

// portRanges reports ranges mihomo reads: ports and ranges of them, separated by "," or "/".
func portRanges(text string) bool {
	for part := range strings.SplitSeq(strings.ReplaceAll(text, "/", ","), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		low, high, ranged := strings.Cut(part, "-")
		if !portNumber(low) || ranged && !portNumber(high) {
			return false
		}
	}
	return true
}

func portNumber(text string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return err == nil && n >= 0 && n <= 65535
}

func base64Std(text string) bool {
	_, err := base64.StdEncoding.DecodeString(text)
	return err == nil
}

// authorizedKey reports a key in the authorized_keys form ssh.ParseAuthorizedKey reads: a type,
// then Base64 of the key in SSH's wire format, which starts with the same type.
func authorizedKey(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) < 4 {
		return false
	}
	size := int(blob[0])<<24 | int(blob[1])<<16 | int(blob[2])<<8 | int(blob[3])
	return size == len(fields[0]) && len(blob) > 4+size && string(blob[4:4+size]) == fields[0]
}

func hexText(text string) bool {
	_, err := hex.DecodeString(text)
	return err == nil
}
