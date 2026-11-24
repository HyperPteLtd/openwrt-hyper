// Original Linux CLI release assets are always identified independently of
// whichever server or uploaded directory supplies the archive.
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

var releaseAPI = "https://api.github.com/repos/" + Repository + "/releases"

type releaseAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type githubRelease struct {
	Tag        string         `json:"tag_name"`
	Prerelease bool           `json:"prerelease"`
	Draft      bool           `json:"draft"`
	Assets     []releaseAsset `json:"assets"`
}

type pin struct {
	Name string
	Size int64
	Hash string
}

func assetName(tag, target string) string {
	return "mihomo-" + strings.TrimPrefix(tag, "v") + "-linux-" + target + ".tar.gz"
}

const rolling = "Prerelease-Alpha"
const alphaPrefix = "v0.0.0-alpha."

var alphaRevision = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func runtimeVersion(tag string) string {
	if rev, ok := strings.CutPrefix(tag, alphaPrefix); ok && alphaRevision.MatchString(rev) {
		return "alpha-" + rev
	}
	return tag
}
func BinaryVersion(tag string) string { return runtimeVersion(tag) }

var releaseLine = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}-v[0-9]`)

// ReportsVersion: the core built for tag says so in `mihomo -v`. A core release line may name
// its build after itself ("<line>-v1.0.1"), which the package carries as the plain tag; its
// pin, not this string, is what identifies the binary.
func ReportsVersion(reported, tag string) bool {
	if reported == BinaryVersion(tag) {
		return true
	}
	line, rest, ok := strings.Cut(reported, "-")
	return ok && rest == tag && releaseLine.MatchString(reported) && line != "alpha"
}

// releaseRef is the repository's tag for the release of tag: the rolling alpha, or the line's
// release of the version.
func releaseRef(tag string) string {
	if strings.HasPrefix(runtimeVersion(tag), "alpha-") {
		return rolling
	}
	if ReleaseLine != "" {
		return ReleaseLine + "-" + tag
	}
	return tag
}

// lineVersion is the version a repository tag releases on the line; ok is false for a tag
// of another series.
func lineVersion(tag string) (string, bool) {
	if ReleaseLine == "" {
		return tag, true
	}
	return strings.CutPrefix(tag, ReleaseLine+"-")
}
func rawPrefix(target string) string {
	switch target {
	case "amd64":
		target = "amd64-v1"
	case "loong64":
		target = "loong64-abi2"
	}
	return "mihomo-linux-" + target + "-"
}

// rawAsset names the release's raw build of tag for target, after the release's own tag.
func rawAsset(tag, target string) string {
	name := runtimeVersion(tag)
	if name == tag {
		name = releaseRef(tag)
	}
	return rawPrefix(target) + name + ".gz"
}

// rawBuild: name is the raw build of tag, by the release's name or by the version alone, as the
// 1.0.0 package named the core it carried.
func rawBuild(name, tag, target string) bool {
	return name == rawAsset(tag, target) || name == rawPrefix(target)+runtimeVersion(tag)+".gz"
}
func (r githubRelease) identity(target string) string {
	if r.Tag != rolling {
		v, ok := lineVersion(r.Tag)
		if !ok {
			return ""
		}
		return v
	}
	if !r.Prerelease {
		return ""
	}
	var tag string
	for _, a := range r.Assets {
		rev, ok := strings.CutPrefix(a.Name, rawPrefix(target)+"alpha-")
		if !ok {
			continue
		}
		rev, ok = strings.CutSuffix(rev, ".gz")
		if !ok || !alphaRevision.MatchString(rev) {
			continue
		}
		if tag != "" {
			return ""
		}
		tag = alphaPrefix + rev
	}
	return tag
}
func (r githubRelease) packageFile(target string) (releaseAsset, bool) {
	tag := r.identity(target)
	if tag == "" {
		return releaseAsset{}, false
	}
	if a, ok := r.asset(assetName(tag, target)); ok {
		return a, true
	}
	return r.asset(rawAsset(tag, target))
}

func (r githubRelease) asset(name string) (releaseAsset, bool) {
	var selected releaseAsset
	for _, a := range r.Assets {
		if a.Name == name {
			if selected.Name != "" {
				return releaseAsset{}, false
			}
			selected = a
		}
	}
	return selected, selected.Size > 0 && selected.Size <= maxAsset
}

func (a releaseAsset) hash() string {
	h, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok || !validHash(h) {
		return ""
	}
	return h
}

func checksumLine(b []byte, name string) (string, error) {
	var sum string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			if sum != "" || !validHash(f[0]) {
				return "", errors.New("invalid or ambiguous upstream checksum")
			}
			sum = f[0]
		}
	}
	if sum == "" {
		return "", errors.New("upstream checksum is missing for this architecture")
	}
	return sum, nil
}

func originalPin(ctx context.Context, tag, target string) (pin, error) {
	f, err := fetch(ctx, releaseAPI+"/tags/"+releaseRef(tag))
	b, err := getBounded(f, err, maxDocument)
	if err != nil {
		return pin{}, fmt.Errorf("cannot verify original release via GitHub: %w", err)
	}
	var r githubRelease
	if json.Unmarshal(b, &r) != nil || r.Tag != releaseRef(tag) || r.identity(target) != tag || r.Draft {
		return pin{}, errors.New("invalid original release identity")
	}
	return r.upstreamPin(ctx, target)
}

func (r githubRelease) upstreamPin(ctx context.Context, target string) (pin, error) {
	tag := r.Tag
	a, ok := r.packageFile(target)
	name := a.Name
	if !ok {
		return pin{}, errors.New("original release lacks a matching Linux CLI archive")
	}
	if hash := a.hash(); hash != "" {
		return pin{Name: a.Name, Size: a.Size, Hash: hash}, nil
	}
	if a.Digest != "" {
		return pin{}, errors.New("invalid original release asset digest")
	}
	if _, ok := r.asset("sha256sums"); !ok {
		return pin{}, errors.New("original release supplies no SHA-256 for this archive")
	}
	f, err := fetch(ctx, origin+"/"+tag+"/sha256sums")
	b, err := getBounded(f, err, maxDocument)
	if err != nil {
		if errors.Is(err, ErrTemporaryDownload) {
			return pin{}, err
		}
		return pin{}, fmt.Errorf("original release checksum unavailable: %w", err)
	}
	hash, err := checksumLine(b, name)
	return pin{Name: a.Name, Size: a.Size, Hash: hash}, err
}

type Version struct {
	Tag             string `json:"tag"`
	Prerelease      bool   `json:"prerelease"`
	SignedMaterials bool   `json:"signed_materials"`
	Installable     bool   `json:"installable"`
	Asset           string `json:"asset,omitempty"`
}

func (r githubRelease) version(target string) Version {
	_, manifest := r.asset("manifest.json")
	_, signature := r.asset("manifest.json.minisig")
	_, caps := r.asset("capabilities.json")
	v := Version{Tag: r.identity(target), Prerelease: r.Prerelease, SignedMaterials: manifest && signature && caps}
	a, ok := r.packageFile(target)
	name := a.Name
	if ok {
		_, checksums := r.asset("sha256sums")
		v.Asset = name
		v.Installable = a.hash() != "" || a.Digest == "" && checksums

	}
	return v
}

func Versions(ctx context.Context, target string) ([]Version, error) {
	if _, ok := platforms[target]; !ok {
		return nil, errors.New("invalid kernel target")
	}
	var versions []Version
	for page := 1; page <= 30; page++ {
		addr := fmt.Sprintf("%s?per_page=100&page=%d", releaseAPI, page)
		f, err := fetch(ctx, addr)
		b, err := getBounded(f, err, 8<<20)
		if err != nil {
			return nil, err
		}
		var list []githubRelease
		if err = json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
		for _, r := range list {
			if !r.Draft && len(r.Tag) <= 100 && TagPattern.MatchString(r.identity(target)) {
				versions = append(versions, r.version(target))
			}
		}
		if len(list) < 100 {
			break
		}
	}
	return versions, nil
}

// A stable release of the line is a plain vX.Y.Z version under its line's tag (lineVersion),
// apart from the mihomo v* tags the repository carries.
var stableVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func stableTag(tag string) (string, bool) {
	v, ok := lineVersion(tag)
	return v, ok && ReleaseLine != "" && stableVersion.MatchString(v)
}

func latestStable(ctx context.Context, target string) (string, pin, error) {
	f, err := fetch(ctx, releaseAPI+"/latest")
	b, err := getBounded(f, err, maxDocument)
	if errors.Is(err, errReleaseNotFound) {
		return "", pin{}, errors.New("publisher has no stable release")
	}
	if err != nil {
		if tlsdiag.From(err) != nil || errors.Is(err, ErrTemporaryDownload) || errors.Is(err, ErrReleaseRateLimit) || ctx.Err() != nil {
			return "", pin{}, err
		}
		return "", pin{}, errors.New("latest stable release unreachable")
	}
	var data struct {
		Tag        string         `json:"tag_name"`
		URL        string         `json:"html_url"`
		Draft      *bool          `json:"draft"`
		Prerelease *bool          `json:"prerelease"`
		Assets     []releaseAsset `json:"assets"`
	}
	if json.Unmarshal(b, &data) != nil || data.Draft == nil || *data.Draft || data.Prerelease == nil || *data.Prerelease ||
		len(data.Tag) > 100 || data.URL != "https://github.com/"+Repository+"/releases/tag/"+data.Tag {
		return "", pin{}, errors.New("invalid latest stable release identity")
	}
	tag, ok := stableTag(data.Tag)
	if !ok {
		return "", pin{}, errors.New("invalid latest stable release identity")
	}
	r := githubRelease{Tag: data.Tag, Assets: data.Assets}
	if !r.version(target).Installable {
		return "", pin{}, errors.New("latest stable release lacks a verified archive for this router")
	}

	p, err := r.upstreamPin(ctx, target)
	return tag, p, err
}
