package router

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/netguard"
)

func TestStartClearsConflictingPlugins(t *testing.T) {
	saved := resolveConflicts
	t.Cleanup(func() { resolveConflicts = saved })
	for _, tc := range []struct {
		name   string
		result netguard.Result
		err    error
		want   string
		events []Event
	}{
		{name: "clear"},
		{
			name:   "stopped",
			result: netguard.Result{Stopped: []string{"openclash", "passwall"}},
			events: []Event{{Source: "service", Level: "warn", Message: "conflict_services_stopped", Detail: "openclash, passwall"}},
		},
		{
			name: "leftovers",
			result: netguard.Result{Blocked: true, Stopped: []string{"homeproxy"}, Resources: []netguard.Resource{
				{Kind: "nft", Name: "inet table passwall", Service: "passwall"},
				{Kind: "process", Name: "mihomo"},
				{Kind: "unverified", Name: "ubus"},
			}},
			want:   "network takeover blocked by: nft inet table passwall; process mihomo; unverified ubus",
			events: []Event{{Source: "service", Level: "warn", Message: "conflict_services_stopped", Detail: "homeproxy"}},
		},
		{name: "unnamed", result: netguard.Result{Blocked: true}, want: errConflictInspection.Error()},
		{name: "no report", err: errConflictInspection, want: errConflictInspection.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolveConflicts = func(context.Context) (netguard.Result, error) { return tc.result, tc.err }
			s := NewSystem()
			var got string
			if err := s.clearConflicts(context.Background()); err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("%q", got)
			}
			events := s.Records().Logs(0, 100, "", "", "")
			for i := range events {
				events[i].Time = 0
			}
			if len(events) != len(tc.events) || len(events) > 0 && !reflect.DeepEqual(events, tc.events) {
				t.Fatal(events)
			}
		})
	}
}

// An unusable nogroup entry fails the start before OpenClash, PassWall or HomeProxy is stopped.
func TestStartResolvesTheCoreGroupBeforeStoppingPlugins(t *testing.T) {
	savedGID, savedResolve := coreGID, resolveConflicts
	t.Cleanup(func() { coreGID, resolveConflicts = savedGID, savedResolve })
	stopped := 0
	resolveConflicts = func(context.Context) (netguard.Result, error) {
		stopped++
		return netguard.Result{Stopped: []string{"openclash"}}, nil
	}
	for _, fails := range []string{"invalid core group", "core group unavailable"} {
		coreGID = func() (int, error) { return 0, errors.New(fails) }
		s := NewSystem()
		gid, err := s.prepareTakeover(context.Background())
		if err == nil || err.Error() != fails || gid != 0 {
			t.Fatal(gid, err)
		}
		if stopped != 0 {
			t.Fatal("plugins were stopped for a start that cannot run the core")
		}
		if events := s.Records().Logs(0, 100, "", "", ""); len(events) != 0 {
			t.Fatal(events)
		}
	}
	coreGID = func() (int, error) { return 4242, nil }
	if gid, err := NewSystem().prepareTakeover(context.Background()); err != nil || gid != 4242 || stopped != 1 {
		t.Fatal(gid, err, stopped)
	}
	// A plugin that still holds the network fails the start after the group is known.
	resolveConflicts = func(context.Context) (netguard.Result, error) { return netguard.Result{Blocked: true}, nil }
	if _, err := NewSystem().prepareTakeover(context.Background()); !errors.Is(err, errConflictInspection) {
		t.Fatal(err)
	}
}
