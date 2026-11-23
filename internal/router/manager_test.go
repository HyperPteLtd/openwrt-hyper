package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

type fakeStore struct {
	i    Intent
	fail bool
}

func (f *fakeStore) Load() (Intent, error) { return f.i, nil }
func (f *fakeStore) Save(i Intent) error {
	if f.fail {
		return errors.New("disk full")
	}
	f.i = i
	return nil
}

type fakeService struct {
	start      func(context.Context, Intent) error
	direct     func(context.Context) error
	fault      func(context.Context) (bool, error)
	health     func(context.Context) error
	selectNode func(context.Context, string, string) error
}

func (f fakeService) Start(c context.Context, i Intent) error {
	if f.start != nil {
		return f.start(c, i)
	}
	return nil
}
func (f fakeService) Direct(c context.Context) error {
	if f.direct != nil {
		return f.direct(c)
	}
	return nil
}
func (f fakeService) Fault(c context.Context) (bool, error) {
	if f.fault != nil {
		return f.fault(c)
	}
	return false, f.Direct(c)
}
func (f fakeService) Healthy(c context.Context) error {
	if f.health != nil {
		return f.health(c)
	}
	return nil
}
func (f fakeService) Select(c context.Context, g, n string) error { return f.selectNode(c, g, n) }
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for !fn() {
		if time.Now().After(until) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func runningTest(t *testing.T, f fakeService, limit time.Duration) (*Manager, func()) {
	t.Helper()
	i := initial()
	i.Enabled = true
	i.Source = "primary"
	m := New(&fakeStore{i: i}, f, Timing{limit, 100 * time.Millisecond, 5 * time.Millisecond, time.Second})
	ctx, end := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	return m, func() {
		end()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("service did not stop")
		}
	}
}
func TestStopDuringStartKeepsStartDuringCleanup(t *testing.T) {
	starts := make(chan int, 3)
	cleanup := make(chan struct{}, 1)
	resume := make(chan struct{})
	counter := 0
	releaseCount := 0
	f := fakeService{start: func(c context.Context, _ Intent) error {
		counter++
		starts <- counter
		if counter == 1 {
			<-c.Done()
			return c.Err()
		}
		return nil
	}, direct: func(c context.Context) error {
		releaseCount++
		if releaseCount == 2 {
			cleanup <- struct{}{}
			select {
			case <-resume:
			case <-c.Done():
				return c.Err()
			}
		}
		return nil
	}}
	m, end := runningTest(t, f, time.Second)
	defer end()
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("missing startup")
	}
	_, e := m.Change(nil, func(i *Intent) error { i.Enabled = false; return nil })
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-cleanup:
	case <-time.After(time.Second):
		t.Fatal("missing cleanup")
	}
	_, e = m.Change(nil, func(i *Intent) error { i.Enabled = true; return nil })
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-starts:
		t.Fatal("overlapped startup")
	default:
	}
	close(resume)
	eventually(t, func() bool { return m.Status().Phase == "running" })
	if <-starts != 2 {
		t.Fatal("restart lost")
	}
}
func TestHealthNeedsThreeMissesAndSuccessfulCleanup(t *testing.T) {
	var mu sync.Mutex
	miss := 0
	clean := 0
	m, end := runningTest(t, fakeService{health: func(c context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		miss++
		return errors.New("control unavailable")
	}, direct: func(c context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		clean++
		if clean > 1 {
			return errors.New("DNS restoration failed")
		}
		return nil
	}}, time.Second)
	defer end()
	eventually(t, func() bool { return m.Status().Phase == "release_failed" })
	mu.Lock()
	defer mu.Unlock()
	if miss != 3 || clean != 2 || !m.Status().FailOpen {
		t.Fatalf("misses %d releases %d", miss, clean)
	}
}
func TestStartupDeadlineCleansOnce(t *testing.T) {
	var mu sync.Mutex
	n := 0
	m, end := runningTest(t, fakeService{start: func(c context.Context, _ Intent) error { <-c.Done(); return c.Err() }, direct: func(context.Context) error { mu.Lock(); n++; mu.Unlock(); return nil }}, 20*time.Millisecond)
	defer end()
	eventually(t, func() bool { return m.Status().Phase == "fail_open" })
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Fatal(n)
	}
}
func TestIndependentSelectionLanesAndDiskFailure(t *testing.T) {
	entered := make(chan string, 5)
	block := make(chan struct{})
	m, end := runningTest(t, fakeService{selectNode: func(c context.Context, g, n string) error {
		entered <- g + n
		if g == "A" && n == "1" {
			select {
			case <-block:
			case <-c.Done():
				return c.Err()
			}
		}
		return nil
	}}, time.Second)
	defer end()
	eventually(t, func() bool { return m.Status().Phase == "running" })
	if _, e := m.Choose("primary", "A", "1"); e != nil {
		t.Fatal(e)
	}
	if <-entered != "A1" {
		t.Fatal("wrong first request")
	}
	_, _ = m.Choose("primary", "A", "2")
	_, _ = m.Choose("primary", "B", "3")
	if <-entered != "B3" {
		t.Fatal("independent group waited")
	}
	close(block)
	eventually(t, func() bool { g := m.GroupState("primary"); return g["B"].Applied == "3" && g["A"].Applied == "2" })
	m.mu.Lock()
	m.store.(*fakeStore).fail = true
	m.mu.Unlock()
	rev := m.Status().Intent.Revision
	if _, e := m.Choose("primary", "A", "4"); e == nil || rev != m.Status().Intent.Revision {
		t.Fatal("failed write published")
	}
	stale := rev - 1
	if _, e := m.Change(&stale, func(*Intent) error { return nil }); e == nil {
		t.Fatal("stale operation applied")
	}
}
func TestUnreadableStateRemainsUntouched(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "intent.json")
	for _, s := range []string{"{", "{\"schema_version\":42}"} {
		if e := os.WriteFile(file, []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
		m := New(Disk{file}, fakeService{}, DefaultTiming)
		if _, e := m.Change(nil, func(*Intent) error { return nil }); e == nil {
			t.Fatal("wrote through invalid schema")
		}
		b, _ := os.ReadFile(file)
		if string(b) != s {
			t.Fatal("old state was rewritten")
		}
	}
}

func TestAtomicIntentCheckpointAndFailedPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	disk := Disk{path}
	old := initial()
	old.Revision = 1
	if e := disk.Save(old); e != nil {
		t.Fatal(e)
	}
	newer := old
	newer.Revision++
	for _, afterRename := range []bool{false, true} {
		e := disk.save(newer, func(p string, b []byte, m os.FileMode) error {
			if p == path {
				if afterRename {
					if e := Atomic(p, b, m); e != nil {
						return e
					}
				}
				return errors.New("injected filesystem failure")
			}
			return Atomic(p, b, m)
		})
		if e == nil {
			t.Fatal("acknowledged failed commit")
		}
		got, e := disk.Load()
		if e != nil || got.Revision != old.Revision {
			t.Fatalf("previous lost %v %v", got, e)
		}
	}
	if e := disk.Save(newer); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := disk.Load()
	if e != nil || got.Revision != 1 {
		t.Fatalf("checkpoint not readable %v %v", got, e)
	}
}

func TestProtectedFaultRequiresExplicitStopToRestoreDirect(t *testing.T) {
	var mu sync.Mutex
	holds, direct := 0, 0
	m, end := runningTest(t, fakeService{
		fault:  func(context.Context) (bool, error) { mu.Lock(); defer mu.Unlock(); holds++; return holds > 1, nil },
		direct: func(context.Context) error { mu.Lock(); defer mu.Unlock(); direct++; return nil },
		health: func(context.Context) error { return errors.New("control unavailable") },
	}, time.Second)
	defer end()
	eventually(t, func() bool { return m.Status().Phase == "protected" })
	if m.Status().FailOpen {
		t.Fatal("reported direct while protected")
	}
	mu.Lock()
	count := direct
	mu.Unlock()
	if count != 0 {
		t.Fatalf("released before user stop: %d", count)
	}
	if _, err := m.Change(nil, func(i *Intent) error { i.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Phase == "direct" })
	mu.Lock()
	defer mu.Unlock()
	if direct != 1 || m.Status().Problem != "" {
		t.Fatalf("after stop releases=%d stale error=%s", direct, m.Status().Problem)
	}
}

