package proxyspec

import (
	"errors"
	"testing"
)

func TestRouterUnsafeOptionsAreRejected(t *testing.T) {
	base := map[string]any{"type": "naive", "server": "node.example", "port": 443}
	for k, v := range map[string]any{"interface-name": "wan", "routing-mark": 255, "certificate": "/tmp/local.pem", "dialer-proxy": 1, "plugin-path": "/usr/sbin/tool", "smux": map[string]any{"dialer-proxy": "other"}, "tls-opts": map[string]any{"key-file": "/etc/key"}} {
		base[k] = v
		if e := Safe(base); !errors.Is(e, ErrOption) {
			t.Fatalf("accepted %s", k)
		}
		delete(base, k)
	}
	if Safe(map[string]any{"type": "anytls", "server": "n.example", "port": 443, "reality-opts": map[string]any{"public-key": "a", "short-id": "b"}}) != nil {
		t.Fatal("safe option refused")
	}
}

func TestChainsKeepTheirDialerName(t *testing.T) {
	if e := Safe(map[string]any{"type": "http", "server": "exit.example", "port": 443, "dialer-proxy": "Front"}); e != nil {
		t.Fatal("chained node rejected", e)
	}
	if !Unsafe("interface-name", "wan") || !Unsafe("routing-mark", 1) || Unsafe("dialer-proxy", "Front") || Unsafe("udp", true) {
		t.Fatal("router-safety policy changed")
	}
	// Safety alone: completeness is the converter's concern.
	if Safe(map[string]any{"type": "vless", "server": "v.example"}) != nil || !errors.Is(Safe(map[string]any{"type": "direct"}), ErrProtocol) {
		t.Fatal("policy mixes safety with completeness")
	}
}
