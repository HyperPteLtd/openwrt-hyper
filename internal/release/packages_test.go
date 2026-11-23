package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The names are those 0.x's updater looks for (family prefix, then name, version and arch).
func TestPackageNames(t *testing.T) {
	if got := PackageName("1.0.0", "ipk", "x86_64"); got != "23.05-24.10_luci-app-hyper_1.0.0_x86_64.ipk" {
		t.Fatal(got)
	}
	if got := PackageName("1.0.0", "apk", "aarch64_cortex-a53"); got != "25.12+_luci-app-hyper-1.0.0-aarch64_cortex-a53.apk" {
		t.Fatal(got)
	}
	for _, format := range Formats {
		for _, arch := range Arches[format] {
			name := PackageName("1.2.3", format, arch)
			v, f, a, ok := ParsePackageName(name)
			if !ok || v != "1.2.3" || f != format || a != arch {
				t.Fatal(name, v, f, a, ok)
			}
		}
	}
	for _, bad := range []string{
		"23.05-24.10_luci-app-hyper_1.0.0_riscv64_generic.ipk", // 25.12's RISC-V name
		"25.12+_luci-app-hyper-1.0.0-riscv64_riscv64.apk",
		"23.05-24.10_luci-app-hyper-1.0.0-x86_64.ipk",
		"25.12+_luci-app-hyper_1.0.0_x86_64.apk",
		"23.05-24.10_luci-app-hyper_1.0_x86_64.ipk",
		"luci-app-hyper_1.0.0-1_x86_64.ipk",
	} {
		if _, _, _, ok := ParsePackageName(bad); ok {
			t.Error("accepted", bad)
		}
	}
	if n := len(ReleaseNames("1.0.0")); n != len(Arches["ipk"])+len(Arches["apk"])+1 {
		t.Fatal(n)
	}
	if got := AssetURL("o/r", "v1.0.0", "25.12+_x.apk"); got != "https://github.com/o/r/releases/download/v1.0.0/25.12%2B_x.apk" {
		t.Fatal(got)
	}
}

func TestFakeLatest(t *testing.T) {
	dir := t.TempDir()
	name := PackageName("1.0.0", "apk", "x86_64")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(invalid string) (resp struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name     string  `json:"name"`
			Digest   *string `json:"digest"`
			Download string  `json:"browser_download_url"`
		} `json:"assets"`
	}, url string) {
		out := filepath.Join(t.TempDir(), "latest.json")
		got, err := FakeLatest(dir, out, invalid, "")
		if err != nil || got != "v1.0.0/"+name {
			t.Fatal(got, err)
		}
		b, _ := os.ReadFile(out)
		json.Unmarshal(b, &resp)
		for _, a := range resp.Assets {
			if a.Name == name {
				url = a.Download
			}
		}
		return
	}
	resp, url := read("")
	if resp.TagName != "v1.0.0" || len(resp.Assets) != len(ReleaseNames("1.0.0")) || url != AssetURL("HyperPteLtd/openwrt-hyper", "v1.0.0", name) {
		t.Fatal(resp.TagName, len(resp.Assets), url)
	}
	if _, url = read("url"); !strings.Contains(url, "/openwrt-other/") {
		t.Fatal("the url fixture serves the official address:", url)
	}
	if resp, _ = read("origin"); !strings.Contains(resp.HTMLURL, "/openwrt-other/") {
		t.Fatal("the origin fixture serves the official address:", resp.HTMLURL)
	}
}
