// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package update checks GitHub Releases for a newer luci-app-hyper and installs it.
//
// Release assets (built by .github/workflows/ci.yml), one package per firmware family and CPU:
//
//	23.05-24.10_luci-app-hyper_<ver>_<arch>.ipk
//	25.12+_luci-app-hyper-<ver>-<arch>.apk
//	sha256sums
//
// The download is verified against sha256sums from the same release, then installed by a
// detached `hyperd install-update` process: the package replaces hyperd itself.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/lazyre"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDir  = "/var/run/hyper-software-update"
	DefaultRepo = "HyperPteLtd/openwrt-hyper"
	Package     = "luci-app-hyper"
)

// Release is the part of GitHub's latest-release reply hyper uses.
type Release struct {
	Version     string  `json:"-"`
	Tag         string  `json:"tag_name"`
	PublishedAt string  `json:"published_at"`
	Body        string  `json:"body"`
	URL         string  `json:"html_url"`
	Assets      []Asset `json:"assets"`
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// PackageManager is the firmware's package manager and arch.
type PackageManager struct {
	Name   string `json:"name"`   // opkg | apk
	Family string `json:"family"` // asset prefix
	Ext    string `json:"ext"`
	Arch   string `json:"arch"`
}

// State is state.json, shown as status().update.state.
type State struct {
	State   string `json:"state"` // installing | done | failed
	Version string `json:"version"`
	Time    int64  `json:"time"`
	Error   string `json:"error,omitempty"`
}

// Job is install.json, handed to the detached installer.
type Job struct {
	Manager   string   `json:"manager"`
	Files     []string `json:"files"`
	Version   string   `json:"version"`
	Automatic bool     `json:"automatic"`
}

// Updater talks to GitHub through Get (which may retry over the core's proxy).
type Updater struct {
	Dir  string
	Repo string
	// Token is config.github_token (optional, raises the API rate limit).
	Token string
	Get   func(ctx context.Context, url string, header http.Header) (*http.Response, error)
	// Run executes a command and returns its stdout (package manager queries).
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Exists reports whether a path exists (apk detection).
	Exists func(string) bool
}

var repoRe = lazyre.New(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (u *Updater) get(ctx context.Context, url string, h http.Header, limit int64) ([]byte, int, error) {
	res, err := u.Get(ctx, url, h)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if int64(len(b)) > limit {
		return nil, res.StatusCode, errors.New("response too large")
	}
	return b, res.StatusCode, nil
}

// Latest returns the latest release.
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	repo := u.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	if !repoRe().MatchString(repo) {
		return nil, ErrRepo
	}
	h := http.Header{"Accept": {"application/vnd.github+json"}, "User-Agent": {Package}}
	if u.Token != "" {
		h.Set("Authorization", "Bearer "+u.Token)
	}
	b, status, err := u.get(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", h, 4<<20)
	if err != nil {
		return nil, err
	}
	var rel Release
	if status != 200 || json.Unmarshal(b, &rel) != nil || rel.Tag == "" {
		return nil, StatusError(status)
	}
	rel.Version = strings.TrimPrefix(rel.Tag, "v")
	return &rel, nil
}

// Manager detects apk (OpenWrt 25.12+) or opkg and the most specific arch.
func (u *Updater) Manager(ctx context.Context) PackageManager {
	if u.Exists("/usr/bin/apk") && u.Exists("/etc/apk") {
		out, _ := u.Run(ctx, "/usr/bin/apk", "--print-arch")
		return PackageManager{"apk", "25.12+", ".apk", strings.TrimSpace(string(out))}
	}
	out, _ := u.Run(ctx, "/bin/opkg", "print-architecture")
	arch := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 3 && f[0] == "arch" && f[1] != "all" && f[1] != "noarch" {
			if _, err := strconv.Atoi(f[2]); err == nil {
				arch = f[1]
			}
		}
	}
	return PackageManager{"opkg", "23.05-24.10", ".ipk", arch}
}

// Pick returns the package asset for pm and the checksum asset.
func Pick(assets []Asset, pm PackageManager) (pkg, sums *Asset) {
	prefix := pm.Family + "_" + Package
	sep := "-"
	if pm.Ext == ".ipk" {
		sep = "_"
	}
	suffix := sep + pm.Arch + pm.Ext
	for i := range assets {
		a := &assets[i]
		switch {
		case a.Name == "sha256sums":
			sums = a
		case pkg == nil && pm.Arch != "" && strings.HasPrefix(a.Name, prefix) && strings.HasSuffix(a.Name, suffix):
			pkg = a
		}
	}
	return
}

// Normalize strips a leading "v" and OpenWrt's "-r<PKG_RELEASE>" suffix.
func Normalize(v string) string {
	return revision().ReplaceAllString(strings.TrimPrefix(strings.TrimSpace(v), "v"), "")
}

var revision = lazyre.New(`-r[0-9]+$`)

// Compare returns -1, 0 or 1. The installed "0.2.0-r1" equals the release tag "v0.2.0";
// a pre-release ("0.2.0-beta.1") sorts before its release; missing parts count as 0.
func Compare(a, b string) int {
	parse := func(v string) (main, pre []string) {
		v = Normalize(v)
		head, tail, dash := strings.Cut(v, "-")
		if head != "" {
			main = strings.Split(head, ".")
		}
		if dash {
			pre = strings.Split(tail, ".")
		}
		return
	}
	xm, xp := parse(a)
	ym, yp := parse(b)
	for i := 0; i < max(len(xm), len(ym)); i++ {
		p, q := "0", "0"
		if i < len(xm) {
			p = xm[i]
		}
		if i < len(ym) {
			q = ym[i]
		}
		if c := part(p, q); c != 0 {
			return c
		}
	}
	if len(xp) == 0 || len(yp) == 0 {
		switch {
		case len(xp) > 0:
			return -1
		case len(yp) > 0:
			return 1
		}
		return 0
	}
	for i := 0; i < max(len(xp), len(yp)); i++ {
		if i >= len(xp) {
			return -1
		}
		if i >= len(yp) {
			return 1
		}
		if c := part(xp[i], yp[i]); c != 0 {
			return c
		}
	}
	return 0
}

var digits = lazyre.New(`^[0-9]+$`)

// part compares one identifier: numbers numerically, before words (semver).
func part(x, y string) int {
	nx, ny := digits().MatchString(x), digits().MatchString(y)
	switch {
	case nx && ny:
		a, _ := strconv.ParseUint(x, 10, 64)
		b, _ := strconv.ParseUint(y, 10, 64)
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	case nx:
		return -1
	case ny:
		return 1
	}
	return strings.Compare(x, y)
}

var assetName = lazyre.New(`^[A-Za-z0-9._+-]+$`)

// Prepare downloads and verifies the package for rel; it returns the install job.
func (u *Updater) Prepare(ctx context.Context, rel *Release, pm PackageManager, automatic bool) (*Job, error) {
	pkg, sums := Pick(rel.Assets, pm)
	if pkg == nil {
		return nil, fmt.Errorf("%w: no %s package for %s in release %s", ErrNoPackage, pm.Ext, pm.Arch, rel.Version)
	}
	if sums == nil {
		return nil, fmt.Errorf("%w: release has no sha256sums", ErrChecksum)
	}
	if !assetName().MatchString(pkg.Name) {
		return nil, fmt.Errorf("illegal asset name %s", pkg.Name)
	}
	b, status, err := u.get(ctx, sums.URL, http.Header{"User-Agent": {Package}}, 1<<20)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("sha256sums: %w", fetchErr(err, status))
	}
	want := ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && len(f[0]) == 64 && strings.TrimPrefix(f[1], "*") == pkg.Name {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return nil, fmt.Errorf("%w: no checksum for %s", ErrChecksum, pkg.Name)
	}
	dl := filepath.Join(u.Dir, "download")
	os.RemoveAll(dl)
	if err := os.MkdirAll(dl, 0o700); err != nil {
		return nil, err
	}
	data, status, err := u.get(ctx, pkg.URL, http.Header{"User-Agent": {Package}}, 64<<20)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("%s: %w", pkg.Name, fetchErr(err, status))
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		os.RemoveAll(dl)
		return nil, fmt.Errorf("%w mismatch for %s", ErrChecksum, pkg.Name)
	}
	path := filepath.Join(dl, pkg.Name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return &Job{Manager: pm.Name, Files: []string{path}, Version: rel.Version, Automatic: automatic}, nil
}

