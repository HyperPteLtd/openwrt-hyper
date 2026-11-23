package router

import (
	"bufio"
	"context"

	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"go.yaml.in/yaml/v3"
)

func pulse() error { return Atomic(RunDir+"/pulse", []byte("ok\n"), 0600) }
func (s *System) ready(ctx context.Context) error {
	c, stop := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer stop()
	if _, e := s.Client().Version(c); e != nil {
		return errors.New("local core control unavailable")
	}
	conn, e := (&net.Dialer{}).DialContext(c, "tcp", "127.0.0.1:19763")
	if e != nil {
		return errors.New("local proxy listener unavailable")
	}
	defer conn.Close()
	deadline, _ := c.Deadline()
	_ = conn.SetDeadline(deadline)
	if _, e = conn.Write([]byte{5, 1, 0}); e != nil {
		return e
	}
	var reply [2]byte
	if _, e = io.ReadFull(conn, reply[:]); e != nil || reply != [2]byte{5, 0} {
		return errors.New("local proxy negotiation failed")
	}
	s.mu.Lock()
	dns, method, v6 := s.dns, s.method, s.family6
	s.mu.Unlock()
	if method == "tun" {
		tun, e := net.InterfaceByName("owtun0")
		if e != nil || tun.Flags&net.FlagUp == 0 {
			return errors.New("local TUN not ready")
		}
	} else if method == "tproxy" {
		if !listening("tcp", v6) || !listening("udp", v6) {
			return errors.New("transparent listeners not ready")
		}
	}
	if dns {
		return probeDNS(c, DNSPort)
	}
	return nil
}
func listening(protocol string, need6 bool) bool {
	for _, suffix := range []string{"", "6"} {
		if need6 && suffix == "" {
			continue
		}
		f, e := os.Open("/proc/net/" + protocol + suffix)
		if e != nil {
			continue
		}
		// Avoid materializing the system connection table on low-memory routers.
		sc := bufio.NewScanner(f)
		found := false
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || protocol == "tcp" && fields[3] != "0A" || protocol == "udp" && fields[3] != "07" {
				continue
			}
			addr, port, ok := strings.Cut(fields[1], ":")
			if ok && strings.EqualFold(port, "4D32") && strings.Trim(addr, "0") == "" {
				found = true
				break
			}
		}
		f.Close()
		if found {
			return true
		}
	}
	return false
}
func (s *System) beat(ctx context.Context) error {
	if e := s.ready(ctx); e != nil {
		return e
	}
	return pulse()
}
func (s *System) verify(ctx context.Context) error {
	if e := s.ready(ctx); e != nil {
		return e
	}
	// A helper stuck on the firewall or a resolver would otherwise hold the check until the
	// start deadline; even slow routers finish a healthy verify in a few seconds.
	c, stop := context.WithTimeout(ctx, verifyTimeout)
	defer stop()
	_, e := command(c, networkHelper, "verify")
	return e
}

const verifyTimeout = 20 * time.Second

// SideRouter reports a takeover on a side router (旁路由): the network helper found the IPv4
// default route on a managed LAN, so devices must use this router as gateway and DNS.
func SideRouter() bool {
	_, e := os.Stat(RunDir + "/network/side_router")
	return e == nil
}

func (s *System) Healthy(ctx context.Context) error {
	s.mu.Lock()
	audit := s.audit
	s.mu.Unlock()
	if audit != nil {
		if failure := audit.err.Load(); failure != nil {
			return *failure
		}
	}
	if _, e := os.Stat(RunDir + "/network/owned"); e != nil {
		return errors.New("network ownership lost")
	}
	return s.beat(ctx)
}

// Verify firewall, RPDB, managed LAN DNS and management access out of band.
// A slow but healthy low-power router must still renew its local heartbeat
// while nft/ubus or a LAN DNS query is in progress.
type routingAudit struct {
	err atomic.Pointer[error]
	// Fixed when the audit starts.
	helper func(context.Context, string) ([]byte, error) // the network helper
	alarm  func() bool                                   // a cheap sign that rules may be gone
	ready  func(context.Context) error                   // the core answers
	limit  *repairLimit
	log    func(message string)
}

func (s *System) newAudit() *routingAudit {
	return &routingAudit{
		helper: func(ctx context.Context, verb string) ([]byte, error) { return command(ctx, networkHelper, verb) },
		alarm:  firewallAlarm(),
		ready:  s.ready,
		limit:  &s.repairs,
		log:    func(message string) { s.records.Log("firewall", "info", message) },
	}
}

// The full check runs every 30 s, and at once when the alarm goes off.
func (a *routingAudit) run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	due := time.Now().Add(30 * time.Second)
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if !a.alarm() && time.Now().Before(due) {
			continue
		}
		e := a.check(ctx)
		if e == nil {
			a.err.Store(nil)
			misses = 0
			due = time.Now().Add(30 * time.Second)
			continue
		}
		misses++
		due = time.Time{}
		if misses >= 3 {
			a.err.Store(&e)
		}
	}
}

