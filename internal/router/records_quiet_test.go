package router

import (
	"fmt"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

func TestCoreLogLeavesOrdinaryConnectionFailuresToTheRecords(t *testing.T) {
	r := NewRecords()
	r.Record(true)
	sink := logSink{r: r}
	const dial = `time="2026-11-21T06:00:00Z" level=warning msg="[TCP] dial @global (match RuleSet/geosite-cn) 192.168.1.23:%d --> www.example.com:443 error: dial tcp 203.0.113.9:443: i/o timeout"` + "\n"
	for port := range 2 {
		_, _ = fmt.Fprintf(&sink, dial, 51234+port)
	}
	_, _ = sink.Write([]byte(`level=warning msg="because group:auto failed multiple times, activate health check"` + "\n" +
		`level=warning msg="[UDP] Resolve Ip error: couldn't find ip"` + "\n" +
		`level=warning msg="handshake failed: unexpected response status: 407"` + "\n" +
		`time="2026-11-21T06:36:01Z" level=warning msg="Mihomo shutting down"` + "\n"))
	if logs := r.Logs(0, 10, "", "", ""); len(logs) != 0 {
		t.Fatal("ordinary traffic reached the log", logs)
	}
	conns := r.Page("connections", 0, 10).([]control.Connection)
	if len(conns) != 1 || conns[0].Result != "failed" || conns[0].Source != "192.168.1.23" || conns[0].Outbound != "@global" || conns[0].Rule != "RuleSet,geosite-cn" {
		t.Fatal("a retried failure should leave one record", conns)
	}
	if hits := r.RuleHits(); hits["geosite-cn"].Count != 1 {
		t.Fatal(hits)
	}
	if routes := r.Routes(8); len(routes) != 0 {
		t.Fatal("a failed dial is not a route", routes)
	}
	// The core's own outbound being refused is a setup problem, not traffic.
	_, _ = sink.Write([]byte(`level=warning msg="[TCP] dial DIRECT 192.168.1.23:5000 --> 198.51.100.1:443 error: dial tcp 198.51.100.1:443: connect: permission denied"` + "\n"))
	if logs := r.Logs(0, 10, "", "", ""); len(logs) != 1 || logs[0].Message != "core_outbound_blocked" {
		t.Fatal(logs)
	}
}

func TestFailedDialsStayOutWhenRecordingIsOff(t *testing.T) {
	r := NewRecords()
	r.Failed(control.Connection{Source: "192.168.1.23", Destination: "x:443", Outbound: "@global", Created: 1, Closed: 1, Result: "failed"})
	if conns := r.Page("connections", 0, 10).([]control.Connection); len(conns) != 0 {
		t.Fatal(conns)
	}
}
