package kernel

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

func stableFixture(tag string, assets []releaseAsset) map[string]any {
	return map[string]any{"tag_name": tag, "html_url": "https://github.com/" + Repository + "/releases/tag/" + tag,
		"draft": false, "prerelease": false, "assets": assets}
}

func TestBootstrapStagesVerifiedLatestWithoutActivation(t *testing.T) {
	withLine(t)
	m := newFixture(t).mgr
	// The line's release hyper-v2.4.1 is the router's v2.4.1.
	const release, tag = "hyper-v2.4.1", "v2.4.1"
	name := assetName(tag, m.Target)
	raw := mihomoArchive(t, tag, m.Target)
	asset := releaseAsset{Name: name, Size: int64(len(raw)), Digest: "sha256:" + Digest(raw)}
	meta, _ := json.Marshal(stableFixture(release, []releaseAsset{asset}))
	archive := origin + "/" + release + "/" + name
	files := map[string][]byte{releaseAPI + "/latest": meta, archive: raw}
	serveReleases(t, files)
	ctx := context.Background()
	files[archive] = bytes.Repeat([]byte{0}, len(raw))
	if _, err := m.Bootstrap(ctx); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("invalid bytes accepted: %v", err)
	}
	files[archive] = raw
	out, err := m.Bootstrap(ctx)
	if err != nil || out["staged"] != tag {
		t.Fatal(out, err)
	}
	st, err := m.Status()
	if err != nil || st.Selected != "" || st.Active != "" || st.Staged != tag || st.Pending {
		t.Fatal(st, err)
	}
	clear(files)
	out, err = m.Bootstrap(ctx)
	if err != nil || out["skipped"] != "existing_kernel" {
		t.Fatal(out, err)
	}
	if err = m.Select(tag, false, st.Revision); err != nil {
		t.Fatal(err)
	}
	out, err = m.Bootstrap(ctx)
	if err != nil || out["skipped"] != "existing_kernel" {
		t.Fatal(out, err)
	}
}

func TestBootstrapDeclinesInvalidLatestWithoutDowngrade(t *testing.T) {
	withLine(t)
	const release, tag = "hyper-v2.4.1", "v2.4.1"
	data := stableFixture(release, []releaseAsset{{Name: assetName(tag, "amd64"), Size: 768, Digest: "sha256:" + strings.Repeat("a", 64)}})
	meta, _ := json.Marshal(data)
	serveReleases(t, map[string][]byte{releaseAPI + "/latest": meta})
	if got, _, err := latestStable(context.Background(), "amd64"); err != nil || got != tag {
		t.Fatal("the fixture is a valid release", got, err)
	}
	asset := releaseAsset{Name: assetName(tag, "amd64"), Size: 768, Digest: "sha256:" + strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"draft", func(r map[string]any) { r["draft"] = true }},
		{"prerelease", func(r map[string]any) { r["prerelease"] = true }},
		{"missing stability", func(r map[string]any) { delete(r, "prerelease") }},
		{"tag is a preview", func(r map[string]any) {
			r["tag_name"] = "hyper-v2.4.1-rc.1"
			r["html_url"] = "https://github.com/" + Repository + "/releases/tag/hyper-v2.4.1-rc.1"
		}},
		{"invalid semver", func(r map[string]any) {
			r["tag_name"] = "hyper-v02.4.1"
			r["html_url"] = "https://github.com/" + Repository + "/releases/tag/hyper-v02.4.1"
		}},
		{"another series", func(r map[string]any) {
			r["tag_name"] = tag
			r["html_url"] = "https://github.com/" + Repository + "/releases/tag/" + tag
		}},
		{"unrelated release URL", func(r map[string]any) { r["html_url"] = "https://example.org/releases/tag/" + tag }},
		{"missing arch", func(r map[string]any) {
			other := asset
			other.Name = assetName(tag, "arm64")
			r["assets"] = []releaseAsset{other}
		}},
		{"ambiguous arch", func(r map[string]any) { r["assets"] = []releaseAsset{asset, asset} }},
		{"unverified", func(r map[string]any) { a := asset; a.Digest = ""; r["assets"] = []releaseAsset{a} }},
		{"invalid upstream digest", func(r map[string]any) { a := asset; a.Digest = "sha256:truncated"; r["assets"] = []releaseAsset{a} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := stableFixture(release, []releaseAsset{asset})
			tc.edit(data)
			meta, _ := json.Marshal(data)
			serveReleases(t, map[string][]byte{releaseAPI + "/latest": meta})
			if _, _, err := latestStable(context.Background(), "amd64"); err == nil || errors.Is(err, ErrTemporaryDownload) {
				t.Fatalf("expected a permanent rejection: %v", err)
			}
		})
	}
}

