package release

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Error is a release precondition or verification failure.
type Error string

func (e Error) Error() string { return string(e) }

func errorf(format string, args ...any) error { return Error(fmt.Sprintf(format, args...)) }

var (
	semverTag     = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	commitSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	manifestRow   = regexp.MustCompile(`^([0-9a-f]{64}) [ *]([A-Za-z0-9][A-Za-z0-9_.+-]*)$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ValidTag reports whether tag is vMAJOR.MINOR.PATCH.
func ValidTag(tag string) bool { return semverTag.MatchString(tag) }

// ValidCommit reports whether sha is a full commit hash.
func ValidCommit(sha string) bool { return commitSHA.MatchString(sha) }

// ValidRepo reports whether repo is owner/name.
func ValidRepo(repo string) bool { return repoPattern.MatchString(repo) }

func semver(tag string) [3]int {
	var v [3]int
	m := semverTag.FindStringSubmatch(tag)
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v
}

// Release is the part of a GitHub release the publisher reads.
type Release struct {
	ID              int64  `json:"id"`
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Body            string `json:"body"`
	HTMLURL         string `json:"html_url"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
}

// Asset is the part of a GitHub release asset the publisher reads.
type Asset struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	State  string  `json:"state"`
	Size   int64   `json:"size"`
	Digest *string `json:"digest"`
}

// API is the GitHub surface used to publish a release.
type API interface {
	ReleaseFor(tag string) (*Release, error)
	TagCommit(tag string) (string, error)
	RequireLatest(tag string) error
	RequireMain(commit string) error
	Assets(release int64) ([]Asset, error)
	DeleteAsset(id int64) error
	CreateDraft(tag, commit, notes string) (*Release, error)
	Upload(release int64, name string, file *os.File) error
	GetRelease(release int64) (*Release, error)
	MakeLatest(release int64) (*Release, error)
}

type expectation struct {
	size   int64  // 0: not known (published release)
	digest string // "": not known (published release)
	path   string
}

// SumsName is the checksum manifest published beside the installer packages.
const SumsName = "SHA256SUMS"

// releaseNames lists every asset of the release for tag: the installers and SHA256SUMS.
// packageVersion is the installers' version for tag: a release carries the revision after
// its last preview (1.2.0-r3), read from the IPKs in dir; without one it is revision 1.
func packageVersion(tag, dir string) string {
	v := strings.TrimPrefix(tag, "v")
	if n, err := AssetRevision(v, dir); err == nil {
		return RevisionVersion(v, n)
	}
	return v
}

// releaseNames lists a release's assets for a tag or a package version with its revision.
func releaseNames(tag string) []string {
	return append(AllNames(strings.TrimPrefix(tag, "v")), SumsName)
}

func manifest(lines []string, tag string) (map[string]string, error) {
	packages := map[string]bool{}
	for _, n := range AllNames(strings.TrimPrefix(tag, "v")) {
		packages[n] = true
	}
	expected := map[string]string{}
	for _, line := range lines {
		m := manifestRow.FindStringSubmatch(line)
		if m == nil {
			return nil, Error("Invalid SHA256SUMS entry")
		}
		if _, dup := expected[m[2]]; dup {
			return nil, errorf("Duplicate checksum for %s", m[2])
		}
		expected[m[2]] = m[1]
	}
	if len(expected) != len(packages) {
		return nil, errorf("SHA256SUMS must contain exactly the per-architecture IPKs and APKs for %s", tag)
	}
	for name := range expected {
		if !packages[name] {
			return nil, errorf("SHA256SUMS must contain exactly the per-architecture IPKs and APKs for %s", tag)
		}
	}
	return expected, nil
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func localAssets(dir, tag string) (map[string]expectation, error) {
	sums := filepath.Join(filepath.Dir(filepath.Clean(dir)), "SHA256SUMS")
	if info, err := os.Lstat(sums); err != nil || !info.Mode().IsRegular() {
		return nil, Error("Missing SHA256SUMS")
	}
	raw, err := os.ReadFile(sums)
	if err != nil {
		return nil, err
	}
	expected, err := manifest(splitLines(string(raw)), packageVersion(tag, dir))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(expected) {
		return nil, Error("Release files must match SHA256SUMS exactly")
	}
	for _, e := range entries {
		if _, ok := expected[e.Name()]; !ok {
			return nil, Error("Release files must match SHA256SUMS exactly")
		}
	}
	assets := map[string]expectation{}
	for name, checksum := range expected {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, errorf("Invalid local release asset: %s", name)
		}
		if d, err := Digest(path); err != nil || d != checksum {
			return nil, errorf("Invalid local release asset: %s", name)
		}
		assets[name] = expectation{info.Size(), "sha256:" + checksum, path}
	}
	info, _ := os.Lstat(sums)
	d, err := Digest(sums)
	if err != nil {
		return nil, err
	}
	assets[SumsName] = expectation{info.Size(), "sha256:" + d, sums}
	return assets, nil
}

