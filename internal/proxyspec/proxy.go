// Package proxyspec is the router-safety policy for mihomo proxies. github.com/openweir/subconv
// converts every subscription and checks that each proxy loads; the router checks what only it
// can decide: that a proxy runs nothing but its own connections.
package proxyspec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrOption = errors.New("unsupported_node")
var ErrProtocol = errors.New("mihomo_protocol_unsupported")

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

// Safe applies the router-safety policy to a whole mihomo proxy, whatever format it came
// from: a protocol the router knows to run only its own connections, with no option that
// reaches into the router (Unsafe). A dialer-proxy stays a name; the document resolves it.
func Safe(v map[string]any) error {
	switch str(v, "type") {
	case "http", "ss", "ssr", "socks5", "vless", "vmess", "trojan", "snell", "hysteria", "hysteria2", "tuic", "anytls", "naive", "nowhere", "mieru", "ssh", "wireguard", "juicity":
	default:
		return ErrProtocol
	}
	if unsafe(v, 0) {
		return ErrOption
	}
	return nil
}

// Unsafe is the router-safety policy for one proxy option, whatever format the node came
// from: a subscription cannot make the core read the router's files, bind its interfaces,
// set routing marks, enter network namespaces or run local plugins.
func Unsafe(key string, value any) bool {
	return unsafe(map[string]any{key: value}, 0)
}

func unsafe(v any, depth int) bool {
	if depth > 32 {
		return true
	}
	switch m := v.(type) {
	case map[string]any:
		for k, x := range m {
			switch k {
			case "interface-name", "routing-mark", "netns", "ip-stack", "ca", "ca-path", "private-key-path", "key-path", "client-cert", "client-key", "wg-opts", "plugin-path":
				return true
			case "dialer-proxy":
				// A chain names a proxy or group of the same document, which resolves it.
				if depth != 0 || str(m, k) == "" {
					return true
				}
			case "certificate":
				if !strings.HasPrefix(str(m, k), "-----BEGIN CERTIFICATE-----") {
					return true
				}
			case "private-key":
				if depth != 0 || str(m, "type") != "wireguard" {
					return true
				}
			}
			if strings.HasSuffix(k, "-file") || strings.HasSuffix(k, "-path") || unsafe(x, depth+1) {
				return true
			}
		}
	case []any:
		for _, x := range m {
			if unsafe(x, depth+1) {
				return true
			}
		}
	}
	return false
}

func Port(v any) (int, bool) {
	p, e := strconv.Atoi(fmt.Sprint(v))
	return p, e == nil && p >= 1 && p <= 65535
}
