// Package release names, verifies, describes and publishes the per-architecture Hyper installers.
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
)

// Name is the package-manager name of the installer.
const Name = "luci-app-hyper"

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

// AllArches lists every architecture of either format, in release-notes order.
var AllArches = append(append([]string(nil), commonArches...), "riscv64_riscv64", "riscv64_generic", "loongarch64_generic")

// Formats are the two package formats, opkg (23.05 / 24.10) and apk (25.12).
var Formats = []string{"ipk", "apk"}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ValidVersion reports whether v is a plain major.minor.patch release version.
func ValidVersion(v string) bool { return len(v) <= 64 && versionPattern.MatchString(v) }

var revisionPattern = regexp.MustCompile(`^((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-r([1-9][0-9]{0,5})$`)

// RevisionVersion is the package version of revision n of version: 1.0.1-r3. Previews of a
// version count r1, r2, …; its stable release takes the next revision, so every build upgrades
// the one before it in both opkg and apk.
func RevisionVersion(version string, n int) string { return fmt.Sprintf("%s-r%d", version, n) }

// ValidPackageVersion accepts a release version (revision 1) or a version with a revision.
func ValidPackageVersion(v string) bool {
	return ValidVersion(v) || len(v) <= 64 && revisionPattern.MatchString(v)
}

// splitRevision returns the version and package revision of a package version.
func splitRevision(v string) (string, string) {
	if m := revisionPattern.FindStringSubmatch(v); m != nil {
		return m[1], m[2]
	}
	return v, "1"
}

// PackageName returns the Release file name of the installer for an OpenWrt architecture.
func PackageName(version, format, arch string) string {
	base, rev := splitRevision(version)
	switch format {
	case "ipk":
		return fmt.Sprintf("%s_%s-%s_%s.ipk", Name, base, rev, arch)
	case "apk":
		return fmt.Sprintf("%s-%s-r%s_%s.apk", Name, base, rev, arch)
	}
	panic("unknown package format: " + format)
}

// FormatNames returns every installer of one format.
func FormatNames(version, format string) []string {
	names := make([]string, 0, len(Arches[format]))
	for _, arch := range Arches[format] {
		names = append(names, PackageName(version, format, arch))
	}
	return names
}

// AllNames returns every installer of a release.
func AllNames(version string) []string {
	var names []string
	for _, format := range Formats {
		names = append(names, FormatNames(version, format)...)
	}
	return names
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
