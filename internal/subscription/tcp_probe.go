package subscription

import (
	"strings"
)

// TCPProbeEndpoint describes the imported server's TCP carrier, not the
// application traffic that a QUIC tunnel might carry after it connects. A chained
// node is reached through another proxy, so the router cannot dial it itself.
func (n Node) TCPProbeEndpoint() (network string, port int, ok bool) {
	if n.Special || n.Dialer() != "" || !hostValid(n.Server) || n.Port < 1 || n.Port > 65535 {
		return "", 0, false
	}
	m := n.Outbound
	carrier := field(m, "network")
	switch n.Protocol {
	case "snell", "shadowsocksr", "anytls", "socks5", "http", "https", "ssh":
	case "vless", "vmess", "trojan":
		switch carrier {
		case "", "tcp", "ws", "grpc", "h2", "http", "httpupgrade", "xhttp":
		default:
			return "", 0, false
		}
	case "naive":
		if m["quic"] == true {
			return "", 0, false
		}
	case "shadowsocks":
		switch field(m, "plugin") {
		case "", "obfs", "shadow-tls", "restls":
		case "v2ray-plugin":
			if mode := field(object(m["plugin-opts"]), "mode"); mode != "" && mode != "websocket" {
				return "", 0, false
			}
		default:
			return "", 0, false
		}
	case "nowhere":
		if carrier != "" && carrier != "tcp" {
			return "", 0, false
		}
	case "mieru":
		if strings.EqualFold(field(m, "transport"), "UDP") {
			return "", 0, false
		}
	default:
		return "", 0, false
	}
	if carrier == "udp" {
		return "", 0, false
	}
	return "tcp", n.Port, true
}
