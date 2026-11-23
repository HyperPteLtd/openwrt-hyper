package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

const Root = "/etc/hyper"
const RunDir = "/var/run/hyper"
const networkHelper = "/usr/libexec/hyper/network"
const kernelCLI = "/usr/sbin/hyper-kernel"

var ErrFallbackReady = errors.New("previous confirmed kernel available for recovery")

type System struct {
	confirmUntil  time.Time
	mu            sync.Mutex
	process       *exec.Cmd
	done          chan struct{}
	client        control.Client
	cancelMonitor context.CancelFunc
	audit         *routingAudit
	repairs       repairLimit
	txn           string
	cancelTrial   bool
	dns           bool
	method        string
	family6       bool
	records       *Records
	recovered     bool
	applied       *applied
	network       [2]string // see observe
}

func NewSystem() *System { return &System{records: NewRecords()} }

// confirmPeriod is how long a start must stay healthy before it counts as running.
const confirmPeriod = 3 * time.Second

// The confirmation starts after core listeners and the network takeover are in place. The
// countdown runs to the end of the current healthy streak: a failed check starts it again, at
// most twice (confirmFailures), and the start's end clears it.
func (s *System) ConfirmationSeconds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := time.Until(s.confirmUntil)
	if remaining <= 0 {
		return 0
	}
	return int((remaining + time.Second - 1) / time.Second)
}

