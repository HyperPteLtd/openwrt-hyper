package kernel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aead.dev/minisign"
)

var buildFixture struct {
	sync.Once
	bin []byte
	err error
}

func tinyCore(t *testing.T) []byte {
	t.Helper()
	buildFixture.Do(func() {
		dir, e := os.MkdirTemp("", "ow-test-cli-")
		if e != nil {
			buildFixture.err = e
			return
		}
		defer os.RemoveAll(dir)
		main := filepath.Join(dir, "main.go")
		buildFixture.err = os.WriteFile(main, []byte("package main\nfunc main(){}\n"), 0600)
		if buildFixture.err != nil {
			return
		}
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dir, "core"), main)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=")
		if out, e := cmd.CombinedOutput(); e != nil {
			buildFixture.err = errors.New(string(out))
			return
		}
		buildFixture.bin, buildFixture.err = os.ReadFile(filepath.Join(dir, "core"))
	})
	if buildFixture.err != nil {
		t.Fatal(buildFixture.err)
	}
	return buildFixture.bin
}

type fixture struct {
	mgr  *Manager
	priv minisign.PrivateKey
	cfg  string
	tmp  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, priv, e := minisign.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	m, e := New(Options{Root: filepath.Join(root, "state"), Temp: filepath.Join(root, "tmp"), Target: "amd64", Key: pub.String(), Inspect: func(_ context.Context, l Launch, cfg string) error {
		_, e := os.Stat(l.Path)
		return e
	}, Capacity: func(string, string) (Capacity, error) {
		return Capacity{Persistent: Volume{1 << 32, 1 << 32, 1, false}, Temporary: Volume{1 << 32, 1 << 32, 2, true}, RAM: 1 << 31, Available: 1 << 31}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	cfg := filepath.Join(root, "config.yaml")
	if e = os.WriteFile(cfg, []byte("proxies: []\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return &fixture{m, priv, cfg, root}
}
func (f fixture) bundle(t *testing.T, tag, build string) (Source, Manifest) {
	t.Helper()
	b := tinyCore(t)
	var raw bytes.Buffer
	z := gzip.NewWriter(&raw)
	w := tar.NewWriter(z)
	if e := w.WriteHeader(&tar.Header{Name: strings.TrimSuffix(assetName(tag, "amd64"), ".tar.gz") + "/mihomo", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(b))}); e != nil {
		t.Fatal(e)
	}
	w.Write(b)
	w.Close()
	z.Close()
	c := Capabilities{1, Repository, tag, build, map[string]TargetCapabilities{"amd64": {Control: Controller, TUN: true, TProxy: true, PeakRAM: 32 * MiB}}}
	cb, _ := json.Marshal(c)
	a := Asset{assetName(tag, "amd64"), "amd64", "little", "native", int64(raw.Len()), int64(len(b)), Digest(raw.Bytes()), []Member{{strings.TrimSuffix(assetName(tag, "amd64"), ".tar.gz") + "/mihomo", "core", int64(len(b)), Digest(b)}}}
	m := Manifest{1, Repository, tag, build, Digest(cb), map[string]Asset{"amd64": a}}
	mb, _ := json.Marshal(m)
	dir := t.TempDir()
	parts := map[string][]byte{"manifest.json": mb, "manifest.json.minisig": minisign.Sign(f.priv, mb), "capabilities.json": cb, a.Name: raw.Bytes()}
	for n, v := range parts {
		if e := os.WriteFile(filepath.Join(dir, n), v, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return Source{Bundle: dir}, m
}
func TestSignedOfflineReleaseAndRejections(t *testing.T) {
	f := newFixture(t)
	src, m := f.bundle(t, "v1.0.0", "build1")
	ctx := context.Background()
	mat, v, e := src.metadata(ctx, m.Tag, "amd64", f.mgr.Key)
	if e != nil || v.Target.PeakRAM != 32*MiB {
		t.Fatal(v, e)
	}
	p, _, _ := minisign.GenerateKey(rand.Reader)
	for _, test := range []struct {
		key, tag, target string
		doc, sig, cap    []byte
	}{
		{"", m.Tag, "amd64", mat.manifest, mat.sig, mat.caps}, {p.String(), m.Tag, "amd64", mat.manifest, mat.sig, mat.caps}, {f.mgr.Key, "v9.0.0", "amd64", mat.manifest, mat.sig, mat.caps}, {f.mgr.Key, m.Tag, "arm64", mat.manifest, mat.sig, mat.caps}, {f.mgr.Key, m.Tag, "amd64", mat.manifest, nil, mat.caps}, {f.mgr.Key, m.Tag, "amd64", mat.manifest, mat.sig, append([]byte{' '}, mat.caps...)},
	} {
		if _, e := Verify(test.key, test.tag, test.target, test.doc, test.sig, test.cap); e == nil {
			t.Fatal("invalid material accepted")
		}
	}
	if e = f.mgr.Install(ctx, m.Tag, "double", src); e != nil {
		t.Fatal(e)
	}
	alt, _ := f.bundle(t, m.Tag, "different-build")
	if e = f.mgr.Install(ctx, m.Tag, "double", alt); e == nil {
		t.Fatal("replacement of existing tag accepted")
	}
	os.WriteFile(filepath.Join(src.Bundle, m.Assets["amd64"].Name), []byte("broken"), 0600)
	if e = f.mgr.Install(ctx, m.Tag, "double", src); e == nil {
		t.Fatal("damaged offline asset accepted")
	}
}
func TestTransactionsAndRecovery(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"double", "single", "tmp"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			mgr := f.mgr
			v1, _ := f.bundle(t, "v1.0.0", "a")
			v1.Cache = filepath.Join(f.tmp, "cached")
			if e := mgr.Install(ctx, "v1.0.0", mode, v1); e != nil {
				t.Fatal(e)
			}
			if _, e := mgr.Active(); e == nil {
				t.Fatal("offline selection became active")
			}
			l, e := mgr.BeginAuthorized(ctx, f.cfg, "", nil)
			if e != nil || l.Token == "" {
				t.Fatal(l, e)
			}
			if e = mgr.Abort("bad"); e == nil {
				t.Fatal("stale token accepted")
			}
			active, e := mgr.Confirm(ctx, l.Token)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = os.Stat(active.Path); e != nil {
				t.Fatal(e)
			}
			l, e = mgr.BeginAuthorized(ctx, f.cfg, "", nil)
			if e != nil || l.Token != "" {
				t.Fatal("unchanged config should reuse confirmation", l, e)
			}
			if mode == "tmp" {
				os.RemoveAll(mgr.Temp)
				a, e := mgr.Recover(ctx)
				if e != nil || a.Tag != "v1.0.0" {
					t.Fatal(a, e)
				}
			}
			v2, _ := f.bundle(t, "v2.0.0", "b")
			if e = mgr.Install(ctx, "v2.0.0", mode, v2); e != nil {
				t.Fatal(e)
			}
			l, e = mgr.BeginAuthorized(ctx, f.cfg, "", nil)
			if e != nil {
				t.Fatal(e)
			}
			a, e := mgr.Recover(ctx)
			if e != nil || a.Tag != "v1.0.0" {
				t.Fatal("power loss before confirmation", a, e)
			}
			if _, e = mgr.Confirm(ctx, l.Token); e == nil {
				t.Fatal("recovered transaction token reused")
			}
			if e = mgr.Install(ctx, "v2.0.0", mode, v2); e != nil {
				t.Fatal(e)
			}
			l, e = mgr.BeginAuthorized(ctx, f.cfg, "", nil)
			if e != nil {
				t.Fatal(e)
			}
			a, e = mgr.Confirm(ctx, l.Token)
			if e != nil || a.Tag != "v2.0.0" {
				t.Fatal(a, e)
			}
			e = mgr.Select("", true)
			if (mode == "double") != (e == nil) {
				t.Fatal("rollback availability", e)
			}
			if mode == "double" {
				if _, err := mgr.BeginAuthorized(ctx, "/missing-authorized-config", "", nil); err == nil {
					t.Fatal("rollback reused archived credentials")
				}
				l, e = mgr.BeginAuthorized(ctx, f.cfg, "", nil)
				if e != nil || l.Tag != "v1.0.0" {
					t.Fatal(l, e)
				}
			}
		})
	}
}
func TestSafetyBoundaries(t *testing.T) {
	c := Capacity{Persistent: Volume{256 * MiB, 76 * MiB, 1, false}, Temporary: Volume{128 * MiB, 120 * MiB, 2, true}, RAM: 256 * MiB, Available: 193 * MiB}
	if e := CheckCapacity(c, "double", 10*MiB, 70*MiB, 60*MiB, 0); e == nil {
		t.Fatal("overlay headroom lost")
	}
	if e := CheckCapacity(c, "single", 10*MiB, 70*MiB, 60*MiB, 68*MiB); e != nil {
		t.Fatal(e)
	}
	c.Available -= 10 * MiB
	if e := CheckCapacity(c, "single", 10*MiB, 70*MiB, 60*MiB, 68*MiB); e == nil {
		t.Fatal("tmpfs RAM reserve lost")
	}
	for _, u := range []string{"http://a.invalid/release", "https://name:password@a.invalid", "/srv/asset"} {
		if _, e := trustedTransportURL(u); e == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	if e := copyChecked(io.Discard, strings.NewReader("abcX"), 3, Digest([]byte("abc"))); e == nil {
		t.Fatal("accepted trailing data")
	}
}

func TestSingleSlotPostCommitPowerCut(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m := f.mgr
	one, _ := f.bundle(t, "v1.0.0", "first")
	if e := m.Install(ctx, "v1.0.0", "single", one); e != nil {
		t.Fatal(e)
	}
	l, e := m.BeginAuthorized(ctx, f.cfg, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Confirm(ctx, l.Token); e != nil {
		t.Fatal(e)
	}
	two, _ := f.bundle(t, "v2.0.0", "next")
	two.Cache = filepath.Join(f.tmp, "recovery-cache")
	if e = m.Install(ctx, "v2.0.0", "single", two); e != nil {
		t.Fatal(e)
	}
	l, e = m.BeginAuthorized(ctx, f.cfg, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	// Cut power immediately after the durable commit intent but before the
	// old persistent inode was reclaimed or a candidate byte was copied.
	s, e := m.read()
	if e != nil {
		t.Fatal(e)
	}
	old := *s.Active
	candidate := s.Pending.Candidate
	s.Active = &candidate
	s.Selected = &candidate
	s.Previous = nil
	s.Pending = nil
	s.Repair = true
	s.Evict = []pick{old}
	if e = m.commit(&s); e != nil {
		t.Fatal(e)
	}
	os.RemoveAll(m.Temp)
	if _, e = m.Active(); e == nil {
		t.Fatal("reported incomplete single-slot commit as running")
	}
	got, e := m.Recover(ctx)
	if e != nil || got.Tag != "v2.0.0" {
		t.Fatal(got, e)
	}
	if _, e = os.Stat(m.slot(old)); !os.IsNotExist(e) {
		t.Fatal("old slot was not reclaimed after commit")
	}
	if _, e = m.Confirm(ctx, l.Token); e == nil {
		t.Fatal("confirmed pre-reboot token")
	}
}

func TestStaleIntentSchemaAndSpace(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m := f.mgr
	one, _ := f.bundle(t, "v1.0.0", "first")
	if e := m.Install(ctx, "v1.0.0", "double", one); e != nil {
		t.Fatal(e)
	}
	if _, e := m.BeginAuthorized(ctx, f.cfg, "v2.0.0", nil); e == nil {
		t.Fatal("started core with a different compilation target")
	}
	l, e := m.BeginAuthorized(ctx, f.cfg, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Confirm(ctx, l.Token); e != nil {
		t.Fatal(e)
	}
	two, _ := f.bundle(t, "v2.0.0", "next")
	base := m.Capacity
	m.Capacity = func(root, temp string) (Capacity, error) {
		c, e := base(root, temp)
		c.Persistent.Free = 0
		return c, e
	}
	if e = m.Install(ctx, "v2.0.0", "double", two); e == nil {
		t.Fatal("accepted ENOSPC")
	}
	if a, e := m.Active(); e != nil || a.Tag != "v1.0.0" {
		t.Fatal(a, e)
	}
	m.Capacity = base
	inspect := m.Inspect
	m.Inspect = func(ctx context.Context, l Launch, cfg string) error {
		if l.Tag == "v2.0.0" {
			if e := m.Select("v1.0.0", false); e != nil {
				return e
			}
		}
		return inspect(ctx, l, cfg)
	}
	if e = m.Install(ctx, "v2.0.0", "double", two); e == nil {
		t.Fatal("late download overwrote newer intention")
	}
	s, e := m.Status()
	if e != nil || s.Selected != "v1.0.0" {
		t.Fatal(s, e)
	}
	path := filepath.Join(m.Root, "state.json")
	if e = os.WriteFile(path, []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Status(); e != nil {
		t.Fatal("could not recover previous complete state", e)
	}
	if e = os.WriteFile(path, []byte(`{"schema_version":97}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Status(); e == nil {
		t.Fatal("ignored unknown schema")
	}
}

func TestAuthorizationRevocationAcrossCandidateAndRollback(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m := f.mgr
	one, _ := f.bundle(t, "v1.0.0", "one")
	if e := m.Install(ctx, "v1.0.0", "double", one); e != nil {
		t.Fatal(e)
	}
	first, e := m.BeginAuthorized(ctx, f.cfg, "v1.0.0", []string{"old", "shared"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Confirm(ctx, first.Token); e != nil {
		t.Fatal(e)
	}
	two, _ := f.bundle(t, "v2.0.0", "two")
	if e = m.Install(ctx, "v2.0.0", "double", two); e != nil {
		t.Fatal(e)
	}
	second, e := m.BeginAuthorized(ctx, f.cfg, "v2.0.0", []string{"new"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Confirm(ctx, second.Token); e != nil {
		t.Fatal(e)
	}
	unchanged, e := m.Status()
	if e != nil {
		t.Fatal(e)
	}
	preview, e := m.Capabilities("v1.0.0")
	if e != nil || preview.Tag != "v1.0.0" {
		t.Fatal(preview, e)
	}
	after, e := m.Status()
	if e != nil || after != unchanged {
		t.Fatal(after, e)
	}
	if e = m.Invalidate("old"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(first.Config); !os.IsNotExist(e) {
		t.Fatal("historical credential retained", e)
	}
	if _, e = os.Stat(second.Config); e != nil {
		t.Fatal("unrelated signed configuration removed", e)
	}
	if e = m.Select("", true); e != nil {
		t.Fatal(e)
	}
	trial, e := m.BeginAuthorized(ctx, f.cfg, "v1.0.0", []string{"shared"})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Invalidate("shared"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Confirm(ctx, trial.Token); e == nil {
		t.Fatal("revoked pending trial confirmed")
	}
	if _, e = os.Stat(trial.Config); !os.IsNotExist(e) {
		t.Fatal("revoked trial persisted", e)
	}
	if _, e = m.Active(); e != nil {
		t.Fatal("unrelated running config unavailable", e)
	}
	if e = m.Invalidate("new"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Active(); e == nil {
		t.Fatal("revoked active config exposed")
	}
}
func TestSingleSlotKeepsRunningPayloadUntilClosed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	src, _ := f.bundle(t, "v1.0.0", "one")
	if e := f.mgr.Install(ctx, "v1.0.0", "single", src); e != nil {
		t.Fatal(e)
	}
	trial, e := f.mgr.BeginAuthorized(ctx, f.cfg, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	final, e := f.mgr.Confirm(ctx, trial.Token)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(trial.Path); e != nil || final.Path == trial.Path {
		t.Fatal("running payload was unlinked too soon", e)
	}
	if e = f.mgr.Closed(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(trial.Path); !os.IsNotExist(e) {
		t.Fatal("temporary copy retained after process exit", e)
	}
	if _, e = os.Stat(final.Path); e != nil {
		t.Fatal("durable payload removed", e)
	}
}

func TestPreflightDoesNotScheduleOrExposeConfiguration(t *testing.T) {
	f := newFixture(t)
	m := f.mgr
	ctx := context.Background()
	a, _ := f.bundle(t, "v1.0.0", "one")
	if e := m.Install(ctx, "v1.0.0", "double", a); e != nil {
		t.Fatal(e)
	}
	before, e := m.read()
	if e != nil {
		t.Fatal(e)
	}
	var path string
	m.Inspect = func(_ context.Context, l Launch, p string) error {
		path = p
		st, e := os.Stat(p)
		if e != nil || st.Mode().Perm() != 0600 || p == f.cfg || l.Tag != "v1.0.0" {
			t.Fatalf("unrestricted candidate check: %s %v", p, e)
		}
		return errors.New("denied by core")
	}
	if e = m.Check(ctx, "v1.0.0", f.cfg); e == nil {
		t.Fatal("invalid configuration accepted")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("preflight retained a secret", e)
	}
	after, e := m.read()
	if e != nil || after.Pending != nil || after.Revision != before.Revision || after.Selected.Tag != before.Selected.Tag {
		t.Fatal("preflight changed state", after, e)
	}
}

func TestInstallReclaimsInterruptedPayloadBeforeCheckingCapacity(t *testing.T) {
	f := newFixture(t)
	m := f.mgr
	ctx := context.Background()
	src, _ := f.bundle(t, "v1.0.0", "one")
	if e := m.setup(); e != nil {
		t.Fatal(e)
	}
	dangling := filepath.Join(m.Root, "slots", ".unpack-crash")
	if e := os.Mkdir(dangling, 0700); e != nil {
		t.Fatal(e)
	}
	reservation, e := m.reservePayload()
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Install(ctx, "v1.0.0", "double", src); e == nil {
		t.Fatal("double-booked free storage")
	}
	if _, e = os.Stat(dangling); e != nil {
		t.Fatal("deleted another install's files", e)
	}
	reservation.Close()
	if e = m.Install(ctx, "v1.0.0", "double", src); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(dangling); !os.IsNotExist(e) {
		t.Fatal("interrupted extraction leaked", e)
	}
}

func TestStagedInstallRequiresCurrentSelectionRevision(t *testing.T) {
	f := newFixture(t)
	m := f.mgr
	ctx := context.Background()
	one, _ := f.bundle(t, "v1.0.0", "old")
	two, _ := f.bundle(t, "v2.0.0", "new")
	if e := m.Install(ctx, "v1.0.0", "double", one); e != nil {
		t.Fatal(e)
	}
	l, e := m.BeginAuthorized(ctx, f.cfg, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Confirm(ctx, l.Token); e != nil {
		t.Fatal(e)
	}
	old, _ := m.Status()
	if e = m.Install(ctx, "v2.0.0", "double", two, true); e != nil {
		t.Fatal(e)
	}
	staged, e := m.Status()
	if e != nil || staged.Staged != "v2.0.0" || staged.Selected != old.Selected || staged.Active != old.Active {
		t.Fatal("staging selected a binary", staged, e)
	}
	if e = m.Check(ctx, "v2.0.0", f.cfg); e != nil {
		t.Fatal("could not preflight staged binary", e)
	}
	if e = m.Select("v2.0.0", false, old.Revision); e == nil {
		t.Fatal("accepted stale selection")
	}
	if e = m.Select("v2.0.0", false, staged.Revision); e != nil {
		t.Fatal(e)
	}
	active, _ := m.Status()
	if active.Staged != "" || active.Selected != "v2.0.0" || active.Active != "v1.0.0" {
		t.Fatal(active)
	}
}

func Test32BitNativeRequirements(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cgo := range []string{"0", "1"} {
		core := filepath.Join(dir, "core-"+cgo)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", core, main)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=386", "GO386=softfloat", "CGO_ENABLED="+cgo, "GOFLAGS=")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build cgo=%s: %s", cgo, output)
		}
		if err := checkELF(core, "386-softfloat", "i386_pentium4"); (err == nil) != (cgo == "0") {
			t.Fatalf("unexpected validation for cgo=%s: %v", cgo, err)
		}
	}
}
