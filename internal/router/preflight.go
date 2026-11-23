package router

import (
	"context"
	"errors"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"os"
	"time"
)

type overlaySource struct {
	*subscription.Store
	candidate *subscription.Profile
}

func (s overlaySource) Load(id string) (subscription.Profile, error) {
	if s.candidate != nil && s.candidate.Source == id {
		return *s.candidate, nil
	}
	return s.Store.Load(id)
}
func (s *System) Preflight(ctx context.Context, i Intent, candidate *subscription.Profile, version string) error {
	n, e := networkOptions(ctx)
	if e != nil {
		return e
	}
	p, e := LoadProfile(i, overlaySource{subscription.OpenStore(Root + "/sources"), candidate})
	if e != nil {
		return errors.New("candidate has unavailable source references")
	}
	var st kernel.Status
	if e = jsonCmd(ctx, &st, kernelCLI, "status"); e != nil {
		return e
	}
	if version == "" {
		version = st.Selected
	}
	var caps kernel.Capabilities
	if e = jsonCmd(ctx, &caps, kernelCLI, "capabilities", "--tag", version); e != nil {
		return e
	}
	cap, ok := caps.Targets[st.Target]
	if !ok || caps.Tag != version || caps.Repository != kernel.Repository {
		return errors.New("capability revision mismatch")
	}
	if i.Mode == "direct" {
		i.Mode = "smart"
	}
	return checkProfile(ctx, i, p, cap, n, version)
}

func checkProfile(ctx context.Context, i Intent, p subscription.Profile, cap kernel.TargetCapabilities, n Network, version string) error {
	{
		if e := prepareMihomo(p); e != nil {
			return e
		}
		if e := ensureDeviceFiles(coreHome+"/devices", policyMACs(i)); e != nil {
			return e
		}
	}
	key, e := secret()
	if e != nil {
		return e
	}
	data, e := Compile(i, p, cap, n, key)
	if e != nil {
		return e
	}
	if e = prepareGeo(ctx, data, p); e != nil {
		return e
	}
	file, e := os.CreateTemp(RunDir, "preflight-*.yaml")
	if e != nil {
		return e
	}
	defer os.Remove(file.Name())
	_, e = file.Write(data)
	closeErr := file.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	_, e = command(ctx, kernelCLI, "check", "--tag", version, "--config", file.Name())
	return e
}
func (m *Manager) ReviewToken(source string) (string, Intent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.frozen {
		return "", Intent{}, false, ErrNotReady
	}
	i := m.state.Intent.clone()
	needed := i.Enabled && i.Mode != "direct" && i.ReferencesSource(source)
	return m.reviewToken(), i, needed, nil
}
func (m *Manager) reviewToken() string {
	return fmt.Sprintf("%d.%d.%d.%d", m.instance, m.generation, m.reviewEpoch, m.state.Intent.Revision)
}
func newInstance() int64 { return time.Now().UnixNano() }

// CommitReviewed serializes the short publication step with stop, mode and
// selection writes. Callers perform downloads and preflight before this point.
func (m *Manager) CommitReviewed(token string, commit func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.frozen || token != m.reviewToken() {
		return errors.New("router intent changed during validation")
	}
	return commit()
}

// SelectReviewed publishes the checked candidate and schedules any restart
// under the same intent lock. A concurrent stop cannot be lost in between.
func (m *Manager) SelectReviewed(token string, restart bool, selectKernel func() error) error {
	return m.CommitReviewed(token, func() error {
		if e := selectKernel(); e != nil {
			return e
		}
		if restart {
			m.refreshLocked()
		}
		return nil
	})
}
