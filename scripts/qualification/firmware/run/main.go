// Command run boots an official ARM64 kernel/rootfs in QEMU and exercises the project's actual
// network helper. Run it from the repository root:
//
//	go run ./scripts/qualification/firmware/run VERSION --cache DIR --log FILE [options]
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	here  = "scripts/qualification/firmware"
	image = "hyper-qualification-vm:arm64"
)

type options struct {
	version, cache, dockerContext, packages, log, report string
	guestOverlay, guestScript, networkPackage, firewall  string
	diskMiB                                              int
	powerCut                                             bool
}

func usage(msg string) {
	fmt.Fprintln(os.Stderr, "run:", msg)
	flag.Usage()
	os.Exit(2)
}

func main() {
	var o options
	flag.StringVar(&o.cache, "cache", "", "official image cache directory (required)")
	flag.StringVar(&o.dockerContext, "docker-context", "default", "Docker context")
	flag.StringVar(&o.packages, "packages", "", "prerequisite packages from the packages command")
	flag.StringVar(&o.log, "log", "", "serial log file (required)")
	flag.StringVar(&o.report, "report", "", "write redacted measurements as JSON")
	flag.StringVar(&o.guestOverlay, "guest-overlay", "", "additional files to stage under /")
	flag.StringVar(&o.guestScript, "guest-script", "", "replace the standard network guest test")
	flag.IntVar(&o.diskMiB, "disk-mib", 0, "128..4096")
	flag.StringVar(&o.networkPackage, "network-package", "", "install this ARM64-compatible Hyper installer for the standard network fixture")
	flag.BoolVar(&o.powerCut, "power-cut", false, "cut power once and boot the same disk again")
	flag.StringVar(&o.firewall, "firewall", "fw4", "fw3 or fw4")
	// Accept the version before or after the options.
	argv := os.Args[1:]
	for len(argv) > 0 {
		flag.CommandLine.Parse(argv)
		if flag.NArg() == 0 {
			break
		}
		if o.version != "" {
			usage("unexpected argument " + flag.Arg(0))
		}
		o.version, argv = flag.Arg(0), flag.Args()[1:]
	}
	switch {
	case !slices.Contains([]string{"23.05.6", "24.10.8", "25.12.6"}, o.version):
		usage("expected 23.05.6, 24.10.8 or 25.12.6")
	case o.cache == "" || o.log == "":
		usage("--cache and --log are required")
	case o.firewall != "fw3" && o.firewall != "fw4":
		usage("--firewall must be fw3 or fw4")
	case o.diskMiB != 0 && (o.diskMiB < 128 || o.diskMiB > 4096):
		usage("--disk-mib must be 128..4096")
	case o.firewall == "fw3" && o.packages == "":
		usage("fw3 requires the matching --firewall fw3 packages")
	}
	passed, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	if !passed {
		os.Exit(1)
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetch(url, dest string) error {
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	if dest == "" {
		return nil
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func copyFile(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, 0)
	})
}

func command(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func stageNetworkSources(overlay string) error {
	for _, f := range []string{"usr/libexec/hyper/network", "usr/libexec/hyper/firewall-backend",
		"usr/libexec/hyper/fault-policy", "etc/config/hyper",
		"usr/share/nftables.d/chain-pre/input/90-hyper.nft", "usr/share/nftables.d/chain-pre/forward/90-hyper.nft",
		"usr/share/nftables.d/ruleset-post/90-hyper.nft"} {
		if err := copyFile(filepath.Join("files", f), filepath.Join(overlay, f), 0); err != nil {
			return err
		}
	}
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(overlay, "usr/sbin/hyper-net"), "./cmd/hyper-net")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	return build.Run()
}

var luciPackage = regexp.MustCompile(`^luci-app-hyper[-_][A-Za-z0-9_.-]+$`)

func run(o options) (bool, error) {
	var packageSHA string
	if pkg := o.networkPackage; pkg != "" {
		suffix := ".ipk"
		if strings.HasPrefix(o.version, "25.") {
			suffix = ".apk"
		}
		info, err := os.Stat(pkg)
		if o.guestOverlay != "" || o.guestScript != "" || err != nil || !info.Mode().IsRegular() ||
			filepath.Ext(pkg) != suffix || !luciPackage.MatchString(filepath.Base(pkg)) {
			usage("--network-package requires the matching Hyper installer and the standard guest fixture")
		}
		if packageSHA, err = sha256File(pkg); err != nil {
			return false, err
		}
	}
	docker := func(args ...string) []string {
		return append([]string{"docker", "--context", o.dockerContext}, args...)
	}
	base := "https://downloads.openwrt.org/releases/" + o.version + "/targets/armsr/armv8/"
	if err := os.MkdirAll(o.cache, 0o755); err != nil {
		return false, err
	}
	sumsFile := filepath.Join(o.cache, "sha256sums-"+o.version)
	if err := fetch(base+"sha256sums", sumsFile); err != nil {
		return false, err
	}
	sumsRaw, err := os.ReadFile(sumsFile)
	if err != nil {
		return false, err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(string(sumsRaw), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			sums[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	prefix := "openwrt-" + o.version + "-armsr-armv8-"
	names := []string{prefix + "generic-kernel.bin", prefix + "generic-ext4-rootfs.img.gz"}
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := filepath.Join(o.cache, name)
			if _, err := os.Stat(path); err != nil {
				if errs[i] = fetch(base+name, path); errs[i] != nil {
					return
				}
			}
			if d, err := sha256File(path); err != nil || d != sums[name] {
				errs[i] = errors.New("official firmware digest mismatch")
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	// The container runs the statically linked runner for its own architecture.
	version := docker("version", "-f", "{{.Server.Arch}}")
	arch, err := exec.Command(version[0], version[1:]...).Output()
	if err != nil {
		return false, fmt.Errorf("docker version: %w", err)
	}
	if build := docker("build", "-q", "-t", image, here); command(build[0], build[1:]...) != nil {
		return false, errors.New("cannot build the qualification image")
	}
	tmp, err := os.MkdirTemp("", "hyper-firmware-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	runner := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(tmp, "runner"), "./"+here+"/runner")
	runner.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+strings.TrimSpace(string(arch)), "CGO_ENABLED=0")
	runner.Stdout, runner.Stderr = os.Stderr, os.Stderr
	if err := runner.Run(); err != nil {
		return false, err
	}
	overlay := filepath.Join(tmp, "overlay")
	if pkg := o.networkPackage; pkg != "" {
		candidate := filepath.Join(overlay, "root/network-candidate")
		if err := copyFile(pkg, filepath.Join(candidate, filepath.Base(pkg)), 0o644); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(candidate, "SHA256SUMS"), []byte(packageSHA+"  "+filepath.Base(pkg)+"\n"), 0o644); err != nil {
			return false, err
		}
		clock := filepath.Join(overlay, "root/clock-fixture")
		if err := os.MkdirAll(clock, 0o755); err != nil {
			return false, err
		}
		cert := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
			"-subj", "/CN=Hyper guest fixture", "-addext", "subjectAltName=IP:127.0.0.1",
			"-days", "2", "-keyout", filepath.Join(clock, "server.key"), "-out", filepath.Join(clock, "server.crt"))
		if err := cert.Run(); err != nil {
			return false, fmt.Errorf("openssl: %w", err)
		}
		if err := os.WriteFile(filepath.Join(clock, "epoch"), []byte(strconv.FormatInt(time.Now().Unix(), 10)+"\n"), 0o644); err != nil {
			return false, err
		}
		if err := copyFile(filepath.Join(here, "clock-retry.sh"), filepath.Join(clock, "test"), 0); err != nil {
			return false, err
		}
	} else if err := stageNetworkSources(overlay); err != nil {
		return false, err
	}
	if err := copyFile(filepath.Join(here, "setup-network.sh"), filepath.Join(overlay, "root/setup-network"), 0); err != nil {
		return false, err
	}
	if o.firewall == "fw3" {
		if err := os.WriteFile(filepath.Join(overlay, "root/enable-fw3"), nil, 0o644); err != nil {
			return false, err
		}
	}
	if o.guestOverlay != "" {
		if err := copyTree(o.guestOverlay, overlay); err != nil {
			return false, err
		}
	}
	if o.powerCut {
		if o.guestScript == "" {
			return false, errors.New("a power-cut guest script is required")
		}
		if err := os.WriteFile(filepath.Join(overlay, "root/enable-powercut"), nil, 0o644); err != nil {
			return false, err
		}
	}
	guest := o.guestScript
	if guest == "" {
		guest = filepath.Join(here, "guest.sh")
	}
	if err := copyFile(guest, filepath.Join(overlay, "root/fw-qualify"), 0); err != nil {
		return false, err
	}
	if o.packages != "" {
		if err := copyTree(o.packages, filepath.Join(overlay, "root/packages")); err != nil {
			return false, err
		}
	}
	for _, name := range names {
		if err := copyFile(filepath.Join(o.cache, name), filepath.Join(tmp, name), 0o644); err != nil {
			return false, err
		}
	}
	staged := map[string]string{}
	err = filepath.WalkDir(overlay, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(overlay, path)
		if strings.HasPrefix(filepath.ToSlash(rel), "root/packages/") {
			return nil
		}
		staged[filepath.ToSlash(rel)], err = sha256File(path)
		return err
	})
	if err != nil {
		return false, err
	}
	create := docker("create", image, "/data/runner")
	if o.diskMiB != 0 {
		create = append(create, "--disk-mib", strconv.Itoa(o.diskMiB))
	}
	if o.powerCut {
		create = append(create, "--power-cut")
	}
	create = append(create, "/data", o.version)
	cidRaw, err := exec.Command(create[0], create[1:]...).Output()
	if err != nil {
		return false, fmt.Errorf("docker create: %w", err)
	}
	cid := strings.TrimSpace(string(cidRaw))
	rm := docker("rm", "-f", cid)
	defer exec.Command(rm[0], rm[1:]...).Run()
	cp := docker("cp", tmp, cid+":/data")
	if err := command(cp[0], cp[1:]...); err != nil {
		return false, err
	}
	logFile, err := os.Create(o.log)
	if err != nil {
		return false, err
	}
	timeout := 680 * time.Second
	if o.powerCut {
		timeout = 1340 * time.Second
	}
	start := docker("start", "-a", cid)
	boot := exec.Command(start[0], start[1:]...)
	boot.Stdout, boot.Stderr = logFile, logFile
	if err := boot.Start(); err != nil {
		logFile.Close()
		return false, err
	}
	timer := time.AfterFunc(timeout, func() { boot.Process.Kill() })
	err = boot.Wait()
	timer.Stop()
	logFile.Close()
	if err != nil {
		return false, fmt.Errorf("firmware run: %w", err)
	}
	return evaluate(o, packageSHA, names, sums, staged)
}

var (
	resultKeys = map[string]bool{"fw3": true, "fw4": true, "dnsmasq": true, "clock_retry": true, "dns_baseline": true,
		"prerequisites": true, "conflict_scan": true, "running_conflict_scan": true, "concurrent_conflict_scan": true,
		"original_postinst_not_found": true, "upgrade_postinst": true, "first_postinst": true,
		"ruleset_watch_permissions": true, "core_identity": true, "live_rule_providers": true, "core_permission_logs": true,
		"cleanup_failure": true, "repeat_stop": true, "missing_supervisor": true,
		"daemon_official_archive_install": true, "daemon_incompatible_config_rejected": true,
		"daemon_failed_candidate_rollback": true, "daemon_stop": true, "daemon_powercut_recovery": true,
		"daemon_candidate_pending": true, "daemon_lifecycle": true}
	resultStage  = regexp.MustCompile(`^(tun|tproxy)_(apply|verify|fw[34]_reload|release|dns_takeover|dns_restored)$`)
	states       = map[string]bool{"pass": true, "failed": true, "running": true, "unreachable": true, "missing": true, "unsupported": true}
	kernelString = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){2}(?:-[A-Za-z0-9.-]+)?$`)
	seconds      = regexp.MustCompile(`^[0-9]{1,5}$`)
	phase        = regexp.MustCompile(`\blifecycle phase=(starting|running|stopping|stopped|rolling_back)\b`)
)

func evaluate(o options, packageSHA string, names []string, sums, staged map[string]string) (bool, error) {
	raw, err := os.ReadFile(o.log)
	if err != nil {
		return false, err
	}
	text := strings.ToValidUTF8(string(raw), "�")
	results := map[string]string{}
	cuts := 0
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "HYPER_") {
			fmt.Println(strings.TrimSpace(line))
		}
		if line == "HYPER_HYPERVISOR_CUT" {
			cuts++
		}
		if rest, ok := strings.CutPrefix(line, "HYPER_RESULT "); ok {
			if k, v, ok := strings.Cut(strings.TrimSpace(rest), "="); ok {
				results[k] = v
			}
		}
	}
	passed := results["pid1"] == "procd" && results["dns_baseline"] == "pass" &&
		results[o.firewall] == "running" && results["dnsmasq"] == "running"
	for _, mode := range []string{"tproxy", "tun"} {
		for _, stage := range []string{"apply", "dns_takeover", "verify", o.firewall + "_reload", "release", "dns_restored"} {
			passed = passed && results[mode+"_"+stage] == "pass"
		}
	}
	if o.networkPackage != "" {
		passed = passed && results["installed_package_sha256"] == packageSHA && results["clock_retry"] == "pass"
	}
	if o.guestScript != "" {
		passed = results["daemon_lifecycle"] == "pass"
	}
	if o.powerCut {
		passed = passed && cuts == 1 && results["daemon_powercut_recovery"] == "pass"
	}
	// Restrict the report to this harness's own counters and status words.
	safe := map[string]any{}
	for k, v := range results {
		if (resultKeys[k] || resultStage.MatchString(k)) && states[v] {
			safe[k] = v
		}
	}
	if o.networkPackage != "" && results["installed_package_sha256"] == packageSHA {
		safe["installed_package_sha256"] = packageSHA
	}
	if o.powerCut {
		safe["hypervisor_power_cuts"] = cuts
	}
	if results["distribution"] == o.version {
		safe["distribution"] = o.version
	}
	if kernelString.MatchString(results["kernel"]) {
		safe["kernel"] = results["kernel"]
	}
	if results["pid1"] == "procd" {
		safe["pid1"] = "procd"
	}
	if strings.HasPrefix(results["management"], "local ") {
		safe["management"] = "local_route"
	}
	if seconds.MatchString(results["daemon_first_confirm_s"]) {
		safe["daemon_first_confirm_s"] = results["daemon_first_confirm_s"]
	}
	phases := []string{}
	for _, m := range phase.FindAllStringSubmatch(text, -1) {
		phases = append(phases, m[1])
	}
	if o.report != "" {
		serial, err := sha256File(o.log)
		if err != nil {
			return false, err
		}
		method := "QEMU ARM64 official ext4 image with "
		if o.guestScript != "" {
			method += filepath.Base(o.guestScript)
		} else {
			method += "real procd/" + o.firewall + "/dnsmasq, CPU-only DNS canary supplied by a local dnsmasq fixture"
		}
		artifacts := map[string]string{}
		for _, n := range names {
			artifacts[n] = sums[n]
		}
		report, err := json.MarshalIndent(map[string]any{
			"verified_utc": time.Now().UTC().Format(time.RFC3339Nano), "target": "armsr/armv8",
			"artifacts": artifacts, "staged": staged, "method": method, "serial_sha256": serial,
			"lifecycle": phases, "results": safe, "passed": passed,
		}, "", "  ")
		if err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(o.report), 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(o.report, append(report, '\n'), 0o644); err != nil {
			return false, err
		}
	}
	return passed, nil
}