// check verifies the takeover. A firewall stop or restart (fw4 flush, fw3 restart) or another
// tool can remove its rules while the core, routes and DNS stay: put them back at once while
// the core is healthy, instead of releasing the takeover and starting over 30 s later.
func (a *routingAudit) check(ctx context.Context) error {
	probe, end := context.WithTimeout(ctx, 10*time.Second)
	_, e := a.helper(probe, "verify")
	end()
	if e == nil {
		return nil
	}
	probe, end = context.WithTimeout(ctx, 20*time.Second)
	defer end()
	now := time.Now()
	if a.ready(probe) != nil || !a.limit.allow(now) {
		return e
	}
	out, e := a.helper(probe, "repair")
	if e == nil && strings.TrimSpace(string(out)) == "repaired" {
		a.limit.note(now)
		a.log("firewall_rules_restored")
	}
	return e
}

// repairLimit allows ten repairs in five minutes, like OpenClash's firewall reloads, so
// Hyper never fights another tool over the rules. It outlives each core run.
type repairLimit struct {
	mu   sync.Mutex
	done []time.Time
}

func (l *repairLimit) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = slices.DeleteFunc(l.done, func(t time.Time) bool { return now.Sub(t) >= 5*time.Minute })
	return len(l.done) < 10
}
func (l *repairLimit) note(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = append(l.done, now)
}

// firewallAlarm watches for the cheap signs that the takeover's rules may be gone. fw4 replays
// its table with each load of its own ruleset, but a flush (firewall stop) or another tool
// removes it. fw3 rewrites its state file with each start, restart and reload.
func firewallAlarm() func() bool {
	backend, _ := os.ReadFile(RunDir + "/network/backend")
	switch strings.TrimSpace(string(backend)) {
	case "fw4":
		return func() bool { return nftTableGone("hyper") }
	case "fw3":
		return fileChanged("/var/run/fw3.state")
	}
	return func() bool { return false }
}

func fileChanged(path string) func() bool {
	last, _ := os.Stat(path)
	return func() bool {
		now, _ := os.Stat(path)
		changed := (last == nil) != (now == nil) || last != nil && now != nil &&
			(!os.SameFile(last, now) || !last.ModTime().Equal(now.ModTime()) || last.Size() != now.Size())
		last = now
		return changed
	}
}

// The last completed probe can be much more informative than a canceled
// socket read performed as the startup deadline expires.
func startupTimeout(ctx context.Context, stage string, last error) error {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	if last == nil {
		return fmt.Errorf("%s: %w", stage, ctx.Err())
	}
	return fmt.Errorf("%s: %w (last failed check: %w)", stage, ctx.Err(), last)
}

// errCoreExited fails a start whose core exits before it counts as running; Start reports the
// reason the core's last lines gave instead, when they gave one.
var errCoreExited = errors.New("core exited during startup")

func waitCoreReady(ctx context.Context, exited <-chan struct{}, interval time.Duration, ready func(context.Context) error) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	var last error
	for {
		select {
		case <-exited:
			return errCoreExited
		default:
		}
		err := ready(ctx)
		if ctx.Err() != nil {
			if last == nil {
				last = err
			}
			return startupTimeout(ctx, "waiting for local core readiness", last)
		}
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return startupTimeout(ctx, "waiting for local core readiness", last)
		case <-exited:
			return errCoreExited
		case <-t.C:
		}
	}
}

// confirmFailures is how many failed checks fail a confirmation. A failed check, or a heartbeat
// failed since the last one, restarts the healthy streak: one or two are a takeover settling
// (dnsmasq restarting), and the third, in a row or not, fails the start with what failed. A
// takeover that stays broken thus fails in about three seconds and one that flaps within three
// streaks, not at the start deadline. It counts checks, not time, so a slow router's slow but
// healthy checks never run out a clock.
const confirmFailures = 3

// A confirmation holds a start until full-path checks stay healthy for a whole window. The core
// and network have already passed startup and apply checks on entry. A slow first LAN check must
// keep renewing the independent heartbeat while the core is ready.
type confirmation struct {
	window, interval, heartbeat time.Duration
	check                       func(context.Context) error // the core and the LAN path
	beat                        func(context.Context) error // the core alone, between checks
	// exited closes when the core exits: the confirmation fails at once, and a check or
	// heartbeat in progress is canceled.
	exited <-chan struct{}
	// until, if set, is told when the current healthy streak completes, for the countdown.
	until func(time.Time)
}

