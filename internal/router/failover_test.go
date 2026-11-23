package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

type trialNodes struct {
	latency map[string]uint64
	groups  []control.Group
	probes  []string
	local   error
}

func (f *trialNodes) Groups(context.Context) ([]control.Group, error)            { return f.groups, f.local }
func (f *trialNodes) Version(context.Context) (string, error)                    { return "fixture", f.local }
func (f *trialNodes) Delays(context.Context) (map[string]control.Latency, error) { return nil, f.local }
func (f *trialNodes) Measure(_ context.Context, id string) (control.Latency, error) {
	f.probes = append(f.probes, id)
	if delay, ok := f.latency[id]; ok {
		return control.Latency{Delay: delay, Measured: time.Now().Unix()}, nil
	}
	return control.Latency{}, control.ErrProbeFailed
}
func testNodeID(char string) string { return "node:" + strings.Repeat(char, 32) }

func TestFailoverUsesReachablePeerOfPureSelector(t *testing.T) {
	a, b, c := testNodeID("1"), testNodeID("2"), testNodeID("3")
	g := control.Group{Tag: Global, Type: "selector", Selectable: true, Selected: a, Members: []string{a, b, c}}
	if !eligibleGroup(g, "global") || eligibleGroup(g, "smart") {
		t.Fatal("wrong mode")
	}
	mixed := g
	mixed.Members = []string{a, b, "@direct"}
	if eligibleGroup(mixed, "global") {
		t.Fatal("changed mixed group")
	}
	mixed = g
	mixed.Type = "urltest"
	if eligibleGroup(mixed, "global") {
		t.Fatal("replaced subscription automation")
	}

	f := &trialNodes{latency: map[string]uint64{b: 32, c: 14}}
	ctx := context.Background()
	one, next := probeFailover(ctx, f, g, nodeFailure{})
	if next != "" || one.misses != 1 || len(f.probes) != 1 {
		t.Fatal("first miss switched", one, next)
	}
	_, next = probeFailover(ctx, f, g, one)
	if next != c {
		t.Fatal("did not choose fastest freshly reachable peer", next)
	}

	f.latency = map[string]uint64{a: 0}
	if state, next := probeFailover(ctx, f, g, one); state.misses != 0 || next != "" {
		t.Fatal("misclassified a sub-millisecond measurement")
	}
	f.latency = nil
	_, next = probeFailover(ctx, f, g, one)
	if next != "" {
		t.Fatal("external test outage switched nodes")
	}
	f.local = errors.New("control not answering")
	state, next := probeFailover(ctx, f, g, one)
	if next != "" || state.misses != 0 {
		t.Fatal("core failure counted as node failure")
	}
}

func TestLateFailoverLosesToManualSelection(t *testing.T) {
	a, b, c := testNodeID("1"), testNodeID("2"), testNodeID("3")
	g := control.Group{Tag: "subscription-group", Type: "selector", Selectable: true, Selected: a, Members: []string{a, b, c}}
	f := &trialNodes{groups: []control.Group{g}}
	last := make(chan string, 5)
	m, stop := runningTest(t, fakeService{selectNode: func(_ context.Context, _ string, id string) error { last <- id; return nil }}, time.Second)
	defer stop()
	eventually(t, func() bool { return m.Status().Phase == "running" })
	m.mu.Lock()
	on := true
	m.state.Intent.Policy.AutoFailover = &on
	m.mu.Unlock()

	token, _, _, _ := m.ReviewToken("primary")
	if _, e := m.Choose("primary", g.Tag, c); e != nil {
		t.Fatal(e)
	}
	m.acceptFailover(context.Background(), f, "primary", g, b, token)
	eventually(t, func() bool { return m.GroupState("primary")[g.Tag].Applied == c })
	if selected := <-last; selected != c {
		t.Fatal("late result replaced manual choice", selected)
	}
	f.groups[0].Selected = c
	token, _, _, _ = m.ReviewToken("primary")
	m.acceptFailover(context.Background(), f, "primary", g, b, token)
	if m.Status().Intent.Choices["primary"][g.Tag] != c {
		t.Fatal("ignored changed live member")
	}
	current := f.groups[0]
	m.acceptFailover(context.Background(), f, "primary", current, b, token)
	eventually(t, func() bool { return m.GroupState("primary")[g.Tag].Applied == b })
	if st := m.Status(); st.Intent.Choices["primary"][g.Tag] != b || st.Applied != st.Intent.Revision {
		t.Fatal("confirmed choice or applied revision lost", st)
	}
	token, _, _, _ = m.ReviewToken("primary")
	inactive, cancel := context.WithCancel(context.Background())
	cancel()
	m.acceptFailover(inactive, f, "primary", current, a, token)
	if m.Status().Intent.Choices["primary"][g.Tag] != b {
		t.Fatal("canceled source switched")
	}
}
