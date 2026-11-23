package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

// bundledTag is the version of the core the package carries, "" without one.
func bundledTag(ctx context.Context) string {
	st, err := kernelStatus(ctx)
	if err != nil {
		return ""
	}
	return st.Bundled
}

func kernelStatus(ctx context.Context) (kernel.Status, error) {
	var st kernel.Status
	data, e := child(ctx, "hyper-kernel", []string{"status"}, nil)
	if e == nil {
		e = json.Unmarshal(data, &st)
	}
	return st, e
}

// Downloading is separate from selecting. A slow install that loses its
// revision leaves a checked, staged asset available for a later explicit choice.
func (s *app) kernelJob(r request) (any, error) {
	if r.Action != "install-json" && r.Action != "select" && r.Action != "rollback" {
		return nil, errors.New("unknown kernel operation")
	}
	initial, _, _, e := s.m.ReviewToken("")
	if e != nil {
		return nil, e
	}
	var args map[string]json.RawMessage
	if len(r.Args) != 0 && json.Unmarshal(r.Args, &args) != nil {
		return nil, errors.New("invalid kernel request")
	}
	var tag string
	if r.Action != "rollback" && (json.Unmarshal(args["tag"], &tag) != nil || !kernel.TagPattern.MatchString(tag)) {
		return nil, errors.New("invalid version")
	}
	if e = checkKernelArgs(r.Action, args); e != nil {
		return nil, e
	}
	return s.runJob("kernel_write", "kernel/"+r.Action, func(ctx context.Context) (json.RawMessage, error) {
		if e := s.yieldFirstKernel(ctx); e != nil {
			return nil, e
		}
		if r.Action == "install-json" && tag == bundledTag(ctx) {
			// The package's own core comes from the package, in the storage asked for.
			mode := "double"
			if raw, ok := args["mode"]; ok && (json.Unmarshal(raw, &mode) != nil || mode != "double" && mode != "single" && mode != "tmp") {
				return nil, errors.New("storage mode must be double, single or tmp")
			}
			if _, e := child(ctx, "hyper-kernel", []string{"bundled", "--mode", mode}, nil); e != nil {
				return nil, e
			}
		} else if r.Action == "install-json" {
			args["stage_only"] = json.RawMessage("true")
			body, e := json.Marshal(args)
			if e != nil {
				return nil, e
			}
			if _, e = child(ctx, "hyper-kernel", []string{"install-json"}, body); e != nil {
				return nil, e
			}
		}
		st, e := kernelStatus(ctx)
		if e != nil {
			return nil, e
		}
		if r.Action == "rollback" {
			tag = st.Previous
			if tag == "" {
				return nil, errors.New("no retained version available")
			}
		}
		if e := s.selectKernel(ctx, initial, st, tag); e != nil {
			return nil, e
		}
		out, _ := json.Marshal(map[string]string{"selected": tag})
		return out, nil
	})
}

// selectKernel makes tag the selected core once the configuration checks against it, and
// waits for the running core to be it when the router routes.
func (s *app) selectKernel(ctx context.Context, initial string, st kernel.Status, tag string) error {
	token, intent, _, e := s.m.ReviewToken("")
	if e != nil {
		return e
	}
	if token != initial {
		return errors.New("router intent changed; version was not selected")
	}
	if intent.Source != "" {
		if e := s.sys.Preflight(ctx, intent, nil, tag); e != nil {
			return e
		}
	} else if intent.Enabled && intent.Mode != "direct" {
		return errors.New("missing primary source for candidate validation")
	}
	phase, _, _ := s.m.RuntimeState()
	reload := intent.Enabled && intent.Mode != "direct" && (st.Selected != tag || st.Active != tag || phase != "running")
	e = s.m.SelectReviewed(initial, reload, func() error {
		cli := []string{"select", "--tag", tag, "--expected", strconv.FormatUint(st.Revision, 10)}
		_, e := child(ctx, "hyper-kernel", cli, nil)
		return e
	})
	if e != nil {
		return e
	}
	if reload {
		return waitForKernel(ctx, tag, st.Active, s.m.RuntimeState, kernelStatus)
	}
	return nil
}