func (s *System) Records() *Records      { return s.records }
func (s *System) Client() control.Client { s.mu.Lock(); defer s.mu.Unlock(); return s.client }
func command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, bin, args...)
	c.WaitDelay = time.Second
	var out bytes.Buffer
	c.Stdout = &out
	e := c.Run()
	if e != nil {
		if bin == kernelCLI && (args[0] == "check" || args[0] == "begin") {
			var issue Issue
			if json.Unmarshal(out.Bytes(), &issue) == nil && issue.Reason != "" {
				return nil, issue
			}
		}
		if bin == networkHelper {
			b, _ := os.ReadFile(RunDir + "/network/error")
			stage := strings.TrimSpace(string(b))
			if dnsIssuePattern.MatchString(stage) {
				return nil, errors.New(stage)
			}
			switch stage {
			case "service_stopping", "fault_flow_offload", "fault_protection", "fault_restore", "firewall_inspection", "firewall_release", "dns_restore", "ipv4_restore", "ipv6_restore", "fw4_restore", "fw3_restore", "firewall_backend", "legacy_capabilities", "direct_lan_probe", "firewall_conflict", "lan_interfaces", "management_probe", "firewall_prepare", "ipv4_route", "ipv6_route", "dns_reload", "firewall_apply", "managed_lan_probe":
				return nil, fmt.Errorf("network %s failed at %s", args[0], stage)
			}
		}
		return nil, fmt.Errorf("%s %s failed", filepath.Base(bin), args[0])
	}
	return out.Bytes(), nil
}
func jsonCmd(ctx context.Context, v any, bin string, args ...string) error {
	b, e := command(ctx, bin, args...)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func secret() (string, error) {
	path := Root + "/private/control.token"
	b, e := os.ReadFile(path)
	if e == nil {
		if len(b) != 64 {
			return "", errors.New("invalid control credential")
		}
		return string(b), nil
	}
	if !os.IsNotExist(e) {
		return "", e
	}
	b = make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	k := hex.EncodeToString(b)
	return k, Atomic(path, []byte(k), 0600)
}
func (s *System) Direct(ctx context.Context) error {
	s.endMonitor()
	_, e := command(ctx, networkHelper, "release")
	if e != nil {
		return e
	}
	return s.stopCore(ctx)
}

func (s *System) Fault(ctx context.Context) (bool, error) {
	s.endMonitor()
	result, err := command(ctx, networkHelper, "fault")
	if err != nil {
		return false, err
	}
	disposition := strings.TrimSpace(string(result))
	if disposition != "direct" && disposition != "protected" {
		return false, errors.New("unverified network fault disposition")
	}
	return disposition == "protected", s.stopCore(ctx)
}

// endMonitor stops the routing audit before a release, even one that fails and is retried,
// so it never puts back rules the release is taking away.
func (s *System) endMonitor() {
	s.mu.Lock()
	stop := s.cancelMonitor
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *System) stopCore(ctx context.Context) error {
	s.mu.Lock()
	p, done, txn, stop := s.process, s.done, s.txn, s.cancelMonitor
	keep := s.cancelTrial
	s.client = control.Client{}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if p != nil {
		select {
		case <-done:
		default:
			_ = signalCore(p, syscall.SIGTERM)
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-done:
			case <-timer.C:
				_ = signalCore(p, syscall.SIGKILL)
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
			timer.Stop()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if txn != "" {
		verb := "abort"
		if keep {
			verb = "cancel"
		}
		if _, e := command(ctx, kernelCLI, verb, "--token", txn); e != nil {
			// confirm can commit durably just before its caller times out.
			var status kernel.Status
			if statusErr := jsonCmd(ctx, &status, kernelCLI, "status"); statusErr != nil || status.Pending {
				return e
			}
		}
	}
	if p != nil {
		if _, err := command(ctx, kernelCLI, "closed"); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.process = nil
	s.txn = ""
	s.cancelMonitor = nil
	s.applied = nil
	s.audit = nil
	s.mu.Unlock()
	return nil
}
func ctxWait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func checkStartupSources(i Intent, sources *subscription.Store, original *subscription.SnapshotReader, config []byte, compile func(subscription.Profile) ([]byte, error)) error {
	if original.Unchanged() {
		return nil
	}
	current, err := LoadProfile(i, sources)
	if err != nil {
		return errors.New("subscription authorization changed")
	}
	fresh, err := compile(current)
	if err != nil || !bytes.Equal(fresh, config) {
		return errors.New("subscription changed during startup")
	}
	return nil
}

func (s *System) Start(ctx context.Context, i Intent) (err error) {
	if !s.recovered {
		var st kernel.Status
		if e := jsonCmd(ctx, &st, kernelCLI, "status"); e != nil {
			return e
		}
		if st.Pending || st.Active != "" {
			if _, e := command(ctx, kernelCLI, "recover"); e != nil && st.Active != "" {
				return e
			}
		}
		if st.Active != "" {
			if _, e := command(ctx, kernelCLI, "closed"); e != nil {
				return e
			}
		}
		s.recovered = true
	}
	var st kernel.Status
	var caps kernel.Capabilities
	if e := jsonCmd(ctx, &st, kernelCLI, "status"); e != nil {
		return e
	}
	if st.Repair {
		if _, e := command(ctx, kernelCLI, "recover"); e != nil {
			return e
		}
	}
	// Stopping other proxy plugins takes their DNS files away before the prerequisites read them.
	// The core's group is resolved first: an unusable one fails the start with them still running.
	gid, e := s.prepareTakeover(ctx)
	if e != nil {
		return e
	}
	// Missing firmware capabilities are independent of the selected core.
	if err := checkNetworkRequirements(ctx); err != nil {
		return err
	}
	// Candidate preflight can fail before begin creates a trial. Roll it back
	// by identity so a concurrent user selection always wins.
	if st.Active != "" && st.Selected != st.Active {
		defer func() {
			if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
				c, end := context.WithTimeout(context.Background(), 3*time.Second)
				defer end()
				s.mu.Lock()
				trial := s.txn != ""
				s.mu.Unlock()
				_, rejected := command(c, kernelCLI, "reject", "--tag", st.Selected)
				if rejected == nil || trial {
					err = errors.Join(err, ErrFallbackReady)
				}
			}
		}()
	}
	n, e := networkOptions(ctx)
	s.observe(ctx, n, e)
	if e != nil {
		return e
	}
	key, e := secret()
	if e != nil {
		return e
	}
	if e = jsonCmd(ctx, &caps, kernelCLI, "capabilities"); e != nil {
		return e
	}
	cap, ok := caps.Targets[st.Target]
	if !ok || caps.Tag != st.Selected || caps.Repository != kernel.Repository {
		return errors.New("capability information does not match selected release")
	}
	sources := subscription.OpenStore(Root + "/sources")
	startupSources := sources.SnapshotReader()
	profile, e := LoadProfile(i, startupSources)
	if e != nil {
		return errors.New("active subscription unavailable")
	}
	for _, dir := range []string{coreHome, RunDir + "/core"} {
		if e = reclaimDir(dir); e != nil {
			return errors.New("cannot reclaim the core's runtime directory")
		}
	}
	if e = prepareMihomo(profile); e != nil {
		return errors.New("cannot prepare authorized rule sets")
	}
	if _, e = syncDevices(ctx, policyMACs(i), n); e != nil {
		return e
	}
	compile := func(p subscription.Profile) ([]byte, error) { return Compile(i, p, cap, n, key) }
	config, e := compile(profile)
	if e != nil {
		return e
	}
	if e = prepareGeo(ctx, config, profile); e != nil {
		return e
	}
	file := RunDir + "/candidate.yaml"
	if e = Atomic(file, config, 0600); e != nil {
		return e
	}
	defer os.Remove(file)
	var launch kernel.Launch
	args := []string{"begin", "--tag", caps.Tag, "--config", file, "--source", i.Source}
	auth := map[string]bool{i.Source: true}
	for _, ref := range i.Policy.refs() {
		if !auth[ref.Source] {
			auth[ref.Source] = true
			args = append(args, "--source", ref.Source)
		}
	}
	if e = jsonCmd(ctx, &launch, kernelCLI, args...); e != nil {
		return e
	}
	s.mu.Lock()
	s.txn = launch.Token
	s.cancelTrial = false
	s.mu.Unlock()
	defer func() {
		if err != nil && errors.Is(ctx.Err(), context.Canceled) {
			s.mu.Lock()
			s.cancelTrial = true
			s.mu.Unlock()
		}
	}()
	if launch.Tag != caps.Tag || !filepath.IsAbs(launch.Config) || !filepath.IsAbs(launch.Path) {
		return errors.New("invalid launch ticket")
	}
	work := RunDir + "/core"
	if e = os.Mkdir(work, 0700); e != nil && !os.IsExist(e) {
		return e
	}
	info, e := os.Lstat(work)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return errors.New("invalid private core work directory")
	}
	cmd := coreCommand(launch, work, gid)
	// No raw node config or free-form core error can enter diagnostics.
	coreLog := &logSink{r: s.records}
	s.records.CoreStarted(i.Source)
	cmd.Stderr = coreLog
	cmd.Stdout = coreLog
	if e = ctx.Err(); e != nil {
		return e
	}
	// The core inherits the manager's open-file limit. procd already applies it to the
	// service; setting it here also stops Go from handing the core its original soft limit.
	if setFileLimit(coreFiles) != nil {
		s.records.Log("core", "warn", "core_file_limit_low")
	}
	// PR_SET_PDEATHSIG follows the creating Linux thread, so keep that
	// thread alive until the supervised core exits.
	done := make(chan struct{})
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		e := cmd.Start()
		started <- e
		if e == nil {
			_ = cmd.Wait()
			coreLog.flush()
		}
	}()
	if <-started != nil {
		return errors.New("cannot launch core")
	}
	s.mu.Lock()
	s.process = cmd
	s.done = done
	s.client = control.Client{Secret: key}
	s.dns = n.DNS
	s.method = n.Method
	s.family6 = n.Family6
	s.mu.Unlock()
	// A core that exits before the start counts as running fails it with the reason its last
	// lines gave, if any.
	exitReason := func(e error) error {
		select {
		case <-done:
			if ctx.Err() == nil && coreLog.terminal != "" {
				return errors.New(coreLog.terminal)
			}
		default:
		}
		return e
	}
	if e = waitCoreReady(ctx, done, 350*time.Millisecond, s.ready); e != nil {
		return exitReason(e)
	}
	validateAuthorization := func() error {
		return checkStartupSources(i, sources, startupSources, config, compile)
	}
	if e = validateAuthorization(); e != nil {
		return e
	}
	if e = pulse(); e != nil {
		return e
	}
	rule, xtRule, e := telegramAllowance(i.Policy.Telegram, n)
	if e != nil {
		return e
	}
	handoff := RunDir + "/telegram-input.rule"
	if e = Atomic(handoff, []byte(rule), 0600); e != nil {
		return e
	}
	defer os.Remove(handoff)
	xtHandoff := RunDir + "/telegram-input.xt"
	if e = Atomic(xtHandoff, []byte(xtRule), 0600); e != nil {
		return e
	}
	defer os.Remove(xtHandoff)
	policy, e := deviceGuardPolicy(i)
	if e != nil {
		return e
	}
	if e = Atomic(RunDir+"/device-guard.policy", []byte(policy), 0600); e != nil {
		return e
	}
	direct, e := directDevices(i)
	if e != nil {
		return e
	}
	directHandoff := RunDir + "/direct-devices"
	if e = Atomic(directHandoff, []byte(direct), 0600); e != nil {
		return e
	}
	defer os.Remove(directHandoff)
	// DNS reloads on a busy router can overlap multiple guard intervals.
	// Keep proving core liveness while the serialized network helper applies.
	applyCtx, endApply := context.WithCancel(ctx)
	applied := make(chan error, 1)
	apply := applyArgs(i, n, gid)
	go func() { _, ae := command(applyCtx, networkHelper, apply...); applied <- ae }()
	tick := time.NewTicker(2 * time.Second)
applyLoop:
	for {
		select {
		case e = <-applied:
			break applyLoop
		case <-tick.C:
			_ = s.beat(applyCtx)
		}
	}
	tick.Stop()
	endApply()
	if e != nil {
		return e
	}
	// Every start, not only a new core's trial, holds four healthy checks in a row (checked
	// every second) before it counts as running: a core or takeover that comes up and falls
	// over at once fails the start here instead of after the page already shows it connected.
	// The core exiting fails it at once, and so does a third failed check (confirmFailures),
	// so a broken start is retried in seconds rather than after the start deadline.
	defer func() { s.mu.Lock(); s.confirmUntil = time.Time{}; s.mu.Unlock() }()
	if e = confirmWindow(ctx, confirmation{
		window: confirmPeriod, interval: time.Second, heartbeat: time.Second,
		check: s.verify, beat: s.beat, exited: done,
		until: func(t time.Time) { s.mu.Lock(); s.confirmUntil = t; s.mu.Unlock() },
	}); e != nil {
		return exitReason(e)
	}
	if e = validateAuthorization(); e != nil {
		return e
	}
	if e = s.verifyChoices(ctx, config); e != nil {
		return e
	}
	if launch.Token != "" {
		if _, e = command(ctx, kernelCLI, "confirm", "--token", launch.Token); e != nil {
			return e
		}
		s.mu.Lock()
		s.txn = ""
		s.mu.Unlock()
	}
	s.remember(i, n, profile, caps.Tag, config)
	s.startMonitor(i, n, key)
	return nil
}

// coreCommand runs the core as OpenClash runs its own: as root with every capability, and
// without a sandbox, a capability bound or no_new_privs, so TUN mode opens the router's
// /dev/net/tun itself. coreProcess sets the group gid (coregroup: nogroup, whose sockets the
// firewall lets out uncaptured) and drops supplementary groups.
func coreCommand(launch kernel.Launch, work string, gid int) *exec.Cmd {
	cmd := exec.Command(launch.Path, "-d", coreHome, "-f", launch.Config)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=" + work, "HOME=" + work}
	cmd.Dir = work
	coreProcess(cmd, gid)
	return cmd
}

func (s *System) startMonitor(i Intent, n Network, key string) {
	stream, end := context.WithCancel(context.Background())
	audit := s.newAudit()
	s.mu.Lock()
	s.cancelMonitor = end
	s.audit = audit
	s.mu.Unlock()
	go audit.run(stream)
	if macs := policyMACs(i); len(macs) > 0 {
		go s.watchDevices(stream, macs, n)
	}
	recording := i.Policy.RecordConnections == nil || *i.Policy.RecordConnections
	s.records.Record(recording)
	if !recording {
		return
	}
	go func() {
		client := control.Client{Secret: key}
		for stream.Err() == nil {
			_ = client.Connections(stream, s.records.pollWait, s.records.Observe)
			_ = ctxWait(stream, time.Second)
		}
	}()
}
