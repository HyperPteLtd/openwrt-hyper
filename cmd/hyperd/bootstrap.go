package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

const bootstrapAction = "kernel/bootstrap"

// bootstrapRetryAfter: how long a router without a core waits between downloads (coreMissingDue).
const bootstrapRetryAfter = 10 * time.Minute

func (s *app) queueFirstKernel() {
	dir := filepath.Join(router.Root, "mihomo-kernel")
	ready, err := firstInstallPending(dir)
	if err != nil {
		s.sys.Records().Log("kernel", "warn", "kernel_bootstrap_marker_invalid")
	}
	if !ready {
		return
	}
	s.queueBootstrap(false)
}

// Called with mutation held, so a manual version change accepted before this
// check supersedes automatic recovery, including across a manager restart.
func (s *app) retryBootstrapClock(now time.Time) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	if s.bootstrapClockDue(now) || s.coreMissingDue(now) {
		s.queueBootstrap(true)
	}
}

// coreMissingDue: a router without any core, which may have started before its WAN or been
// installed offline, tries to download one every bootstrapRetryAfter while no kernel task
// runs. A core installed meanwhile, or any kernel state, ends it (Bootstrap skips then too).
func (s *app) coreMissingDue(now time.Time) bool {
	s.mu.Lock()
	due := !now.Before(s.bootstrapNext)
	for i := len(s.order) - 1; due && i >= 0; i-- {
		if j := s.jobs[s.order[i]]; strings.HasPrefix(j.Action, "kernel/") {
			due = j.Stage != "running"
			break
		}
	}
	if due {
		s.bootstrapNext = now.Add(bootstrapRetryAfter)
	}
	s.mu.Unlock()
	if !due {
		return false
	}
	st, err := kernelStatus(s.ctx)
	return err == nil && st.Selected == "" && st.Staged == "" && st.Active == "" && !st.Pending && !st.Repair
}

func (s *app) bootstrapClockDue(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		j := s.jobs[s.order[i]]
		if strings.HasPrefix(j.Action, "kernel/") {
			return j.Action == bootstrapAction && j.Stage == "failed" && tlsdiag.Parse(j.Error).Recheck(now)
		}
	}
	return false
}

func (s *app) queueBootstrap(retry bool) {
	dir := filepath.Join(router.Root, "mihomo-kernel")
	manualCtx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.bootstrapCancel, s.bootstrapDone = cancel, done
	s.mu.Unlock()
	_, err := s.runJob("kernel_bootstrap", bootstrapAction, func(ctx context.Context) (json.RawMessage, error) {
		defer close(done)
		defer cancel()
		ctx, end := context.WithCancel(ctx)
		defer end()
		unhook := context.AfterFunc(manualCtx, end)
		defer unhook()
		if !retry {
			claimed, err := claimFirstInstall(dir)
			if err != nil {
				return nil, err
			}
			if !claimed {
				return json.RawMessage(`{"skipped":"existing_intent"}`), nil
			}
		}
		// A router that is on (0.x's, upgraded with its proxy running) needs its core all the more.
		var stage struct {
			Staged string `json:"staged"`
		}
		out, err := retryFirstKernel(ctx, []time.Duration{5 * time.Second, 15 * time.Second}, func(ctx context.Context) (json.RawMessage, error) {
			// A retry clears what an attempt it follows left behind, as a restart mid-download does.
			args := []string{"bootstrap"}
			if retry {
				args = append(args, "--again")
			}
			return child(ctx, "hyper-kernel", args, nil)
		})
		if manualCtx.Err() != nil && s.ctx.Err() == nil {
			return json.RawMessage(`{"skipped":"manual_operation"}`), nil
		}
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(out, &stage); err != nil {
			return nil, err
		}
		if stage.Staged == "" {
			return out, nil
		}
		return s.selectFirstKernel(ctx, stage.Staged)
	})
	if err != nil {
		cancel()
		close(done)
		s.sys.Records().Log("kernel", "warn", "kernel_bootstrap_queue_failed")
	}
}

