package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/links/model"
)

func decode(t *testing.T, text string) model.Object {
	t.Helper()
	value, err := model.Decode([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return model.AsObject(value)
}

// The checks follow what the mihomo core's constructors refuse at load (which takes the whole
// config down) and what they accept: each case is a proxy, the problem that leaves it out (""
// when it stays), the options it loses and what it looks like after.
func TestNormalizeLoadChecks(t *testing.T) {
	x25519 := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	decapsulation, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	mlkemKey := base64.RawURLEncoding.EncodeToString(decapsulation.EncapsulationKey().Bytes())
	badMLKEM := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("\xff", mlkem.EncapsulationKeySize768)))
	vless := "{name: v, type: vless, server: v.example.com, port: 443, uuid: 7c8c8c0e-0000-4000-8000-000000000001, "
	for _, c := range []struct {
		name, input, problem string
		removed              []string
		want                 string // a JSON object whose fields the result has, or "" to skip
		absent               []string
	}{
		// VLESS encryption: none, mlkem768x25519plus, or VMess's auto, which means none.
		{name: "vless none", input: vless + "encryption: none}", want: `{"encryption":"none"}`},
		{name: "vless auto", input: vless + "encryption: auto}", removed: []string{"encryption auto"}, absent: []string{"encryption"}},
		{name: "vless zero", input: vless + "encryption: zero}", problem: model.WarnUnsupportedOption},
		{name: "vless mlkem x25519", input: vless + "encryption: mlkem768x25519plus.native.0rtt." + x25519 + "}"},
		{name: "vless mlkem padded", input: vless + "encryption: mlkem768x25519plus.xorpub.1rtt.100-111-1111.75-0-111." + mlkemKey + "}"},
		{name: "vless mlkem no key", input: vless + "encryption: mlkem768x25519plus.native.1rtt.short}", problem: model.WarnInvalidCredential},
		{name: "vless mlkem mode", input: vless + "encryption: mlkem768x25519plus.plain.1rtt." + x25519 + "}", problem: model.WarnInvalidCredential},
		{name: "vless mlkem bad key", input: vless + "encryption: mlkem768x25519plus.random.1rtt." + badMLKEM + "}", problem: model.WarnInvalidCredential},
		{name: "vless mlkem padding", input: vless + "encryption: mlkem768x25519plus.native.1rtt.50-10-10." + x25519 + "}", problem: model.WarnInvalidCredential},

		// Trojan carries TCP, ws and grpc; anything else would load as plain TLS.
		{name: "trojan ws", input: "{name: t, type: trojan, server: t.example.com, port: 443, password: p, network: ws, ws-opts: {path: /t}}"},
		{name: "trojan tcp", input: "{name: t, type: trojan, server: t.example.com, port: 443, password: p, network: tcp}"},
		{name: "trojan h2", input: "{name: t, type: trojan, server: t.example.com, port: 443, password: p, network: h2, h2-opts: {host: [t.example.com]}}", problem: model.WarnUnsupportedOption},
		{name: "trojan http", input: "{name: t, type: trojan, server: t.example.com, port: 443, password: p, network: http}", problem: model.WarnUnsupportedOption},

		// Hysteria reads bare numbers as Mbps, and numbers as text wherever it takes text.
		{name: "hysteria numbers", input: "{name: h, type: hysteria, server: h.example.com, port: 443, auth-str: a, up: 100, down: 200}", want: `{"up":100,"down":200}`},
		{name: "hysteria units", input: "{name: h, type: hysteria, server: h.example.com, port: 443, up: 30 Mbps, down: 1 GBps}"},
		{name: "hysteria zero", input: "{name: h, type: hysteria, server: h.example.com, port: 443, up: 0, down: 10}", problem: model.WarnInvalidProxy},
		{name: "hysteria negative", input: "{name: h, type: hysteria, server: h.example.com, port: 443, up: -5, down: 10}", problem: model.WarnInvalidProxy},
		{name: "hysteria bits", input: "{name: h, type: hysteria, server: h.example.com, port: 443, up: 4 bps, down: 10}", problem: model.WarnInvalidProxy},
		{name: "hysteria auth", input: "{name: h, type: hysteria, server: h.example.com, port: 443, auth: '#', up: 10, down: 10}", problem: model.WarnInvalidCredential},
		{name: "hysteria2 gecko", input: "{name: h, type: hysteria2, server: h.example.com, port: 443, password: p, obfs: gecko, obfs-password: g, obfs-min-packet-size: 600, obfs-max-packet-size: 1400}", want: `{"obfs":"gecko","obfs-min-packet-size":600}`},
		{name: "hysteria2 gecko sizes", input: "{name: h, type: hysteria2, server: h.example.com, port: 443, password: p, obfs: gecko, obfs-password: g, obfs-min-packet-size: 1500}", removed: []string{"obfs-min-packet-size"}},
		{name: "hysteria2 gecko too large", input: "{name: h, type: hysteria2, server: h.example.com, port: 443, password: p, obfs: gecko, obfs-password: g, obfs-max-packet-size: 4096}", removed: []string{"obfs-max-packet-size"}},
		{name: "hysteria2 numeric obfs-password", input: "{name: h, type: hysteria2, server: h.example.com, port: 443, password: p, obfs: salamander, obfs-password: 12345, up: 50}", want: `{"obfs-password":12345,"up":50}`},
		{name: "hysteria2 no obfs-password", input: "{name: h, type: hysteria2, server: h.example.com, port: 443, password: p, obfs: gecko}", problem: model.WarnInvalidCredential},
		{name: "hysteria2 other obfs", input: "{name: h, type: hysteria2, server: h.example.com, port: 443, password: p, obfs: xplus, obfs-password: x}", problem: model.WarnUnsupportedOption},
		{name: "tuic numeric token", input: "{name: t, type: tuic, server: t.example.com, port: 443, token: 123456}", want: `{"token":123456}`},
		{name: "snell numeric psk", input: "{name: s, type: snell, server: s.example.com, port: 443, version: 6, psk: 123456789012}"},

		// ZeroTier needs a network ID only; seeds and worlds are optional orbits.
		{name: "zerotier", input: "{name: z, type: zerotier, network: 8056c2e21c000001}"},
		{name: "zerotier network", input: "{name: z, type: zerotier, network: 0000000000000001}", problem: model.WarnInvalidProxy},
		{name: "zerotier ad hoc", input: "{name: z, type: zerotier, network: ff0000000000ffff}"},
		{name: "zerotier local", input: "{name: z, type: zerotier, network: 8056c2e21c000001, state-dir: zt, planet: ./planet}", removed: []string{"state-dir", "planet"}},
		{name: "zerotier orbits", input: "{name: z, type: zerotier, network: 8056c2e21c000001, orbit: [{world: 000000000000abcd, seed: abcdef0123}, {world: 000000000000ABCD, seed: abcdef0124}, {world: 1, seed: ff00000000}]}",
			removed: []string{"orbit 000000000000abcd/abcdef0124", "orbit 1/ff00000000"}, want: `{"orbit":[{"world":"000000000000abcd","seed":"abcdef0123"}]}`},

		// REALITY without a public-key is plain TLS to mihomo, except AnyTLS with a short-id.
		{name: "reality no key", input: vless + "tls: true, reality-opts: {public-key: '', short-id: ''}}", removed: []string{"reality-opts"}, absent: []string{"reality-opts", "client-fingerprint"}},
		{name: "reality empty", input: "{name: t, type: trojan, server: t.example.com, port: 443, password: p, reality-opts: {}}", removed: []string{"reality-opts"}},
		{name: "reality anytls", input: "{name: a, type: anytls, server: a.example.com, port: 443, password: p, reality-opts: {public-key: ''}}", removed: []string{"reality-opts"}},
		{name: "reality anytls short-id", input: "{name: a, type: anytls, server: a.example.com, port: 443, password: p, reality-opts: {short-id: ab}}", problem: model.WarnInvalidCredential},
		{name: "reality key", input: vless + "reality-opts: {public-key: " + x25519 + ", short-id: ab}}", want: `{"tls":true,"client-fingerprint":"chrome"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, removed, problem := Normalize(decode(t, c.input))
			switch {
			case c.problem == "" && problem != nil:
				t.Fatalf("left out: %s: %s", problem.Code, problem.Reason)
			case c.problem != "" && problem == nil:
				t.Fatalf("kept, want %s: %v", c.problem, p)
			case c.problem != "" && problem.Code != c.problem:
				t.Fatalf("%s: %s, want %s", problem.Code, problem.Reason, c.problem)
			case problem != nil:
				return
			}
			if len(removed) != len(c.removed) {
				t.Errorf("removed %q, want %q", removed, c.removed)
			}
			for index, prefix := range c.removed {
				if index < len(removed) && !strings.HasPrefix(removed[index], prefix) {
					t.Errorf("removed %q, want %q", removed[index], prefix)
				}
			}
			for _, key := range c.absent {
				if _, set := p.Get(key); set {
					t.Errorf("%s is still set: %v", key, p)
				}
			}
			if c.want != "" {
				var want map[string]any
				if err := json.Unmarshal([]byte(c.want), &want); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(p)
				var got map[string]any
				json.Unmarshal(data, &got)
				for key, value := range want {
					g, _ := json.Marshal(got[key])
					w, _ := json.Marshal(value)
					if string(g) != string(w) {
						t.Errorf("%s = %s, want %s", key, g, w)
					}
				}
			}
		})
	}
}

// naive_options.go refuses each of these; the proxy works without them.
func TestNormalizeNaive(t *testing.T) {
	pem := selfSigned(t)
	input := decode(t, `{name: n, type: naive, server: n.example.com, port: 443, username: u, password: p,
  private-key: "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
  certificate: "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n",
  udp-over-tcp-version: 2, quic-congestion-control: bbr, quic-session-receive-window: 1000,
  stream-receive-window: 4294967296, insecure-concurrency: -1,
  headers: {X-Ok: a, Host: h.example.com, padding: "1", -connect-authority: x, "Bad Name": v, x-ok: b, X-Line: "a\nb", X-Flag: true}}`)
	p, removed, problem := Normalize(input)
	if problem != nil {
		t.Fatalf("left out: %v", problem)
	}
	for _, key := range []string{"private-key", "certificate", "udp-over-tcp-version", "quic-congestion-control",
		"quic-session-receive-window", "stream-receive-window", "insecure-concurrency"} {
		if _, set := p.Get(key); set {
			t.Errorf("%s is still set", key)
		}
	}
	headers, _ := json.Marshal(p.Value("headers"))
	if string(headers) != `{"X-Ok":"a","X-Flag":"true"}` {
		t.Errorf("headers %s", headers)
	}
	if len(removed) != 13 {
		t.Errorf("removed %d: %q", len(removed), removed)
	}

	// With QUIC and UDP over TCP, the same options stay; a CA certificate in PEM does too.
	input = decode(t, `{name: n, type: naive, server: n.example.com, port: 443, quic: true, udp: true, udp-over-tcp: true,
  udp-over-tcp-version: 2, quic-congestion-control: bbr, quic-session-receive-window: 1000, stream-receive-window: 4294967296}`)
	input.Set("certificate", pem)
	p, removed, problem = Normalize(input)
	if problem != nil || len(removed) != 0 {
		t.Fatalf("problem %v, removed %q", problem, removed)
	}
	if p.String("certificate") != pem {
		t.Error("the CA certificate was removed")
	}
}

func selfSigned(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// mihomo matches keys without case, so "Name" names the proxy.
func TestName(t *testing.T) {
	for input, want := range map[string]string{
		"{name: a, type: ss}":          "a",
		"{Name: B, type: ss}":          "B",
		"{NAME: 1, type: ss}":          "1",
		"{Name: C, name: c, type: ss}": "c",
		"{type: ss}":                   "",
	} {
		if got := Name(decode(t, input)); got != want {
			t.Errorf("Name(%s) = %q, want %q", input, got, want)
		}
	}
	p, _, problem := Normalize(decode(t, "{Name: B, type: ss, server: s.example.com, port: 1, cipher: none}"))
	if problem != nil || p.String("name") != "B" {
		t.Errorf("Normalize kept %v, %v", p, problem)
	}
}
