package kernelcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

func Main() {
	if err := run(os.Args[1:]); err != nil {
		if len(os.Args) > 1 && (os.Args[1] == "begin" || os.Args[1] == "check") {
			_ = json.NewEncoder(os.Stdout).Encode(launchIssue(err))
		}
		if failure := tlsdiag.From(err); failure != nil {
			fmt.Fprintln(os.Stderr, failure.Wire())
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected kernel management command")
	}
	if args[0] == "exec-restricted" {
		return execRestricted(args[1:])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	tag := f.String("tag", "", "")
	mode := f.String("mode", "double", "")
	mirror := f.String("mirror", "", "")
	address := f.String("url", "", "")
	bundle := f.String("bundle", "", "")
	cache := f.String("cache", "", "")
	token := f.String("token", "", "")
	config := f.String("config", "", "")
	stageOnly := f.Bool("stage-only", false, "")
	var expected []uint64
	f.Func("expected", "", func(s string) error {
		if len(expected) > 0 {
			return errors.New("duplicate expected revision")
		}
		v, e := strconv.ParseUint(s, 10, 64)
		if e == nil {
			expected = append(expected, v)
		}
		return e
	})
	var authorities []string
	f.Func("source", "", func(v string) error { authorities = append(authorities, v); return nil })
	if e := f.Parse(args[1:]); e != nil || f.NArg() != 0 {
		return errors.New("invalid kernel command options")
	}
	arch, e := readArch()
	if e != nil {
		return e
	}
	target, e := kernel.DetectTarget(arch)
	if e != nil {
		return e
	}
	if args[0] == "versions" {
		items, err := kernel.Versions(ctx, target)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(items)
	}
	m, e := kernel.New(kernel.Options{Root: "/etc/hyper/mihomo-kernel", Temp: "/tmp/hyper/mihomo-kernel", Target: target, Board: arch, Key: kernel.PublicKey, Inspect: inspect})
	if e != nil {
		return e
	}
	var result any = struct{}{}
	switch args[0] {
	case "bootstrap":
		if len(args) != 1 {
			return errors.New("bootstrap takes no options")
		}
		result, e = m.Bootstrap(ctx)
	case "status":
		result, e = m.Status()
	case "bundled":
		// The core the package carries, staged unless installed already; the daemon selects it.
		// --mode moves it to that storage; without, it goes where it fits.
		chosen := ""
		f.Visit(func(fl *flag.Flag) {
			if fl.Name == "mode" {
				chosen = *mode
			}
		})
		var tag string
		tag, e = m.InstallBundled(ctx, kernel.BundleDir, chosen)
		result = map[string]string{"tag": tag}
	case "capabilities":
		result, e = m.Capabilities(*tag)
	case "target-capabilities":
		var caps kernel.Capabilities
		caps, e = m.Capabilities(*tag)
		if e == nil {
			result = caps.Targets[target]
		}
	case "active":
		result, e = m.Active()
	case "recover":
		result, e = m.Recover(ctx)
	case "install-json":
		if len(args) != 1 {
			return errors.New("install-json takes its options on stdin")
		}
		var req struct {
			Tag       string `json:"tag"`
			Mode      string `json:"mode"`
			URL       string `json:"url"`
			Mirror    string `json:"mirror"`
			Bundle    string `json:"bundle"`
			Cache     string `json:"cache"`
			StageOnly bool   `json:"stage_only"`
		}
		raw, readErr := io.ReadAll(io.LimitReader(os.Stdin, 16385))
		if readErr != nil || len(raw) > 16384 {
			return errors.New("invalid installation request")
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF {
			return errors.New("invalid installation request")
		}
		if req.Mode == "" {
			req.Mode = "double"
		}
		e = m.Install(ctx, req.Tag, req.Mode, kernel.Source{Mirror: req.Mirror, URL: req.URL, Bundle: req.Bundle, Cache: req.Cache}, req.StageOnly)
	case "install":
		e = m.Install(ctx, *tag, *mode, kernel.Source{Mirror: *mirror, URL: *address, Bundle: *bundle, Cache: *cache}, *stageOnly)
	case "select":
		e = m.Select(*tag, false, expected...)
	case "reject":
		e = m.Reject(*tag)
	case "rollback":
		e = m.Select("", true, expected...)
	case "check":
		e = m.Check(ctx, *tag, *config)
	case "begin":
		result, e = m.BeginAuthorized(ctx, *config, *tag, authorities)
	case "confirm":
		result, e = m.Confirm(ctx, *token)
	case "closed":
		e = m.Closed()
	case "invalidate":
		if len(authorities) != 1 {
			return errors.New("one authority required")
		}
		e = m.Invalidate(authorities[0])
	case "cancel":
		e = m.Abort(*token, true)
	case "abort":
		e = m.Abort(*token)
	default:
		return errors.New("unknown kernel management command")
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
func readArch() (string, error) {
	b, e := os.ReadFile("/etc/openwrt_release")
	if e != nil {
		return "", errors.New("OpenWrt CPU type unavailable")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == "DISTRIB_ARCH" {
			return strings.Trim(v, "'\""), nil
		}
	}
	return "", errors.New("missing DISTRIB_ARCH")
}

type capped struct {
	b        strings.Builder
	overflow bool
}

func (c *capped) Write(p []byte) (int, error) {
	remaining := 4096 - c.b.Len()
	if len(p) > remaining {
		c.overflow = true
	}
	c.b.Write(p[:min(len(p), remaining)])
	return len(p), nil
}

const runtimeHome = "/var/run/hyper/mihomo"

func inspect(ctx context.Context, l kernel.Launch, config string) error {
	limit := 15 * time.Second
	if config != "" {
		limit = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	self, e := os.Executable()
	if e != nil {
		return e
	}
	args := []string{"exec-restricted", l.Path, "-v"}
	if config != "" {
		args = []string{"exec-restricted", l.Path, "-t", "-d", runtimeHome, "-f", config}
	}
	var probe *os.File
	if strings.HasPrefix(filepath.Base(config), ".capability-") {
		// First installation precedes creation of the runtime home. Reading the
		// synthetic probe on stdin keeps Mihomo from creating a default file.
		probe, e = os.Open(config)
		if e != nil {
			return e
		}
		defer probe.Close()
		args[len(args)-1] = "-"
	}
	cmd := exec.CommandContext(ctx, self, args...)
	// os.Executable resolves the installed symlink to the shared executable.
	cmd.Args[0] = "hyper-kernel"
	if probe != nil {
		cmd.Stdin = probe
	}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Dir = "/"
	var out capped
	// Mihomo writes diagnostics to stdout and the final result to stderr on some builds.
	cmd.Stdout = &out
	cmd.Stderr = &out
	if e = cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if config != "" {
			if !strings.Contains(out.b.String(), "test failed") {
				return errors.New("core could not complete restricted configuration validation")
			}
			return rejection(config, out.b.String())
		}
		return errors.New("core failed restricted version validation")
	}
	if config == "" {
		if out.overflow {
			return errors.New("core version output exceeded limit")
		}
		for _, line := range strings.Split(out.b.String(), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 6 && fields[0] == "Mihomo" && fields[1] == "Meta" && kernel.ReportsVersion(fields[2], l.Tag) && fields[3] == "linux" && fields[5] == "with" {
				return nil
			}
		}
		return errors.New("Mihomo executable reported a different version")
	}
	return nil
}

// restrictedArgs admits the two checks inspect runs, mihomo -v and mihomo -t, and no
// invocation that would leave a core running.
func restrictedArgs(args []string) (string, []string, error) {
	if len(args) < 2 || !filepath.IsAbs(args[0]) || filepath.Clean(args[0]) != args[0] {
		return "", nil, errors.New("invalid restricted invocation")
	}
	isVersion := len(args) == 2 && args[1] == "-v"
	isCheck := len(args) == 6 && args[1] == "-t" && args[2] == "-d" && args[3] == runtimeHome && args[4] == "-f" && (args[5] == "-" || filepath.IsAbs(args[5]) && filepath.Clean(args[5]) == args[5])
	if !(isVersion || isCheck) {
		return "", nil, errors.New("invalid restricted operation")
	}
	return args[0], []string{"PATH=/usr/bin:/bin", "HOME=/", "LANG=C"}, nil
}