func TestFaultPolicyMatchesRoutingOrder(t *testing.T) {
	i := Intent{Mode: "smart", Policy: Policy{DefaultDirect: true, Devices: []DeviceRule{
		{"10.0.0.1/8", "@proxy"}, {"02:00:00:00:00:01", "@direct"}, {"fd01::1/64", "group:device"},
	}}}
	s, e := deviceGuardPolicy(i)
	if e != nil || s != "mac 02:00:00:00:00:01 direct\n6 fd01::/64 block\n4 10.0.0.0/8 block\ndefault direct\n" {
		t.Fatal(s, e)
	}
	i.Mode = "global"
	s, e = deviceGuardPolicy(i)
	if e != nil || s != "mac 02:00:00:00:00:01 direct\ndefault block\n" {
		t.Fatal(s, e)
	}
}

func TestDirectDevicesSkipTheTakeoverUnlessAnEarlierRuleRoutesThem(t *testing.T) {
	i := Intent{Mode: "smart", Policy: Policy{DefaultDirect: true, Devices: []DeviceRule{
		{"192.168.1.10", "@direct"}, {"02:00:00:00:00:AB", "@direct"}, {"fd00::10/64", "@direct"}, {"192.168.1.10/32", "@direct"},
		// A proxy exception only exempts a device from default direct; it shadows nothing.
		{"10.0.0.1/8", "@proxy"}, {"10.0.0.5", "@direct"},
		{"192.168.2.0/24", "group:x"}, {"192.168.2.7", "@direct"}, {"192.168.3.7", "@direct"},
		// After a routed IP rule a MAC device may hold that address, and the reverse.
		{"02:00:00:00:00:02", "@direct"}, {"02:00:00:00:00:03", "@reject"}, {"192.168.4.1", "@direct"},
	}}}
	s, e := directDevices(i)
	if e != nil || s != "4 192.168.1.10/32\nmac 02:00:00:00:00:ab\n6 fd00::/64\n4 10.0.0.5/32\n4 192.168.3.7/32\n" {
		t.Fatalf("%q %v", s, e)
	}
	// Global mode drops every other device rule, so nothing shadows the direct ones.
	i.Mode = "global"
	s, e = directDevices(i)
	if e != nil || s != "4 192.168.1.10/32\nmac 02:00:00:00:00:ab\n6 fd00::/64\n4 10.0.0.5/32\n4 192.168.2.7/32\n4 192.168.3.7/32\nmac 02:00:00:00:00:02\n4 192.168.4.1/32\n" {
		t.Fatalf("%q %v", s, e)
	}
	if _, e = directDevices(Intent{Mode: "smart", Policy: Policy{Devices: []DeviceRule{{"lan", "@direct"}}}}); e == nil {
		t.Fatal("accepted an invalid device")
	}
	// Turning a device direct re-applies the firewall; moving it between groups only reloads the core.
	n := Network{Method: "tproxy"}
	group := func(target string) Intent {
		return Intent{Mode: "smart", Policy: Policy{Devices: []DeviceRule{{"192.168.1.10", target}}}}
	}
	if networkKey(group("group:a"), n) != networkKey(group("group:b"), n) || networkKey(group("group:a"), n) == networkKey(group("@direct"), n) {
		t.Fatal("takeover key does not follow direct devices")
	}
}

func TestCoreLogsKeepNetworkErrorsSeparateFromInformationalEvents(t *testing.T) {
	records := NewRecords()
	sink := logSink{r: records}
	sink.Write([]byte("INFO[0000] started with example-error.test\nWARN[0000] dns: no such host example-error.test\nERROR network is unreachable\nERROR network is unreachable\n"))
	entries := records.Logs(0, 10, "", "", "")
	if len(entries) != 2 || entries[0].Message != "network_unreachable" || entries[0].Count != 2 ||
		entries[1].Message != "dns_name_not_found" || entries[1].Level != "warn" {
		t.Fatal(entries)
	}
}