func confirmWindow(ctx context.Context, w confirmation) error {
	c, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	var mayHeartbeat atomic.Bool
	mayHeartbeat.Store(true)
	var beatFailure atomic.Pointer[error]
	go func() {
		defer close(done)
		t := time.NewTicker(w.heartbeat)
		defer t.Stop()
		for {
			select {
			case <-c.Done():
				return
			case <-w.exited:
				stop()
				return
			case <-t.C:
				if mayHeartbeat.Load() {
					if err := w.beat(c); err != nil && c.Err() == nil {
						beatFailure.Store(&err)
					}
				}
			}
		}
	}()
	defer func() { stop(); <-done }()
	report := func(t time.Time) {
		if w.until != nil {
			w.until(t)
		}
	}
	report(time.Now().Add(w.window))
	t := time.NewTicker(w.interval)
	defer t.Stop()
	var since time.Time
	var lastFailure error
	failures := 0
	for {
		select {
		case <-w.exited:
			return errCoreExited
		default:
		}
		err := w.check(c)
		beat := beatFailure.Swap(nil)
		if beat != nil {
			lastFailure = *beat
		}
		if ctx.Err() != nil {
			if lastFailure == nil {
				lastFailure = err
			}
			return startupTimeout(ctx, "confirming core and LAN health", lastFailure)
		}
		// A check the exit canceled, or one that failed because the core went away, is not
		// the cause.
		select {
		case <-w.exited:
			return errCoreExited
		default:
		}
		mayHeartbeat.Store(err == nil)
		if err != nil {
			if w.window == 0 {
				return err
			}
			lastFailure = err
		}
		if err != nil || beat != nil {
			since = time.Time{}
			if failures++; failures >= confirmFailures {
				return fmt.Errorf("local health: %w", lastFailure)
			}
		}
		now := time.Now()
		if err == nil {
			if since.IsZero() {
				since = now
				report(since.Add(w.window))
			}
			if now.Sub(since) >= w.window {
				return nil
			}
		} else {
			// The next check, an interval away, can start a new streak at the earliest.
			report(now.Add(w.interval + w.window))
		}
		select {
		case <-ctx.Done():
			if failure := beatFailure.Load(); failure != nil {
				lastFailure = *failure
			}
			return startupTimeout(ctx, "confirming core and LAN health", lastFailure)
		case <-w.exited:
			return errCoreExited
		case <-t.C:
		}
	}
}
func (s *System) Selected(ctx context.Context) (map[string]string, error) {
	c, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	gs, e := s.Client().Groups(c)
	if e != nil {
		return nil, e
	}
	ret := make(map[string]string, len(gs))
	for _, g := range gs {
		ret[g.Tag] = g.Selected
	}
	return ret, nil
}
func (s *System) verifyChoices(ctx context.Context, config []byte) error {
	var cfg struct {
		Groups []struct {
			Name, Type string
			Default    string `yaml:"default-selected"`
			Proxies    []string
		} `yaml:"proxy-groups"`
	}
	if e := yaml.Unmarshal(config, &cfg); e != nil {
		return e
	}
	selections, e := s.Selected(ctx)
	if e != nil {
		return e
	}
	for _, g := range cfg.Groups {
		if g.Type == "select" && g.Default != "" && coreTarget(selections[g.Name]) != g.Default {
			return errors.New("core did not confirm saved group selection")
		}
	}
	return nil
}
func probeDNS(ctx context.Context, port int) error {
	r := &net.Resolver{PreferGo: true, Dial: func(c context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}}
	ans, e := r.LookupNetIP(ctx, "ip4", "probe.hyper.invalid.")
	if e != nil || len(ans) != 1 || ans[0].Unmap() != netip.MustParseAddr("192.0.2.1") {
		return errors.New("local core DNS answer unavailable")
	}
	return nil
}

// Guard has its own procd process. The router boot clock may step at any time,
// so missed-heartbeat detection compares file identity across monotonic ticks.
func Guard(ctx context.Context) error {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var previous os.FileInfo
	miss := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if _, e := os.Stat(RunDir + "/network/owned"); os.IsNotExist(e) {
				miss = 0
				previous = nil
				continue
			}
			current, e := os.Stat(RunDir + "/pulse")
			if e == nil {
				token, keyErr := os.ReadFile(Root + "/private/control.token")
				if keyErr != nil || len(token) != 64 {
					e = errors.New("core credential unavailable")
				} else {
					c, end := context.WithTimeout(ctx, 700*time.Millisecond)
					_, e = (control.Client{Secret: string(token)}).Version(c)
					end()
					if e == nil {
						// A live API can coexist with a blocked DNS listener. Probe the
						// local synthetic answer only after DNS takeover is established.
						if _, statErr := os.Stat(RunDir + "/network/dns-probed"); statErr == nil {
							c, end := context.WithTimeout(ctx, 700*time.Millisecond)
							e = probeDNS(c, DNSPort)
							end()
						}
					}
				}
			}
			if e != nil || previous != nil && os.SameFile(previous, current) && previous.ModTime() == current.ModTime() {
				miss++
			} else {
				miss = 0
				previous = current
			}
			if miss >= 3 {
				c, end := context.WithTimeout(ctx, 12*time.Second)
				_, e = command(c, networkHelper, "fault")
				end()
				if e == nil {
					miss = 0
				}
			}
		}
	}
}
