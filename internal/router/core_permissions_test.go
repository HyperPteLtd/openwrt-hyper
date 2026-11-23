package router

import (
	"strings"
	"testing"
)

func TestCorePermissionRecordsNameOperationWithoutSensitiveText(t *testing.T) {
	const secret = "hidden-provider-credential"
	for _, tc := range []struct{ line, reason string }{
		{"FATAL[0001] start service: initialize rule-set[0]: open /etc/hyper/rulesets/" + secret + ".mrs: permission denied", "core_file_permission"},
		{"FATAL[0001] set IP_TRANSPARENT: operation not permitted", "core_tproxy_socket_permission"},
		{"ERRO[0002] listenLocalConn failed with error: bind: permission denied, packet loss (rAddr=1.2.3.4:443)", "core_tproxy_udp_reply_permission"},
		{"WARN[0001] listen ip4:icmp 0.0.0.0: socket: permission denied", "core_icmp_socket_permission"},
		{"ERROR routing: set SO_MARK: operation not permitted", "core_route_mark_permission"},
		{"\x1b[31mFATAL\x1b[0m[0001] listen tcp 127.0.0.1:443: bind: permission denied", "core_listener_permission"},
		{"WARN other action for https://example.test/" + secret + ": permission denied", "permission_denied"},
	} {
		s := logSink{r: NewRecords()}
		_, _ = s.Write([]byte(tc.line + "\n"))
		j := s.r.Logs(0, 1, "", "", "")
		if len(j) != 1 || j[0].Message != tc.reason || strings.Contains(j[0].Message, secret) {
			t.Fatal(tc.reason, j)
		}
		if strings.Contains(tc.line, "FATAL") {
			if s.terminal != tc.reason || j[0].Level != "error" {
				t.Fatal("fatal cause lost", s.terminal, j)
			}
		} else if s.terminal != "" {
			t.Fatal("nonfatal diagnostic recorded as an exit", j)
		}
	}
}