func fetchErr(err error, status int) error {
	if err != nil {
		return err
	}
	return StatusError(status)
}

// Kinds of failure the router page tells apart (cmd/hyperd's updateCode).
var (
	ErrRepo      = errors.New("illegal repo")
	ErrNoPackage = errors.New("no package for this router")
	ErrChecksum  = errors.New("checksum")
)

// StatusError is an HTTP status other than 200 from GitHub.
type StatusError int

func (e StatusError) Error() string { return "HTTP " + strconv.Itoa(int(e)) }

// ReadState returns state.json, nil when absent.
func ReadState(dir string) *State {
	var s State
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

// WriteState replaces state.json.
func WriteState(dir string, s State) error {
	b, _ := json.Marshal(s)
	tmp := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "state.json"))
}

// Install runs the job (in the detached installer): the package manager, then cleanup
// and the final state. autoAllowed is re-read so turning off automatic updates cancels.
func Install(ctx context.Context, dir string, job Job, autoAllowed func() bool) State {
	st := State{State: "failed", Version: job.Version}
	defer os.RemoveAll(filepath.Join(dir, "download"))
	prefix := filepath.Join(dir, "download") + "/"
	valid := (job.Manager == "opkg" || job.Manager == "apk") && len(job.Files) > 0
	for _, f := range job.Files {
		if !strings.HasPrefix(f, prefix) || !assetName().MatchString(strings.TrimPrefix(f, prefix)) {
			valid = false
		}
	}
	switch {
	case !valid:
		st.Error = "update_failed"
	case job.Automatic && !autoAllowed():
		st.Error = "automatic_update_disabled"
	default:
		name, args := "/bin/opkg", []string{"install"}
		if job.Manager == "apk" {
			name, args = "/usr/bin/apk", []string{"add", "--allow-untrusted"}
		}
		log, _ := os.Create(filepath.Join(dir, "install.log"))
		cmd := exec.CommandContext(ctx, name, append(args, job.Files...)...)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Run(); err == nil {
			st.State = "done"
		} else {
			st.Error = "install_failed"
		}
		if log != nil {
			log.Close()
		}
	}
	st.Time = time.Now().Unix()
	return st
}