// useBundledKernel installs the core this package carries and selects it when the router
// runs another (first boot, or an upgrade that brought a newer core). Without a bundled core,
// or with it selected already, nothing changes.
func (s *app) useBundledKernel() {
	_, _ = s.runJob("kernel_write", "kernel/bundled", func(ctx context.Context) (json.RawMessage, error) {
		if e := s.yieldFirstKernel(ctx); e != nil {
			return nil, e
		}
		out, e := child(ctx, "hyper-kernel", []string{"bundled"}, nil)
		if e != nil {
			return nil, e
		}
		var bundled struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(out, &bundled) != nil || bundled.Tag == "" {
			return json.RawMessage(`{}`), nil
		}
		st, e := kernelStatus(ctx)
		if e != nil {
			return nil, e
		}
		if st.Selected != bundled.Tag {
			initial, _, _, e := s.m.ReviewToken("")
			if e != nil {
				return nil, e
			}
			if e := s.selectKernel(ctx, initial, st, bundled.Tag); e != nil {
				return nil, e
			}
		}
		return json.Marshal(map[string]string{"selected": bundled.Tag})
	})
}

// LuCI may choose a transport, but only the installer may choose trusted metadata.
func checkKernelArgs(action string, args map[string]json.RawMessage) error {
	for key := range args {
		switch {
		case (action == "select" || action == "install-json") && key == "tag":
		case action == "install-json" && (key == "mode" || key == "url" || key == "mirror"):
		default:
			return errors.New("unsupported kernel request field")
		}
	}
	return nil
}

// Never forward raw helper stderr: file paths and mirror URLs can contain secrets.
func publicKernelError(stderr string) error {
	message := strings.TrimSpace(stderr)
	if failure := tlsdiag.Parse(message); failure != nil {
		return errors.New(failure.Wire())
	}
	switch message {
	case errKernelRevision.Error():
		return errKernelRevision
	case kernel.ErrReleaseRateLimit.Error():
		return kernel.ErrReleaseRateLimit
	case kernel.ErrTemporaryDownload.Error():
		return kernel.ErrTemporaryDownload
	case "publisher has no stable release":
		return errors.New("publisher has no stable release")
	case "latest stable release unreachable", "invalid latest stable release identity", "latest stable release lacks a verified archive for this router":
		return errors.New(message)
	case "release file size or SHA-256 mismatch", "archive was modified during extraction":
		return errors.New("release checksum mismatch")
	case "invalid or ambiguous upstream checksum", "upstream checksum is missing for this architecture",
		"invalid original release asset digest", "original release supplies no SHA-256 for this archive":
		return errors.New("upstream release checksum unavailable")
	case "original release lacks a matching Linux CLI archive", "invalid Linux ELF",
		"wrong ELF machine, class or endian", "core OS/architecture mismatch",
		"core instruction set exceeds the declared target", "wrong ARM instruction or floating-point ABI",
		"wrong software floating-point ABI":
		return errors.New("release archive incompatible with this router")
	case "release source unreachable":
		return errors.New("release source unreachable")
	case "core failed restricted version validation", "Mihomo executable reported a different version":
		return errors.New("release executable failed version validation")
	case "insufficient free storage; active core retained", "insufficient shared storage; active core retained",
		"insufficient temporary space for release archive", "insufficient space on cache volume",
		"insufficient trial RAM and 48 MiB system reserve", "persistent mode requires 128 MiB storage and 256 MiB RAM",
		"temporary mode requires 64 MiB storage and 512 MiB RAM":
		return errors.New("insufficient resources for this storage mode")
	}
	if strings.HasPrefix(message, "cannot verify original release via GitHub: ") || strings.HasPrefix(message, "original release checksum unavailable: ") {
		return errors.New("upstream release checksum unavailable")
	}
	return errors.New("kernel operation failed")
}

// A selected tag is only a saved preference. A running change finishes when
// the supervisor confirms the healthy core, or restores the confirmed release.
func waitForKernel(ctx context.Context, tag, previous string, service func() (string, string, bool), status func(context.Context) (kernel.Status, error)) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		phase, problem, wantsProxy := service()
		if !wantsProxy {
			return errors.New("service stopped before the kernel change was confirmed")
		}
		switch phase {
		case "running", "fail_open", "protected", "release_failed":
			st, e := status(ctx)
			if e != nil {
				return e
			}
			if st.Pending {
				continue
			}
			if phase == "running" && st.Selected == tag && st.Active == tag {
				return nil
			}
			if phase == "running" && previous != "" && st.Selected == previous && st.Active == previous {
				return fmt.Errorf("candidate failed startup or health checks; previous kernel %s is running", previous)
			}
			if phase == "release_failed" || phase != "running" && (previous == "" || st.Selected == tag) {
				return fmt.Errorf("kernel change unconfirmed: %s", problem)
			}
		}
	}
}