// Manual requests take precedence even during startup WAN backoff. The helper
// process has exited and released its cross-process payload lock before done.
func (s *app) yieldFirstKernel(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.bootstrapCancel, s.bootstrapDone
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func retryFirstKernel(ctx context.Context, delays []time.Duration, attempt func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := attempt(ctx)
		if !errors.Is(err, kernel.ErrTemporaryDownload) || i >= len(delays) {
			return out, err
		}
		timer := time.NewTimer(delays[i])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *app) selectFirstKernel(ctx context.Context, tag string) (json.RawMessage, error) {
	st, err := kernelStatus(ctx)
	if err != nil {
		return nil, err
	}
	if st.Staged != tag || st.Selected != "" || st.Active != "" || st.Pending {
		return json.RawMessage(`{"skipped":"existing_kernel"}`), nil
	}
	// A router that is on takes its first core as an administrator's choice would: checked
	// against its source and started with it, or, before its account's first sync brings the
	// source, selected for that sync to start.
	if token, intent, _, err := s.m.ReviewToken(""); err == nil && intent.Enabled {
		if intent.Source != "" {
			err = s.selectKernel(ctx, token, st, tag)
		} else {
			err = s.m.SelectReviewed(token, false, func() error {
				_, err := child(ctx, "hyper-kernel", []string{"select", "--tag", tag, "--expected", strconv.FormatUint(st.Revision, 10)}, nil)
				return err
			})
		}
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"selected": tag})
	}
	return selectCheckedKernel(ctx, s.m, tag, func(intent router.Intent) error {
		return s.sys.Preflight(ctx, intent, nil, tag)
	}, func() error {
		_, err := child(ctx, "hyper-kernel", []string{"select", "--tag", tag, "--expected", strconv.FormatUint(st.Revision, 10)}, nil)
		return err
	})
}

func selectCheckedKernel(ctx context.Context, manager *router.Manager, tag string, preflight func(router.Intent) error, selectVersion func() error) (json.RawMessage, error) {
	result := map[string]string{"staged": tag}
	token, intent, _, err := manager.ReviewToken("")
	if err != nil || intent.Enabled || ctx.Err() != nil {
		result["reason"] = "intent_changed"
		return json.Marshal(result)
	}
	if intent.Source != "" {
		if err = preflight(intent); err != nil {
			result["reason"] = "preflight_failed"
			return json.Marshal(result)
		}
	}
	var writeErr error
	err = manager.CommitReviewed(token, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		writeErr = selectVersion()
		return writeErr
	})
	if err != nil {
		if writeErr != nil && !errors.Is(writeErr, errKernelRevision) {
			out, _ := json.Marshal(result)
			return out, writeErr
		}
		result["reason"] = "selection_changed"
		return json.Marshal(result)
	}
	return json.Marshal(map[string]string{"selected": tag})
}

var errKernelRevision = errors.New("kernel selection revision changed")

// The bootstrap worker emits only these public fields. Drop any unexpected
// values before storing its result in the persistent task journal.
func publicBootstrapResult(raw json.RawMessage) json.RawMessage {
	var result map[string]string
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	safe := map[string]string{}
	for key, value := range result {
		switch key {
		case "selected", "staged":
			if len(value) <= 100 && kernel.TagPattern.MatchString(value) {
				safe[key] = value
			}
		case "skipped":
			switch value {
			case "existing_kernel", "existing_intent", "manual_operation", "installation_in_progress":
				safe[key] = value
			}
		case "reason":
			switch value {
			case "intent_changed", "preflight_failed", "selection_changed":
				safe[key] = value
			}
		}
	}
	if len(safe) == 0 {
		return nil
	}
	out, _ := json.Marshal(safe)
	return out
}

// Claim before any network access. Offline routers and interrupted installs
// keep a durable one-attempt boundary; the normal installer remains available.
func firstInstallPending(dir string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(dir, ".first-install-attempted")); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	info, err := os.Lstat(filepath.Join(dir, ".first-install-pending"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return false, errors.New("invalid initial kernel marker")
	}
	return true, nil
}

func claimFirstInstall(dir string) (bool, error) {
	ready, err := firstInstallPending(dir)
	if !ready || err != nil {
		return false, err
	}
	pending := filepath.Join(dir, ".first-install-pending")
	attempted := filepath.Join(dir, ".first-install-attempted")
	if err = os.Rename(pending, attempted); err != nil {
		return false, err
	}
	return true, atomicfile.SyncDir(dir)
}

type kernelView struct {
	kernel.Status
	Bootstrap *job `json:"bootstrap,omitempty"`
}

func (s *app) bootstrapJob() *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		j := s.jobs[s.order[i]]
		if j.Action == bootstrapAction {
			return &j
		}
	}
	return nil
}