func verifyTarget(api API, tag, commit string, release *Release) error {
	actual, err := api.TagCommit(tag)
	if err != nil {
		return err
	}
	if release != nil && !release.Draft {
		if release.Prerelease {
			return errorf("%s is already a published prerelease", tag)
		}
		target := release.TargetCommitish
		if actual == "" || (commitSHA.MatchString(target) && target != actual) {
			return errorf("Published release and tag disagree for %s", tag)
		}
		return nil
	}
	if actual != "" && actual != commit {
		return errorf("%s already identifies another commit", tag)
	}
	if release != nil && (release.TargetCommitish != commit || release.Prerelease) {
		return errorf("Existing draft for %s has a different target or release type", tag)
	}
	return nil
}

func inspectAssets(api API, release int64, expected map[string]expectation, resume bool) (map[string]bool, error) {
	assets, err := api.Assets(release)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	var placeholders []int64
	for _, a := range assets {
		want, ok := expected[a.Name]
		if !ok || present[a.Name] {
			return nil, errorf("Unexpected release asset: %s", a.Name)
		}
		// GitHub can leave an empty "starter" placeholder after a failed upload.
		// Never delete an uploaded asset, including an uploaded zero-byte file.
		if resume && a.State == "starter" && a.Size == 0 {
			placeholders = append(placeholders, a.ID)
			continue
		}
		if a.State != "uploaded" || a.Size <= 0 || a.Digest == nil || !digestPattern.MatchString(*a.Digest) ||
			(want.digest != "" && *a.Digest != want.digest) ||
			(want.size != 0 && a.Size != want.size) {
			return nil, errorf("Remote asset does not match the build: %s", a.Name)
		}
		present[a.Name] = true
	}
	for _, id := range placeholders {
		if err := api.DeleteAsset(id); err != nil {
			return nil, err
		}
	}
	return present, nil
}

func complete(present map[string]bool, expected map[string]expectation) bool {
	if len(present) != len(expected) {
		return false
	}
	for name := range expected {
		if !present[name] {
			return false
		}
	}
	return true
}

func sameLines(a, b string) bool { return slices.Equal(splitLines(a), splitLines(b)) }

// Publish uploads the exact verified asset set in dir to a draft for tag at commit and then
// publishes it as the latest release. It is safe to retry after any interruption.
func Publish(api API, tag, commit, dir, notes string, out func(string)) error {
	release, err := api.ReleaseFor(tag)
	if err != nil {
		return err
	}
	if err := verifyTarget(api, tag, commit, release); err != nil {
		return err
	}
	if release != nil && !release.Draft {
		// Later builds of the same version may have different signing bytes, so a
		// published release is only checked for its complete, uploaded asset set.
		expected := map[string]expectation{}
		for _, name := range releaseNames(packageVersion(tag, dir)) {
			expected[name] = expectation{}
		}
		present, err := inspectAssets(api, release.ID, expected, false)
		if err != nil {
			return err
		}
		if !complete(present, expected) {
			return Error("Incomplete published release assets")
		}
		out(tag + " is already published")
		return nil
	}
	expected, err := localAssets(dir, tag)
	if err != nil {
		return err
	}
	if err := api.RequireMain(commit); err != nil {
		return err
	}
	if release == nil {
		if release, err = api.CreateDraft(tag, commit, notes); err != nil {
			return err
		}
	}
	if !release.Draft || release.TagName != tag || release.TargetCommitish != commit {
		return Error("GitHub draft does not match the requested tag and commit")
	}
	if !sameLines(release.Body, notes) {
		return Error("Existing draft notes differ from the prepared release")
	}
	present, err := inspectAssets(api, release.ID, expected, true)
	if err != nil {
		return err
	}
	var missing []string
	for name := range expected {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	for _, name := range missing {
		f, err := os.Open(expected[name].path)
		if err != nil {
			return err
		}
		err = api.Upload(release.ID, name, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if present, err = inspectAssets(api, release.ID, expected, false); err != nil {
		return err
	} else if !complete(present, expected) {
		return Error("Incomplete remote release assets")
	}
	draft, err := api.GetRelease(release.ID)
	if err != nil {
		return err
	}
	if !draft.Draft || draft.TagName != tag || draft.TargetCommitish != commit || !sameLines(draft.Body, notes) {
		return Error("Release changed during upload")
	}
	if err := verifyTarget(api, tag, commit, draft); err != nil {
		return err
	}
	if err := api.RequireLatest(tag); err != nil {
		return err
	}
	if err := api.RequireMain(commit); err != nil {
		return err
	}
	published, err := api.MakeLatest(release.ID)
	if err != nil {
		return err
	}
	if actual, err := api.TagCommit(tag); err != nil {
		return err
	} else if published.Draft || published.TagName != tag || actual != commit {
		return Error("Could not confirm the published tag at its tested commit")
	}
	present, err = inspectAssets(api, release.ID, expected, false)
	if err != nil && !errors.As(err, new(Error)) {
		return err
	}
	if !sameLines(published.Body, notes) || err != nil || !complete(present, expected) {
		return Error("Could not confirm both published installers and release notes")
	}
	out(published.HTMLURL)
	return nil
}
