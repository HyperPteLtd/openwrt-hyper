package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const version = "1.0.0"

type fixture struct{ root, feeds, output string }

func writeSums(t *testing.T, feed string) {
	t.Helper()
	entries, _ := os.ReadDir(feed)
	var b strings.Builder
	for _, e := range entries {
		if e.Name() == "SHA256SUMS" {
			continue
		}
		d, err := Digest(filepath.Join(feed, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(d + "  " + e.Name() + "\n")
	}
	if err := os.WriteFile(filepath.Join(feed, "SHA256SUMS"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) fixture { return newFixtureFor(t, version) }

func newFixtureFor(t *testing.T, version string) fixture {
	root := t.TempDir()
	f := fixture{root, filepath.Join(root, "feeds"), filepath.Join(root, "assets")}
	for _, format := range Formats {
		feed := filepath.Join(f.feeds, "release-installer-"+format)
		if err := os.MkdirAll(feed, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range FormatNames(version, format) {
			if err := os.WriteFile(filepath.Join(feed, name), []byte(name+" fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		writeSums(t, feed)
	}
	return f
}

func (f fixture) feed(format string) string {
	return filepath.Join(f.feeds, "release-installer-"+format)
}

func mustErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want %q", err, want)
	}
}

func TestNamesMatchRuntimeABI(t *testing.T) {
	for _, arch := range AllArches {
		if out, err := exec.Command("../../packaging/openwrt/runtime-abi", arch).Output(); err != nil || len(out) == 0 {
			t.Errorf("runtime-abi does not know %s: %v", arch, err)
		}
	}
	if got := PackageName(version, "ipk", "aarch64_cortex-a53"); got != "luci-app-hyper_1.0.0-1_aarch64_cortex-a53.ipk" {
		t.Fatal(got)
	}
	if got := PackageName(version, "apk", "mipsel_24kc"); got != "luci-app-hyper-1.0.0-r1_mipsel_24kc.apk" {
		t.Fatal(got)
	}
	if n := len(AllNames(version)); n != 28 {
		t.Fatalf("%d installers", n)
	}
}

func TestExactPublicAssetsAndOfflineCommands(t *testing.T) {
	f := newFixture(t)
	if err := Assemble(version, f.feeds, f.output); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(f.output)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := AllNames(version)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("assets %v", got)
	}
	sums, _ := os.ReadFile(filepath.Join(f.root, "SHA256SUMS"))
	var expect strings.Builder
	for _, name := range want {
		d, _ := Digest(filepath.Join(f.output, name))
		expect.WriteString(d + "  " + name + "\n")
	}
	if string(sums) != expect.String() {
		t.Fatalf("SHA256SUMS:\n%s", sums)
	}
	notes, err := RenderNotes("HyperPteLtd/openwrt-hyper", version, f.output, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		if !strings.Contains(notes, "https://github.com/HyperPteLtd/openwrt-hyper/releases/download/v1.0.0/"+name) {
			t.Errorf("notes miss %s", name)
		}
	}
	if _, err := RenderNotes("HyperPteLtd/openwrt-hyper", version, f.output, &Preview{Tag: "v1.0.0-preview.2", Commit: strings.Repeat("a", 40)}); err == nil {
		t.Error("preview notes accepted the stable packages")
	}
	if strings.Contains(notes, string(sums)) {
		t.Error("notes repeat the checksums; they belong in the SHA256SUMS asset")
	}
	for _, s := range []string{"`SHA256SUMS`", "---\n\n## 安装包", "opkg install ./luci-app-hyper_1.0.0-1_<arch>.ipk",
		"apk add --no-network --allow-untrusted ./luci-app-hyper-1.0.0-r1_<架构>.apk", "| `riscv64_riscv64` | [luci-app-hyper_1.0.0-1_riscv64_riscv64.ipk]", "| —"} {
		if !strings.Contains(notes, s) {
			t.Errorf("notes miss %q", s)
		}
	}
}

func TestRepeatAndInterruptedRun(t *testing.T) {
	f := newFixture(t)
	sums := filepath.Join(f.root, "SHA256SUMS")
	if err := Assemble(version, f.feeds, f.output); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(sums)
	if err := Assemble(version, f.feeds, f.output); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.output); err != nil {
		t.Fatal(err)
	}
	if err := Assemble(version, f.feeds, f.output); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(sums); string(again) != string(first) {
		t.Fatal("checksums changed")
	}
	apk := filepath.Join(f.output, PackageName(version, "apk", "arm_cortex-a7_neon-vfpv4"))
	os.WriteFile(apk, []byte("unrelated"), 0o644)
	mustErr(t, Assemble(version, f.feeds, f.output), "existing output differs")
	if data, _ := os.ReadFile(apk); string(data) != "unrelated" {
		t.Fatal("existing output was replaced")
	}
}

func TestPartialOrTamperedInputAbortsBeforeStaging(t *testing.T) {
	f := newFixture(t)
	baseline := filepath.Join(f.feed("apk"), "baseline")
	os.Mkdir(baseline, 0o755)
	mustErr(t, Assemble(version, f.feeds, f.output), "baseline")
	os.Remove(baseline)
	os.WriteFile(filepath.Join(f.feed("ipk"), PackageName(version, "ipk", "mips_24kc")), []byte("different"), 0o644)
	mustErr(t, Assemble(version, f.feeds, f.output), "checksums mismatch")
	os.Remove(filepath.Join(f.feed("ipk"), PackageName(version, "ipk", "x86_64")))
	mustErr(t, Assemble(version, f.feeds, f.output), "missing entries")
	if entries, _ := os.ReadDir(f.root); len(entries) != 1 || entries[0].Name() != "feeds" {
		t.Fatalf("staged output after failure: %v", entries)
	}
}

func TestRejectsSymlinks(t *testing.T) {
	f := newFixture(t)
	apk := filepath.Join(f.feed("apk"), PackageName(version, "apk", "aarch64_generic"))
	os.Remove(apk)
	os.Symlink(filepath.Join(f.feed("ipk"), PackageName(version, "ipk", "aarch64_generic")), apk)
	mustErr(t, Assemble(version, f.feeds, f.output), "file type")
	if _, err := os.Stat(f.output); !os.IsNotExist(err) {
		t.Fatal("output created")
	}
	mustErr(t, Assemble("1.0", f.feeds, f.output), "release version")
}

func TestFakeLatest(t *testing.T) {
	dir := t.TempDir()
	name := PackageName(version, "ipk", "aarch64_generic")
	os.WriteFile(filepath.Join(dir, name), []byte("pkg"), 0o644)
	response := filepath.Join(t.TempDir(), "latest")
	asset, err := FakeLatest(dir, response, "", "")
	if err != nil || asset != "v1.0.0/"+name {
		t.Fatal(asset, err)
	}
	data, _ := os.ReadFile(response)
	if !strings.Contains(string(data), `"tag_name":"v1.0.0"`) || strings.Count(string(data), `"name"`) != 29 || !strings.Contains(string(data), `"name":"SHA256SUMS"`) {
		t.Fatalf("%s", data)
	}
	os.Rename(filepath.Join(dir, name), filepath.Join(dir, "luci-app-hyper_1.0.0-1_all.ipk"))
	mustErr(t, func() error { _, err := FakeLatest(dir, response, "", ""); return err }(), "unexpected Release package name")
}

func TestMatrix(t *testing.T) {
	if n := len(Matrix("")); n != 13+14+14 {
		t.Fatal(n)
	}
	for _, j := range Matrix("apk") {
		if j.Firmware != "25.12.6" {
			t.Fatal(j)
		}
	}
}

func TestPreviewPackagesCarryTheirNumber(t *testing.T) {
	pkg := RevisionVersion(version, 2)
	if pkg != "1.0.0-r2" || !ValidPackageVersion(pkg) || ValidVersion(pkg) || ValidPackageVersion("1.0.0-r0") {
		t.Fatal(pkg)
	}
	if got := PackageName(pkg, "apk", "mipsel_24kc"); got != "luci-app-hyper-1.0.0-r2_mipsel_24kc.apk" {
		t.Fatal(got)
	}
	if got := PackageName(pkg, "ipk", "mipsel_24kc"); got != "luci-app-hyper_1.0.0-2_mipsel_24kc.ipk" {
		t.Fatal(got)
	}
	f := newFixtureFor(t, pkg)
	if err := Assemble(pkg, f.feeds, f.output); err != nil {
		t.Fatal(err)
	}
	notes, err := RenderNotes("HyperPteLtd/openwrt-hyper", version, f.output, &Preview{Tag: "v1.0.0-preview.2", Commit: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Preview of 1.0.0-preview.2, built from aaaaaaa", "1.0.0-preview.2 预览版", "version 1.0.0-r2",
		"https://github.com/HyperPteLtd/openwrt-hyper/releases/download/v1.0.0-preview.2/luci-app-hyper_1.0.0-2_", "blob/v1.0.0-preview.2/packaging/openwrt/README.md"} {
		if !strings.Contains(notes, s) {
			t.Errorf("preview notes miss %q", s)
		}
	}
	for _, tag := range []string{"v2.0.0-preview.1", "v1.0.0-preview.3", "v1.0.0-preview.02"} {
		if _, err := RenderNotes("HyperPteLtd/openwrt-hyper", version, f.output, &Preview{Tag: tag, Commit: strings.Repeat("a", 40)}); err == nil {
			t.Errorf("accepted %s", tag)
		}
	}
}

func TestBumpFollowsConventionalCommits(t *testing.T) {
	for _, c := range []struct {
		msgs []string
		want string
	}{
		{nil, "1.2.3"},
		{[]string{"fix(ui): align"}, "1.2.4"},
		{[]string{"docs: x", "feat(router): y"}, "1.3.0"},
		{[]string{"feat!: drop fw3"}, "2.0.0"},
		{[]string{"fix: z\n\nBREAKING CHANGE: new config"}, "2.0.0"},
	} {
		if got, err := Bump("1.2.3", c.msgs); err != nil || got != c.want {
			t.Errorf("%v: got %s %v, want %s", c.msgs, got, err, c.want)
		}
	}
}

// A release takes the revision after its last preview, as in luci-app-hyper-1.2.0-r2_x86_64.apk.
func TestFakeLatestRevision(t *testing.T) {
	dir := t.TempDir()
	name := PackageName("1.2.0-r2", "apk", "x86_64")
	if name != "luci-app-hyper-1.2.0-r2_x86_64.apk" {
		t.Fatal(name)
	}
	os.WriteFile(filepath.Join(dir, name), []byte("pkg"), 0o644)
	response := filepath.Join(t.TempDir(), "latest")
	asset, err := FakeLatest(dir, response, "", "")
	if err != nil || asset != "v1.2.0/"+name {
		t.Fatal(asset, err)
	}
	data, _ := os.ReadFile(response)
	if !strings.Contains(string(data), `"name":"luci-app-hyper_1.2.0-2_x86_64.ipk"`) {
		t.Fatalf("%s", data)
	}
}
