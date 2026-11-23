package router

import (
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

func TestDeniedOperation(t *testing.T) {
	for in, want := range map[string]string{
		`time="x" level=error msg="[tcp] dial direct (match ruleset/cn) 10.0.0.2:5000 --> api.example.com:443 error: dial tcp 1.2.3.4:443: connect: permission denied"`: "tcp dial direct match ruleset/cn <addr> --> <host> error: dial tcp <addr>: connect",
		`level=error msg="start udp listener error: listen udp [::]:53: bind: permission denied"`:                                                                       "start udp listener error: listen udp <addr>: bind",
		`level=error msg="open /etc/hyper/secret/deep/file.yaml: operation not permitted"`:                                                                           "open /etc/hyper/...",
		`level=error msg="mystery"`: "",
	} {
		if got := deniedOperation(in); got != want {
			t.Errorf("%s\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRoutesFollowTheRunningCore(t *testing.T) {
	r := NewRecords()
	r.connections = append(r.connections, control.Connection{ID: "a", Outbound: "node:old", Chain: []string{"node:old", "group:old"}, Created: 1})
	if len(r.Routes(6)) != 1 {
		t.Fatal("route before restart")
	}
	r.ruleHits["Streaming"] = &RuleHit{Count: 1}
	r.CoreStarted("a")
	if got := r.Routes(6); len(got) != 0 {
		t.Fatalf("an earlier configuration's route survived: %+v", got)
	}
	r.ruleHits["Streaming"] = &RuleHit{Count: 1}
	r.CoreStarted("a")
	if len(r.ruleHits) != 1 {
		t.Fatal("a restart of the same subscription keeps its rule set hits")
	}
	r.CoreStarted("b")
	if len(r.ruleHits) != 0 || len(r.connections) != 1 {
		t.Fatal("a switch drops rule set hits and keeps the connection history")
	}
}
