// Package release names and checks the per-architecture Hyper installers of a GitHub Release.
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Name is the package-manager name of the installer.
const Name = "luci-app-hyper"

// SumsName is the Release asset listing every installer's SHA-256.
const SumsName = "sha256sums"

// commonArches are the OpenWrt package architectures every supported firmware publishes.
var commonArches = []string{"x86_64", "i386_pentium4", "aarch64_generic", "aarch64_cortex-a53", "aarch64_cortex-a72",
	"arm_arm926ej-s", "arm_arm1176jzf-s_vfp", "arm_cortex-a7_neon-vfpv4", "arm_cortex-a9_vfpv3-d16",
	"arm_cortex-a15_neon-vfpv4", "mips_24kc", "mipsel_24kc"}

// Arches lists, per package format, the OpenWrt package architectures a Release has one
// installer for. Each installer declares that architecture, so the package manager refuses
// it on another router. IPKs serve 23.05 / 24.10, APKs 25.12; RISC-V changed its name.
var Arches = map[string][]string{
	"ipk": append(append([]string(nil), commonArches...), "riscv64_riscv64", "loongarch64_generic"),
	"apk": append(append([]string(nil), commonArches...), "riscv64_generic", "loongarch64_generic"),
}

// Formats are the two package formats, opkg (23.05 / 24.10) and apk (25.12).
var Formats = []string{"ipk", "apk"}

// Families name the firmware a format serves, the prefix of its Release names.
var Families = map[string]string{"ipk": "23.05-24.10", "apk": "25.12+"}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ValidVersion reports whether v is a plain major.minor.patch release version.
func ValidVersion(v string) bool { return len(v) <= 64 && versionPattern.MatchString(v) }

// PackageVersion is the version the package manager records for a release: revision 1.
func PackageVersion(version, format string) string {
	if format == "apk" {
		return version + "-r1"
	}
	return version + "-1"
}

// PackageName returns the Release file name of the installer for an OpenWrt architecture,
// as 0.x's updater looks for it.
func PackageName(version, format, arch string) string {
	switch format {
	case "ipk":
		return fmt.Sprintf("%s_%s_%s_%s.ipk", Families[format], Name, version, arch)
	case "apk":
		return fmt.Sprintf("%s_%s-%s-%s.apk", Families[format], Name, version, arch)
	}
	panic("unknown package format: " + format)
}

var namePattern = regexp.MustCompile(`^(23\.05-24\.10_luci-app-hyper_|25\.12\+_luci-app-hyper-)((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))[_-]([a-z0-9_-]+)\.(ipk|apk)$`)

// ParsePackageName reads a Release installer name.
func ParsePackageName(name string) (version, format, arch string, ok bool) {
	m := namePattern.FindStringSubmatch(name)
	if m == nil {
		return "", "", "", false
	}
	version, arch, format = m[2], m[3], m[4]
	if !slices.Contains(Arches[format], arch) || PackageName(version, format, arch) != name {
		return "", "", "", false
	}
	return version, format, arch, true
}

// AssetURL is GitHub's download address of a Release asset: it escapes the + of a name.
func AssetURL(repo, tag, name string) string {
	return "https://github.com/" + repo + "/releases/download/" + tag + "/" + strings.ReplaceAll(name, "+", "%2B")
}

// ReleaseNames returns every asset of a release: each installer, then SumsName.
func ReleaseNames(version string) []string {
	var names []string
	for _, format := range Formats {
		for _, arch := range Arches[format] {
			names = append(names, PackageName(version, format, arch))
		}
	}
	return append(names, SumsName)
}

// Digest returns the hex SHA-256 of a file.
func Digest(path string) (string, error) {
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
