// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.2.0", "0.2.0-r1", 0}, {"0.10.0", "0.9.9", 1}, {"v1.0.0", "1.0.0", 0}, {"1.2", "1.2.0", 0}, {"1.1", "1.0.9", 1},
		{"0.2.0-beta.1", "0.2.0", -1}, {"0.2.0-beta.2", "0.2.0-beta.10", -1}, {"0.2.0-1", "0.2.0-beta", -1}, {"0.2.0-beta", "0.2.0-beta.1", -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: %d", c.a, c.b, got)
		}
	}
}

func TestPick(t *testing.T) {
	assets := []Asset{{Name: "sha256sums"}, {Name: "23.05-24.10_luci-app-hyper_1.0.0_aarch64_cortex-a53.ipk"},
		{Name: "25.12+_luci-app-hyper-1.0.0-aarch64_cortex-a53.apk"}, {Name: "23.05-24.10_luci-app-hyper_1.0.0_x86_64.ipk"}}
	pkg, sums := Pick(assets, PackageManager{"opkg", "23.05-24.10", ".ipk", "x86_64"})
	if pkg == nil || pkg.Name != assets[3].Name || sums == nil {
		t.Fatal(pkg, sums)
	}
	pkg, _ = Pick(assets, PackageManager{"apk", "25.12+", ".apk", "aarch64_cortex-a53"})
	if pkg == nil || pkg.Name != assets[2].Name {
		t.Fatal(pkg)
	}
	if pkg, _ = Pick(assets, PackageManager{"opkg", "23.05-24.10", ".ipk", ""}); pkg != nil {
		t.Fatal("matched empty arch")
	}
}

func TestManager(t *testing.T) {
	u := &Updater{Exists: func(string) bool { return false }, Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte("arch all 1\narch noarch 1\narch aarch64_generic 10\narch aarch64_cortex-a53 20\n"), nil
	}}
	if pm := u.Manager(context.Background()); pm.Name != "opkg" || pm.Arch != "aarch64_cortex-a53" {
		t.Fatal(pm)
	}
}

func TestPrepare(t *testing.T) {
	pkgData := []byte("package bytes")
	sum := sha256.Sum256(pkgData)
	files := map[string]string{
		"https://x/sums": hex.EncodeToString(sum[:]) + "  23.05-24.10_luci-app-hyper_1.1.0_x86_64.ipk\n",
		"https://x/pkg":  string(pkgData),
	}
	u := &Updater{Dir: t.TempDir(), Get: func(_ context.Context, url string, _ http.Header) (*http.Response, error) {
		body, ok := files[url]
		if !ok {
			return nil, errors.New("unreachable")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	rel := &Release{Version: "1.1.0", Assets: []Asset{{Name: "sha256sums", URL: "https://x/sums"}, {Name: "23.05-24.10_luci-app-hyper_1.1.0_x86_64.ipk", URL: "https://x/pkg"}}}
	pm := PackageManager{"opkg", "23.05-24.10", ".ipk", "x86_64"}
	job, err := u.Prepare(context.Background(), rel, pm, false)
	if err != nil || len(job.Files) != 1 {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(job.Files[0]); string(b) != string(pkgData) {
		t.Fatal("download")
	}
	files["https://x/pkg"] = "tampered"
	if _, err = u.Prepare(context.Background(), rel, pm, false); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal(err)
	}
	// Install refuses files outside the download dir and honours the auto-update switch.
	st := Install(context.Background(), u.Dir, Job{Manager: "opkg", Files: []string{"/etc/passwd"}, Version: "1"}, nil)
	if st.State != "failed" || st.Error != "update_failed" {
		t.Fatal(st)
	}
	st = Install(context.Background(), u.Dir, Job{Manager: "opkg", Files: []string{filepath.Join(u.Dir, "download", "a.ipk")}, Automatic: true},
		func() bool { return false })
	if st.Error != "automatic_update_disabled" {
		t.Fatal(st)
	}
	if err := WriteState(u.Dir, st); err != nil || ReadState(u.Dir).Error != st.Error {
		t.Fatal(err)
	}
}
