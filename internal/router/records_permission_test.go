package router

import "testing"

func TestCorePermissionReasonNamesTheResource(t *testing.T) {
	for line, want := range map[string]string{
		"level=warning msg=\"[tcp] dial direct error: dial tcp 1.2.3.4:443: connect: permission denied\"": "core_outbound_blocked",
		"level=warning msg=\"[udp] write udp 0.0.0.0:1->8.8.8.8:53: sendto: operation not permitted\"":    "core_outbound_blocked",
		"level=warning msg=\"open /proc/net/tcp: permission denied\"":                                     "core_process_lookup_permission",
		"level=error msg=\"open /etc/other/file: permission denied\"":                                     "core_file_permission",
		"level=error msg=\"permission denied\"":                                                           "permission_denied",
	} {
		if got := corePermissionReason(line); got != want {
			t.Errorf("%s: %s, want %s", line, got, want)
		}
	}
}