func TestChangeWithoutDifferenceKeepsRevision(t *testing.T) {
	m := New(&memStoreNoop{}, nil, DefaultTiming)
	r1, e := m.Change(nil, func(i *Intent) error { i.Mode = "global"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	m.state.Phase = "running"
	r2, e := m.Change(nil, func(i *Intent) error { i.Mode = "global"; return nil })
	if e != nil || r2 != r1 {
		t.Fatalf("no-op change bumped revision %d -> %d (%v)", r1, r2, e)
	}
}

type memStoreNoop struct{ v Intent }

func (s *memStoreNoop) Load() (Intent, error) { return initial(), nil }
func (s *memStoreNoop) Save(i Intent) error   { s.v = i; return nil }

type reloadService struct {
	fakeService
	reload func(context.Context, Intent) error
}

func (r reloadService) Reload(c context.Context, i Intent) error { return r.reload(c, i) }

func TestModeChangeReloadsInPlace(t *testing.T) {
	var mu sync.Mutex
	starts, reloads, directs := 0, 0, 0
	f := reloadService{fakeService{
		start:  func(context.Context, Intent) error { mu.Lock(); starts++; mu.Unlock(); return nil },
		direct: func(context.Context) error { mu.Lock(); directs++; mu.Unlock(); return nil },
	}, func(_ context.Context, i Intent) error {
		mu.Lock()
		defer mu.Unlock()
		if i.Mode != "global" {
			return errors.New("reload got a stale intent")
		}
		reloads++
		return nil
	}}
	i := initial()
	i.Enabled = true
	i.Source = "primary"
	m := New(&fakeStore{i: i}, f, Timing{time.Second, 100 * time.Millisecond, 5 * time.Millisecond, time.Second})
	ctx, end := context.WithCancel(context.Background())
	defer end()
	go m.Run(ctx)
	eventually(t, func() bool { return m.Status().Phase == "running" })
	mu.Lock()
	before := directs
	mu.Unlock()
	rev, e := m.Change(nil, func(i *Intent) error { i.Mode = "global"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, func() bool { s := m.Status(); return s.Applied == rev && s.Phase == "running" })
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 || reloads != 1 || directs != before {
		t.Fatalf("starts=%d reloads=%d directs %d->%d", starts, reloads, before, directs)
	}
}

func TestFailedReloadFallsBackToRestart(t *testing.T) {
	var mu sync.Mutex
	starts := 0
	f := reloadService{fakeService{
		start: func(context.Context, Intent) error { mu.Lock(); starts++; mu.Unlock(); return nil },
	}, func(context.Context, Intent) error { return ErrNeedsRestart }}
	i := initial()
	i.Enabled = true
	i.Source = "primary"
	m := New(&fakeStore{i: i}, f, Timing{time.Second, 100 * time.Millisecond, 5 * time.Millisecond, time.Second})
	ctx, end := context.WithCancel(context.Background())
	defer end()
	go m.Run(ctx)
	eventually(t, func() bool { return m.Status().Phase == "running" })
	rev, e := m.Change(nil, func(i *Intent) error { i.Mode = "global"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, func() bool { s := m.Status(); return s.Applied == rev && s.Phase == "running" })
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 {
		t.Fatalf("expected a full restart, got %d starts", starts)
	}
}

// A selection of a group the subscription no longer has resets: it neither shows as pending
// nor keeps a later hot switch from being acknowledged.
func TestSelectionOfARemovedGroupDoesNotHoldBackOthers(t *testing.T) {
	i := initial()
	i.Enabled, i.Source = true, "primary"
	i.Choices = map[string]map[string]string{"primary": {"group:gone": "node:a"}}
	m := New(&fakeStore{i: i}, fakeService{selectNode: func(context.Context, string, string) error { return nil }}, Timing{time.Second, 100 * time.Millisecond, 5 * time.Millisecond, time.Second})
	ctx, end := context.WithCancel(context.Background())
	defer end()
	go m.Run(ctx)
	eventually(t, func() bool { return m.Status().Phase == "running" })
	rev, err := m.Choose("primary", "group:live", "node:b")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Applied == rev })
	// Once the profile without the group is committed, its selection goes: should the group
	// come back without the member, nothing blocks its update.
	m.PruneChoices("primary", subscription.Profile{Groups: []subscription.Group{{ID: "group:live"}}})
	choices := m.Status().Intent.Choices["primary"]
	if _, ok := choices["group:gone"]; ok || choices["group:live"] != "node:b" {
		t.Fatal(choices)
	}
	before := m.Status().Intent.Revision
	m.PruneChoices("primary", subscription.Profile{Groups: []subscription.Group{{ID: "group:live"}}})
	if m.Status().Intent.Revision != before {
		t.Fatal("nothing to prune changed the intent")
	}
}
