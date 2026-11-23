package subscription

import (
	"encoding/json"
	"testing"
)

func TestTCPProbeRequiresConfiguredCarrier(t *testing.T) {
	for _, tc := range []struct {
		proto, opts, network string
		port                 int
	}{
		{"hysteria2", "{}", "", 0}, {"tuic", "{}", "", 0}, {"vless", `{"network":"quic"}`, "", 0}, {"vless", `{"network":"ws"}`, "tcp", 443}, {"http", `{"network":"udp"}`, "", 0}, {"https", "{}", "tcp", 443}, {"naive", `{"quic":true}`, "", 0}, {"nowhere", `{"network":"udp"}`, "", 0}, {"nowhere", `{"network":"tcp"}`, "tcp", 443}, {"shadowsocks", `{"plugin":"kcptun"}`, "", 0}, {"shadowsocks", `{"plugin":"v2ray-plugin","plugin-opts":{"mode":"quic"}}`, "", 0}, {"shadowsocks", `{"plugin":"v2ray-plugin","plugin-opts":{"mode":"websocket"}}`, "tcp", 443}, {"mieru", `{"transport":"UDP"}`, "", 0}, {"mieru", `{"transport":"TCP"}`, "tcp", 443}, {"snell", "{}", "tcp", 443}, {"anytls", "{}", "tcp", 443}, {"mtproxy", "{}", "", 0}, {"unknown", "{}", "", 0},
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.opts), &m); err != nil {
			t.Fatal(err)
		}
		n := Node{Protocol: tc.proto, Server: "node.example", Port: 443, Outbound: m}
		network, port, ok := n.TCPProbeEndpoint()
		if network != tc.network || port != tc.port || ok != (tc.port != 0) || n.Summary().TCPProbe != ok {
			t.Errorf("%s %s: %s %d %t", tc.proto, tc.opts, network, port, ok)
		}
	}
}
