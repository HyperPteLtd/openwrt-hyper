// Package kernel installs and manages verified Linux router cores.
package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"aead.dev/minisign"
)

// PublicKey is the publisher's base64 minisign public key, linked by the
// trusted plugin build. Never derive it from a download or user input.
var PublicKey string

// Repository names the GitHub repository online core releases come from; a build sets it with
// -X to offer them. Empty here: the package carries its core (BundleDir) and the router never
// downloads one. It is never taken from a subscription.
var Repository = ""

const Controller = "mihomo-rest-v1"
const maxAsset int64 = 512 << 20
const maxDocument = 2 << 20

var TagPattern = regexp.MustCompile(`^(?:hyper-)?v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.+-]+)?$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

type Member struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Asset struct {
	Name     string   `json:"name"`
	Arch     string   `json:"arch"`
	Endian   string   `json:"endian"`
	FloatABI string   `json:"float_abi"`
	Size     int64    `json:"size"`
	Expanded int64    `json:"expanded_size"`
	SHA256   string   `json:"sha256"`
	Files    []Member `json:"files"`
}
type Manifest struct {
	Schema          int              `json:"schema_version"`
	Repository      string           `json:"repository"`
	Tag             string           `json:"tag"`
	Build           string           `json:"build_id"`
	CapabilitiesSHA string           `json:"capabilities_sha256"`
	Assets          map[string]Asset `json:"assets"`
}
type TargetCapabilities struct {
	ProtocolSupport map[string]bool `json:"protocol_support,omitempty"`
	Naive           bool            `json:"naive"`
	TUN             bool            `json:"tun"`
	TProxy          bool            `json:"tproxy"`

	Control string `json:"control_api"`

	PeakRAM int64 `json:"peak_ram_bytes"`
}
type Capabilities struct {
	Schema     int                           `json:"schema_version"`
	Repository string                        `json:"repository"`
	Tag        string                        `json:"tag"`
	Build      string                        `json:"build_id"`
	Targets    map[string]TargetCapabilities `json:"targets"`
}
type Verified struct {
	Manifest     Manifest
	Capabilities Capabilities
	Asset        Asset
	Target       TargetCapabilities
}

type abi struct{ arch, endian, float string }

var platforms = map[string]abi{
	"amd64": {"amd64", "little", "native"}, "arm64": {"arm64", "little", "native"},
	"armv5": {"arm", "little", "soft"}, "armv6": {"arm", "little", "hard"}, "armv7": {"arm", "little", "hard"},
	"mips-softfloat": {"mips", "big", "soft"}, "mipsle-softfloat": {"mipsle", "little", "soft"},
	"386-softfloat": {"386", "little", "soft"}, "riscv64": {"riscv64", "little", "native"}, "loong64": {"loong64", "little", "native"},
}

func Digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func safeName(name string) bool {
	if name == "" || len(name) > 384 || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if !identifier.MatchString(part) {
			return false
		}
	}
	return true
}
func verifyKey(key string) (minisign.PublicKey, error) {
	var pub minisign.PublicKey
	if key == "" {
		return pub, errors.New("publisher trust root unavailable")
	}
	if err := pub.UnmarshalText([]byte(key)); err != nil {
		return pub, errors.New("invalid publisher trust root")
	}
	return pub, nil
}
func Verify(key, tag, target string, doc, sig, caps []byte) (*Verified, error) {
	pub, err := verifyKey(key)
	if err != nil {
		return nil, err
	}
	if len(doc) > maxDocument || len(sig) > 4096 || len(caps) > maxDocument || !minisign.Verify(pub, doc, sig) {
		return nil, errors.New("invalid publisher signature")
	}
	var m Manifest
	var c Capabilities
	if err = json.Unmarshal(doc, &m); err != nil {
		return nil, errors.New("invalid release manifest")
	}
	if !TagPattern.MatchString(tag) || len(tag) > 100 || m.Schema != 1 || m.Repository != Repository || m.Tag != tag || !identifier.MatchString(m.Build) || Digest(caps) != m.CapabilitiesSHA {
		return nil, errors.New("release identity or capabilities digest mismatch")
	}
	if err = json.Unmarshal(caps, &c); err != nil || c.Schema != 1 || c.Repository != Repository || c.Tag != tag || c.Build != m.Build {
		return nil, errors.New("invalid release capabilities")
	}
	p, ok := platforms[target]
	a, present := m.Assets[target]
	tc, has := c.Targets[target]
	if !ok || !present || !has || a.Arch != p.arch || a.Endian != p.endian || a.FloatABI != p.float {
		return nil, errors.New("release lacks matching architecture and ABI")
	}
	seen, err := checkMembers(a)
	if err != nil {
		return nil, err
	}
	if a.Name != assetName(tag, target) || seen[strings.TrimSuffix(a.Name, ".tar.gz")+"/mihomo"] != "core" {
		return nil, errors.New("invalid Mihomo archive layout")
	}
	if tc.Control != Controller || tc.PeakRAM <= 0 || tc.PeakRAM > 16<<30 {
		return nil, errors.New("invalid Mihomo capabilities")
	}
	return &Verified{m, c, a, tc}, nil
}

func checkMembers(a Asset) (map[string]string, error) {
	if !identifier.MatchString(a.Name) || !strings.HasSuffix(a.Name, ".gz") || !validHash(a.SHA256) || a.Size <= 0 || a.Size > maxAsset || a.Expanded <= 0 || a.Expanded > maxAsset || len(a.Files) > 64 {
		return nil, errors.New("invalid release asset")
	}
	seen := map[string]string{}
	var total int64
	cores := 0
	for _, f := range a.Files {
		if !safeName(f.Path) || seen[f.Path] != "" || !validHash(f.SHA256) || f.Size <= 0 || f.Size > maxAsset || total > maxAsset-f.Size {
			return nil, errors.New("invalid release archive member")
		}
		switch f.Kind {
		case "core":
			cores++
		case "capabilities", "notice":
		default:
			return nil, errors.New("unknown archive member kind")
		}
		seen[f.Path] = f.Kind
		total += f.Size
	}
	for key := range seen {
		for dir := path.Dir(key); dir != "."; dir = path.Dir(dir) {
			if seen[dir] != "" {
				return nil, errors.New("overlapping archive paths")
			}
		}
	}
	if total != a.Expanded || cores != 1 {
		return nil, errors.New("invalid archive member count or expanded size")
	}
	return seen, nil
}

func DetectTarget(arch string) (string, error) {
	if _, ok := platforms[arch]; ok {
		return arch, nil
	}
	switch {
	case arch == "x86_64":
		return "amd64", nil
	case strings.HasPrefix(arch, "aarch64_"):
		return "arm64", nil
	case strings.HasPrefix(arch, "arm_arm1176jzf"):
		return "armv6", nil
	case strings.HasPrefix(arch, "arm_cortex-a") && (strings.Contains(arch, "vfp") || strings.Contains(arch, "neon")):
		return "armv7", nil
	case strings.HasPrefix(arch, "arm_"):
		return "armv5", nil
	case strings.HasPrefix(arch, "mipsel_"):
		return "mipsle-softfloat", nil
	case strings.HasPrefix(arch, "mips_"):
		return "mips-softfloat", nil
	case strings.HasPrefix(arch, "i386_"):
		return "386-softfloat", nil
	case strings.HasPrefix(arch, "riscv64_"):
		return "riscv64", nil
	case strings.HasPrefix(arch, "loongarch64_"):
		return "loong64", nil
	}
	return "", errors.New("unrecognized OpenWrt CPU architecture")
}
