package router

import (
	"strings"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

// Running out of descriptors drops connections, so it reaches the log even from a dial that
// otherwise only becomes a connection record.
func TestCoreLogNamesDescriptorExhaustion(t *testing.T) {
	r := NewRecords()
	r.Record(true)
	sink := logSink{r: r}
	const secret = "private.example.com"
	for range 3 {
		_, _ = sink.Write([]byte(`time="2026-11-22T06:00:00Z" level=warning msg="[TCP] dial @global (match Match/) 192.168.1.23:51234 --> ` + secret + `:443 error: dial tcp 203.0.113.9:443: socket: too many open files"` + "\n"))
	}
	logs := r.Logs(0, 10, "", "", "")
	if len(logs) != 1 || logs[0].Message != "core_file_limit_reached" || logs[0].Level != "warn" || logs[0].Count != 3 {
		t.Fatal("repeats should fold into one entry", logs)
	}
	if strings.Contains(logs[0].Message+logs[0].Detail, secret) {
		t.Fatal(logs)
	}
	if conns := r.Page("connections", 0, 10).([]control.Connection); len(conns) != 1 || conns[0].Result != "failed" {
		t.Fatal("the failed dial should still leave its record", conns)
	}
	for _, line := range []string{
		`time="2026-11-22T06:00:01Z" level=error msg="accept tcp [::]:19762: accept4: too many open files"`,
		`level=error msg="listen udp 127.0.0.1:19764: socket: too many open files in system"`,
	} {
		r := NewRecords()
		s := logSink{r: r}
		_, _ = s.Write([]byte(line + "\n"))
		if logs := r.Logs(0, 10, "", "", ""); len(logs) != 1 || logs[0].Message != "core_file_limit_reached" || logs[0].Level != "error" {
			t.Fatal(line, logs)
		}
	}
	s := logSink{r: NewRecords()}
	_, _ = s.Write([]byte("FATAL[0000] start inbound: socket: too many open files\n"))
	if s.terminal != "core_file_limit_reached" {
		t.Fatal("a core that exits for lack of descriptors should say so", s.terminal)
	}
}
