package kernel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func serveReleases(t *testing.T, files map[string][]byte) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Fatal("download forwarded credentials")
		}
		body, ok := files[req.URL.String()]
		if !ok {
			t.Errorf("unexpected release request to %s", req.URL)
			return nil, errors.New("unexpected URL")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	t.Cleanup(func() { httpClient = old })
}

type testMember struct {
	name string
	data []byte
}

func releaseArchive(t *testing.T, members ...testMember) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	w := tar.NewWriter(z)
	for _, m := range members {
		if err := w.WriteHeader(&tar.Header{Name: m.name, Size: int64(len(m.data)), Mode: 0755, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMirroredOfficialReleaseAndOfflineRecovery(t *testing.T) {
	f := newFixture(t)
	m := f.mgr
	m.Key = ""
	const tag = "v2.3.4-beta.1"
	const target = "amd64"
	const mirror = "https://mirror.example/releases"
	ctx := context.Background()
	name := assetName(tag, target)
	raw := mihomoArchive(t, tag, target)
	asset := releaseAsset{Name: name, Size: int64(len(raw)), Digest: "sha256:" + Digest(raw)}
	r := githubRelease{Tag: tag, Prerelease: true, Assets: []releaseAsset{asset}}
	meta, _ := json.Marshal(r)
	list, _ := json.Marshal([]githubRelease{r})
	files := map[string][]byte{
		releaseAPI + "/tags/" + tag: meta, releaseAPI + "?per_page=100&page=1": list,
		mirror + "/" + tag + "/" + name: raw,
	}
	serveReleases(t, files)
	versions, err := Versions(ctx, target)
	if err != nil || len(versions) != 1 || !versions[0].Installable || versions[0].SignedMaterials || versions[0].Asset != name {
		t.Fatal(versions, err)
	}
	mips, err := Versions(ctx, "mipsle-softfloat")
	if err != nil || len(mips) != 1 || mips[0].Installable {
		t.Fatal(mips, err)
	}

	// A mirror's self-consistent file and checksum must never become trusted.
	copyRaw := append([]byte(nil), raw...)
	copyRaw[len(copyRaw)-1] ^= 1
	files[mirror+"/"+tag+"/"+name] = copyRaw
	if err = m.Install(ctx, tag, "tmp", Source{Mirror: mirror}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal("tampered mirror accepted", err)
	}
	if s, err := m.Status(); err != nil || s.Selected != "" {
		t.Fatal(s, err)
	}

	files[mirror+"/"+tag+"/"+name] = raw
	cache := filepath.Join(f.tmp, "cache")
	if err = m.Install(ctx, tag, "tmp", Source{Mirror: mirror, Cache: cache}); err != nil {
		t.Fatal(err)
	}
	caps, err := m.Capabilities()
	if err != nil || caps.Targets[target].Control != Controller || !caps.Targets[target].TUN || caps.Build != Digest(raw) {
		t.Fatal(caps, err)
	}
	l, err := m.BeginAuthorized(ctx, f.cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Confirm(ctx, l.Token); err != nil {
		t.Fatal(err)
	}
	clear(files)
	state, err := m.read()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(m.slot(*state.Active)); err != nil {
		t.Fatal(err)
	}
	restored, err := m.Recover(ctx)
	if err != nil || restored.Tag != tag {
		t.Fatal(restored, err)
	}
	// An already trusted release remains reinstallable offline from the
	// original archive alone. No mirror-supplied receipt is consumed.
	uploaded := Source{Bundle: filepath.Join(cache, tag)}
	if err = os.WriteFile(filepath.Join(uploaded.Bundle, "receipt.json"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Install(ctx, tag, "double", uploaded); err != nil {
		t.Fatal(err)
	}
}

func TestUnsignedReleaseChecksumSourceAndUnsafeMembers(t *testing.T) {
	const tag = "v1.23.0"
	target := "amd64"
	name := assetName(tag, target)
	raw := releaseArchive(t, testMember{"../mihomo", []byte("invalid")})
	r := githubRelease{Tag: tag, Assets: []releaseAsset{{Name: name, Size: int64(len(raw))}, {Name: "sha256sums", Size: 160}}}
	meta, _ := json.Marshal(r)
	ctx := context.Background()
	sumsURL := origin + "/" + tag + "/sha256sums"
	line := []byte(Digest(raw) + " *" + name + "\n")
	files := map[string][]byte{releaseAPI + "/tags/" + tag: meta, sumsURL: line}
	serveReleases(t, files)
	if p, err := originalPin(ctx, tag, target); err != nil || p.Hash != Digest(raw) {
		t.Fatal(p, err)
	}
	files[sumsURL] = append(append([]byte{}, line...), line...)
	if _, err := originalPin(ctx, tag, target); err == nil {
		t.Fatal("duplicate checksum accepted")
	}
	files[sumsURL] = line
	f := newFixture(t)
	f.mgr.Key = ""
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.mgr.Install(ctx, tag, "double", Source{Bundle: dir}); err == nil || !strings.Contains(err.Error(), "archive path") {
		t.Fatal("unsafe archive accepted", err)
	}
	put := func(file string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("receipt.json", []byte(`{"schema_version":1}`))
	put("sha256sums", line)
	files[sumsURL] = []byte("no upstream checksum for this file")
	if err := f.mgr.Install(ctx, tag, "double", Source{Bundle: dir}); err == nil || !strings.Contains(err.Error(), "upstream checksum") {
		t.Fatal("upload supplied its own trust", err)
	}
}

func mihomoArchive(t *testing.T, tag, target string) []byte {
	base := strings.TrimSuffix(assetName(tag, target), ".tar.gz") + "/"
	return releaseArchive(t, testMember{base + "mihomo", tinyCore(t)}, testMember{base + "capabilities.json", []byte(`{"control_api":"mihomo-rest-v1","tun":true,"tproxy":true,"naive":false}`)})
}

// An optional locally published ARM64 artifact is checked against a digest supplied independently of the archive.
func TestPublishedARM64Archive(t *testing.T) {
	file := os.Getenv("HYPER_RELEASE_TEST_ARCHIVE")
	if file == "" {
		t.Skip("set HYPER_RELEASE_TEST_ARCHIVE to the Mihomo ARM64 release archive")
	}
	tag, digest := os.Getenv("HYPER_RELEASE_TEST_TAG"), os.Getenv("HYPER_RELEASE_TEST_SHA256")
	name := filepath.Base(file)
	if !TagPattern.MatchString(tag) || !validHash(digest) || (name != assetName(tag, "arm64") && name != rawAsset(tag, "arm64")) {
		t.Fatal("invalid release qualification inputs")
	}
	st, e := os.Stat(file)
	if e != nil {
		t.Fatal(e)
	}
	a := releaseAsset{Name: name, Size: st.Size(), Digest: "sha256:" + digest}
	r, _ := json.Marshal(githubRelease{Tag: releaseRef(tag), Prerelease: releaseRef(tag) == rolling, Assets: []releaseAsset{a}})
	serveReleases(t, map[string][]byte{releaseAPI + "/tags/" + releaseRef(tag): r})
	m := newFixture(t).mgr
	m.Target = "arm64"
	m.Key = ""
	if e = m.Install(context.Background(), tag, "double", Source{Bundle: filepath.Dir(file)}); e != nil {
		t.Fatal(e)
	}
	caps, e := m.Capabilities()
	if e != nil || caps.Targets["arm64"].Control != Controller {
		t.Fatal(caps, e)
	}
}

func TestRollingAssetIdentityCannotFollowAReplacedBinary(t *testing.T) {
	const tag = "v0.0.0-alpha.123abcd"
	asset := releaseAsset{Name: rawAsset(tag, "amd64"), Size: 1234, Digest: "sha256:" + strings.Repeat("a", 64)}
	release := githubRelease{Tag: rolling, Prerelease: true, Assets: []releaseAsset{asset}}
	if v := release.version("amd64"); v.Tag != tag || v.Asset != asset.Name || !v.Prerelease || !v.Installable {
		t.Fatal(v)
	}
	release.Assets[0].Name = rawAsset("v0.0.0-alpha.8765432", "amd64")
	raw, _ := json.Marshal(release)
	serveReleases(t, map[string][]byte{releaseAPI + "/tags/" + rolling: raw})
	if _, err := originalPin(context.Background(), tag, "amd64"); err == nil {
		t.Fatal("moving alpha silently replaced selected commit")
	}
}

// Validate all locally built binaries without executing foreign instruction sets.
func TestStaticReleaseMatrix(t *testing.T) {
	dir := os.Getenv("HYPER_MIHOMO_MATRIX")
	if dir == "" {
		t.Skip("set HYPER_MIHOMO_MATRIX to the build output")
	}
	const tag = "v1.19.34-0.20261117082241-15fb245e9278"
	for target := range platforms {
		t.Run(target, func(t *testing.T) {
			name := assetName(tag, target)
			path := filepath.Join(dir, name)
			f, e := os.Open(path)
			if e != nil {
				t.Fatal(e)
			}
			hash := sha256.New()
			n, e := io.Copy(hash, f)
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
			_, v, e := inspectArchive(context.Background(), path, tag, target, pin{Name: name, Size: n, Hash: hex.EncodeToString(hash.Sum(nil))})
			if e != nil {
				t.Fatal(e)
			}
			if e = unpack(path, t.TempDir(), target, target, v.Asset); e != nil {
				t.Fatal(e)
			}
		})
	}
}

type rejectedProbe struct{}

func (rejectedProbe) Error() string               { return "rejected test profile" }
func (rejectedProbe) RejectedConfiguration() bool { return true }

func TestProbeDoesNotAdvertiseIgnoredRealityOrUnknownProtocols(t *testing.T) {
	m := newFixture(t).mgr
	if err := m.setup(); err != nil {
		t.Fatal(err)
	}
	m.Inspect = func(_ context.Context, _ Launch, path string) error {
		config, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(config), "type: nowhere") {
			return rejectedProbe{}
		}
		if strings.Contains(string(config), "type: snell") {
			var profile struct {
				Proxies []struct {
					PSK string `yaml:"psk"`
				}
			}
			if err := yaml.Unmarshal(config, &profile); err != nil {
				return err
			}
			if len(profile.Proxies) != 1 || len(profile.Proxies[0].PSK) < 12 || len(profile.Proxies[0].PSK) > 255 {
				return rejectedProbe{}
			}
		}
		// Simulate a parser that accepts AnyTLS yet silently ignores reality-opts.
		return nil
	}
	caps, err := m.probeFeatures(context.Background(), Launch{}, TargetCapabilities{})
	if err != nil || caps.ProtocolSupport["anytls-reality"] || caps.ProtocolSupport["nowhere"] || !caps.ProtocolSupport["snell-v6"] || caps.Naive {
		t.Fatal(caps, err)
	}
	m.Inspect = func(context.Context, Launch, string) error { return context.Canceled }
	if _, err = m.probeFeatures(context.Background(), Launch{}, TargetCapabilities{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was recorded as missing protocol support", err)
	}
}

// The repository's own releases are tagged <line>-vX.Y.Z beside mihomo's v* tags; the router
// knows them by version, and the core the 1.0.0 package carried by its version-named build.
func TestReleaseLineVersions(t *testing.T) {
	if _, ok := stableTag("v1.0.1"); ok {
		t.Error("a stable release without a line")
	}
	withLine(t)
	for tag, want := range map[string]string{"hyper-v0.1.0": "v0.1.0", "hyper-v1.20.3": "v1.20.3", "v1.19.34": "", "hyper-v01.0.0": "", "hyper-v0.1.0-rc.1": "", "Prerelease-Alpha": "", "other-v1.0.0": ""} {
		if got, ok := stableTag(tag); got != want && ok || ok != (want != "") {
			t.Errorf("stableTag(%q) = %q, %v", tag, got, ok)
		}
	}
	if releaseRef("v0.1.0") != "hyper-v0.1.0" || releaseRef("v0.0.0-alpha.123abcd") != rolling {
		t.Error("releaseRef", releaseRef("v0.1.0"))
	}
	if got := rawAsset("v0.1.0", "amd64"); got != "mihomo-linux-amd64-v1-hyper-v0.1.0.gz" {
		t.Errorf("rawAsset = %q", got)
	}
	if !rawBuild("mihomo-linux-amd64-v1-v0.1.0.gz", "v0.1.0", "amd64") || !rawBuild(rawAsset("v0.1.0", "amd64"), "v0.1.0", "amd64") || rawBuild("mihomo-linux-amd64-v1-v0.1.1.gz", "v0.1.0", "amd64") {
		t.Error("rawBuild")
	}
	for tag, want := range map[string]string{"hyper-v1.0.1": "v1.0.1", "v1.19.34": "", "other-v1.0.1": ""} {
		if got := (githubRelease{Tag: tag}).identity("amd64"); got != want {
			t.Errorf("identity(%q) = %q", tag, got)
		}
	}
	if !TagPattern.MatchString("v1.19.34") || TagPattern.MatchString("hyper-1.0.0") {
		t.Error("TagPattern")
	}
}

// A core the 1.0.0 package installed (no repository, named by its version) stays valid.
func TestPackagedCoreReceiptStaysValid(t *testing.T) {
	withLine(t)
	plat := platforms["amd64"]
	core := Member{Path: strings.TrimSuffix(assetName("v1.0.1", "amd64"), ".tar.gz") + "/mihomo", Kind: "core", Size: 100, SHA256: strings.Repeat("b", 64)}
	a := Asset{Name: "mihomo-linux-amd64-v1-v1.0.1.gz", Arch: plat.arch, Endian: plat.endian, FloatABI: plat.float, Size: 50, Expanded: 100,
		SHA256: strings.Repeat("a", 64), Files: []Member{core}}
	caps := TargetCapabilities{Control: Controller, TUN: true, TProxy: true}
	if _, err := (receipt{1, "", "v1.0.1", "amd64", a, caps}).verified("v1.0.1", "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := (receipt{1, "someone/else", "v1.0.1", "amd64", a, caps}).verified("v1.0.1", "amd64"); err == nil {
		t.Fatal("a core of another repository")
	}
}

// A core the 1.0.0 package installed in RAM comes back after a reboot from its line's release,
// which names the same build after itself.
func TestPackagedCoreRestoresFromItsRelease(t *testing.T) {
	withLine(t)
	f := newFixture(t)
	m := f.mgr
	m.Key = ""
	const tag, target = "v1.0.1", "amd64"
	ctx := context.Background()
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write(tinyCore(t))
	z.Close()
	raw := gz.Bytes()
	name := rawAsset(tag, target)
	meta, _ := json.Marshal(githubRelease{Tag: "hyper-" + tag, Assets: []releaseAsset{{Name: name, Size: int64(len(raw)), Digest: "sha256:" + Digest(raw)}}})
	archive := origin + "/hyper-" + tag + "/" + name
	files := map[string][]byte{releaseAPI + "/tags/hyper-" + tag: meta, archive: raw}
	serveReleases(t, files)
	if err := m.Install(ctx, tag, "tmp", Source{}); err != nil {
		t.Fatal(err)
	}
	l, err := m.BeginAuthorized(ctx, f.cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Confirm(ctx, l.Token); err != nil {
		t.Fatal(err)
	}
	// As 1.0.0 recorded it: no repository, the build named by its version.
	file := filepath.Join(m.Root, "meta", tag, "receipt.json")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var r receipt
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	r.Repository, r.Asset.Name = "", "mihomo-linux-amd64-v1-"+tag+".gz"
	if b, err = json.Marshal(r); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := m.read()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(m.slot(*state.Active)); err != nil {
		t.Fatal(err)
	}
	delete(files, releaseAPI+"/tags/hyper-"+tag)
	restored, err := m.Recover(ctx)
	if err != nil || restored.Tag != tag {
		t.Fatal(restored, err)
	}
}