func TestBootstrapLeavesResidualPayloadAndBusyInstallAlone(t *testing.T) {
	serveReleases(t, nil)
	for _, tc := range []struct {
		name     string
		resident func(*Manager) string
	}{
		{"persistent slot", func(m *Manager) string { return filepath.Join(m.Root, "slots") }},
		{"volatile slot", func(m *Manager) string { return filepath.Join(m.Temp, "slots") }},
		{"cached metadata", func(m *Manager) string { return filepath.Join(m.Root, "meta") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFixture(t).mgr
			if err := m.setup(); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(tc.resident(m), "existing-payload")
			if err := os.WriteFile(p, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := m.Bootstrap(context.Background())
			if err != nil || out["skipped"] != "existing_kernel" {
				t.Fatal(out, err)
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != "retained" {
				t.Fatal(string(b), err)
			}
		})
	}
	m := newFixture(t).mgr
	if err := m.setup(); err != nil {
		t.Fatal(err)
	}
	lock, err := m.reservePayload()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	out, err := m.Bootstrap(context.Background())
	if err != nil || out["skipped"] != "installation_in_progress" {
		t.Fatal(out, err)
	}
}

func TestLatestRetryOnlyForTemporaryHTTP(t *testing.T) {
	old := httpClient
	defer func() { httpClient = old }()
	for _, status := range []int{404, 408, 429, 500, 503} {
		httpClient = &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		_, _, err := latestStable(context.Background(), "amd64")
		if err == nil || errors.Is(err, ErrTemporaryDownload) != (status != 404) {
			t.Fatal(status, err)
		}
	}
}

func TestLatestRecognizesOnlyGitHubReportedQuota(t *testing.T) {
	old := httpClient
	defer func() { httpClient = old }()
	for _, tc := range []struct {
		status          int
		remaining, host string
		quota           bool
	}{
		{403, "0", "api.github.com", true}, {429, "0", "api.github.com", true},
		{403, "", "api.github.com", false}, {403, "10", "api.github.com", false},
		{401, "0", "api.github.com", false}, {403, "0", "mirror.example", false},
	} {
		httpClient = &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
			remote := req.Clone(req.Context())
			remote.URL.Host = tc.host
			headers := make(http.Header)
			headers.Set("X-RateLimit-Remaining", tc.remaining)
			return &http.Response{Request: remote, StatusCode: tc.status, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		_, _, err := latestStable(context.Background(), "arm64")
		if errors.Is(err, ErrReleaseRateLimit) != tc.quota || errors.Is(err, ErrTemporaryDownload) {
			t.Fatal(tc, err)
		}
	}
}

func TestBootstrapCancellationReleasesManualInstaller(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.mgr
	old := httpClient
	defer func() { httpClient = old }()
	started := make(chan struct{})
	httpClient = &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Bootstrap(ctx); done <- err }()
	// Bootstrap can fail before it reaches the network (storage setup, an existing kernel),
	// and then the request never starts; waiting on started alone would hang until go test
	// times out.
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Bootstrap returned before requesting the latest release: %v", err)
	}
	if err := m.Install(context.Background(), "v3.0.0", "double", Source{}); !errors.Is(err, errPayloadBusy) {
		t.Errorf("manual operation raced active payload writer: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	src, _ := fixture.bundle(t, "v3.0.0", "fixture")
	if err := m.Install(context.Background(), "v3.0.0", "double", src); err != nil {
		t.Fatal("manual selection lost", err)
	}
	state, err := m.Status()
	if err != nil || state.Selected != "v3.0.0" || state.Active != "" || state.Staged != "" {
		t.Fatal(state, err)
	}
}

func TestBootstrapLatestPreservesCertificateValidityFailure(t *testing.T) {
	now := time.Now().UTC()
	cert := &x509.Certificate{NotBefore: now.Add(-2 * time.Hour), NotAfter: now.Add(-time.Hour)}
	old := httpClient
	t.Cleanup(func() { httpClient = old })
	httpClient = &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
		return nil, x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}
	})}
	_, _, err := latestStable(t.Context(), "amd64")
	failure := tlsdiag.From(err)
	if failure == nil || failure.Reason != "expired" || !failure.Recheck(cert.NotBefore) || failure.Recheck(now) || errors.Is(err, ErrTemporaryDownload) {
		t.Fatal(err)
	}
}

// A later attempt clears what an interrupted one left (the daemon restarted mid-download, as a
// package upgrade does) and installs; the first one leaves it alone, and kernel state ends both.
func TestBootstrapAgainClearsAnInterruptedAttempt(t *testing.T) {
	withLine(t)
	m := newFixture(t).mgr
	const release, tag = "hyper-v2.4.1", "v2.4.1"
	name := assetName(tag, m.Target)
	raw := mihomoArchive(t, tag, m.Target)
	meta, _ := json.Marshal(stableFixture(release, []releaseAsset{{Name: name, Size: int64(len(raw)), Digest: "sha256:" + Digest(raw)}}))
	serveReleases(t, map[string][]byte{releaseAPI + "/latest": meta, origin + "/" + release + "/" + name: raw})
	if err := m.setup(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(m.Root, "slots", ".unpack-1"), filepath.Join(m.Root, "meta", "v2.4.0"), filepath.Join(m.Temp, "slots", ".unpack-2")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(m.Temp, ".archive-1"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if out, err := m.Bootstrap(ctx); err != nil || out["skipped"] != "existing_kernel" {
		t.Fatal(out, err)
	}
	out, err := m.BootstrapAgain(ctx)
	if err != nil || out["staged"] != tag {
		t.Fatal(out, err)
	}
	for _, p := range []string{filepath.Join(m.Root, "slots", ".unpack-1"), filepath.Join(m.Temp, "slots", ".unpack-2"), filepath.Join(m.Temp, ".archive-1")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("left", p, err)
		}
	}
	if out, err = m.BootstrapAgain(ctx); err != nil || out["skipped"] != "existing_kernel" {
		t.Fatal(out, err)
	}
}
