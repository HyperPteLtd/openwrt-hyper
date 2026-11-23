package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	sha   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tag   = "v1.0.0"
)

var firstIPK = PackageName(version, "ipk", "x86_64")

type fakeGitHub struct {
	main, tag           string
	release             *Release
	assets              []Asset
	uploaded            []string
	failAt              string
	advanceDuringUpload bool
	removeAfterPublish  string
	creates, publishes  int
}

func (g *fakeGitHub) ReleaseFor(string) (*Release, error) {
	if g.release == nil {
		return nil, nil
	}
	r := *g.release
	return &r, nil
}
func (g *fakeGitHub) TagCommit(string) (string, error) { return g.tag, nil }
func (g *fakeGitHub) RequireLatest(string) error       { return nil }
func (g *fakeGitHub) RequireMain(commit string) error {
	if g.main != commit {
		return Error("main advanced")
	}
	return nil
}
func (g *fakeGitHub) Assets(int64) ([]Asset, error) { return slices.Clone(g.assets), nil }
func (g *fakeGitHub) DeleteAsset(id int64) error {
	g.assets = slices.DeleteFunc(g.assets, func(a Asset) bool { return a.ID == id })
	return nil
}
func (g *fakeGitHub) CreateDraft(tag, commit, notes string) (*Release, error) {
	g.creates++
	g.release = &Release{ID: 1, TagName: tag, TargetCommitish: commit, Body: notes, Draft: true}
	return g.ReleaseFor(tag)
}
func (g *fakeGitHub) Upload(_ int64, name string, file *os.File) error {
	if name == g.failAt {
		g.assets = append(g.assets, Asset{ID: 100, Name: name, State: "starter"})
		return Error("interrupted upload")
	}
	data, _ := io.ReadAll(file)
	sum := sha256.Sum256(data)
	d := "sha256:" + hex.EncodeToString(sum[:])
	g.assets = append(g.assets, Asset{Name: name, Size: int64(len(data)), State: "uploaded", Digest: &d})
	g.uploaded = append(g.uploaded, name)
	if g.advanceDuringUpload {
		g.main = other
	}
	return nil
}
func (g *fakeGitHub) GetRelease(int64) (*Release, error) { return g.ReleaseFor("") }
func (g *fakeGitHub) MakeLatest(int64) (*Release, error) {
	if !g.release.Draft || g.main != g.release.TargetCommitish {
		panic("unexpected publish")
	}
	g.publishes++
	g.release.Draft = false
	g.release.HTMLURL = "https://example.org/releases/" + tag
	g.tag = g.release.TargetCommitish
	if g.removeAfterPublish != "" {
		g.assets = slices.DeleteFunc(g.assets, func(a Asset) bool { return a.Name == g.removeAfterPublish })
	}
	return g.ReleaseFor("")
}

type publishFixture struct {
	files, sums, notes string
	github             *fakeGitHub
}

func newPublishFixture(t *testing.T) *publishFixture {
	root := t.TempDir()
	p := &publishFixture{files: filepath.Join(root, "assets"), sums: filepath.Join(root, "SHA256SUMS"),
		github: &fakeGitHub{main: sha}}
	os.Mkdir(p.files, 0o755)
	var sums strings.Builder
	names := AllNames(version)
	slices.Sort(names)
	for _, name := range names {
		os.WriteFile(filepath.Join(p.files, name), []byte(name), 0o644)
		d, _ := Digest(filepath.Join(p.files, name))
		sums.WriteString(d + "  " + name + "\n")
	}
	os.WriteFile(p.sums, []byte(sums.String()), 0o644)
	p.notes = "Install packages\n"
	return p
}

func (p *publishFixture) publish(commit string) error {
	return Publish(p.github, tag, commit, p.files, p.notes, func(string) {})
}

func (p *publishFixture) assetNames() []string {
	var names []string
	for _, a := range p.github.assets {
		names = append(names, a.Name)
	}
	slices.Sort(names)
	return names
}

func TestDraftUploadResumeAndImmutableFirstRelease(t *testing.T) {
	p := newPublishFixture(t)
	p.github.failAt = firstIPK
	mustErr(t, p.publish(sha), "interrupted upload")
	if !p.github.release.Draft || p.github.tag != "" {
		t.Fatal("published after an interrupted upload")
	}
	p.github.failAt = ""
	if err := p.publish(sha); err != nil {
		t.Fatal(err)
	}
	if p.github.tag != sha {
		t.Fatal("tag not created")
	}
	p.github.main = other
	os.Remove(p.sums) // An existing public release has its own original manifest.
	if err := p.publish(other); err != nil {
		t.Fatal(err)
	}
	want := releaseNames(tag)
	slices.Sort(want)
	uploaded := slices.Clone(p.github.uploaded)
	slices.Sort(uploaded)
	if p.github.creates != 1 || p.github.publishes != 1 || !slices.Equal(uploaded, want) || !slices.Equal(p.assetNames(), want) {
		t.Fatalf("creates=%d publishes=%d uploaded=%v", p.github.creates, p.github.publishes, uploaded)
	}
}

func TestMovedMainOrExistingWrongTagNeverCreatesDraft(t *testing.T) {
	p := newPublishFixture(t)
	p.github.main = other
	mustErr(t, p.publish(sha), "main advanced")
	p.github.main, p.github.tag = sha, other
	mustErr(t, p.publish(sha), "another commit")
	if p.github.creates != 0 {
		t.Fatal("draft created")
	}
}

func TestMainAdvanceDuringUploadKeepsDraft(t *testing.T) {
	p := newPublishFixture(t)
	p.github.advanceDuringUpload = true
	mustErr(t, p.publish(sha), "main advanced")
	if !p.github.release.Draft || p.github.tag != "" {
		t.Fatal("published")
	}
}

func TestForeignOrDifferentRemoteAssetsArePreserved(t *testing.T) {
	p := newPublishFixture(t)
	p.github.failAt = firstIPK
	if p.publish(sha) == nil {
		t.Fatal("expected failure")
	}
	p.github.failAt = ""
	p.github.assets = append(p.github.assets, Asset{Name: "manual.txt", State: "uploaded", Size: 42})
	mustErr(t, p.publish(sha), "Unexpected release asset")
	if !slices.Contains(p.assetNames(), "manual.txt") {
		t.Fatal("foreign asset deleted")
	}
	p.github.assets = slices.DeleteFunc(p.github.assets, func(a Asset) bool { return a.Name == "manual.txt" })
	bad := "sha256:" + strings.Repeat("0", 64)
	p.github.assets[0].Digest = &bad
	mustErr(t, p.publish(sha), "does not match")
	if !p.github.release.Draft || p.github.publishes != 0 {
		t.Fatal("published")
	}
}

func TestUnlistedAndCorruptLocalFilesRejectedBeforeRemoteWrite(t *testing.T) {
	p := newPublishFixture(t)
	extra := filepath.Join(p.files, "notes.txt")
	os.WriteFile(extra, []byte("not an asset"), 0o644)
	mustErr(t, p.publish(sha), "match SHA256SUMS")
	os.Remove(extra)
	os.WriteFile(filepath.Join(p.files, firstIPK), []byte("different"), 0o644)
	mustErr(t, p.publish(sha), "Invalid local release asset")
	if p.github.creates != 0 {
		t.Fatal("draft created")
	}
}

func TestRetargetedPublicTagIsReported(t *testing.T) {
	p := newPublishFixture(t)
	if err := p.publish(sha); err != nil {
		t.Fatal(err)
	}
	p.github.tag = other
	mustErr(t, p.publish(sha), "disagree")
	if p.github.publishes != 1 {
		t.Fatal("published twice")
	}
}

func TestExtraFileCannotBeAnAsset(t *testing.T) {
	for _, extra := range []string{"packages.zip", "SHA256SUMS", "release-metadata.json", "luci-app-hyper_1.0.0-1_all.ipk"} {
		p := newPublishFixture(t)
		path := filepath.Join(p.files, extra)
		os.WriteFile(path, []byte("not an installer"), 0o644)
		mustErr(t, p.publish(sha), "match SHA256SUMS")
		d, _ := Digest(path)
		f, _ := os.OpenFile(p.sums, os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(d + "  " + extra + "\n")
		f.Close()
		mustErr(t, p.publish(sha), "exactly the per-architecture IPKs and APKs")
		if p.github.release != nil {
			t.Fatal("draft created")
		}
	}
}

func TestChangedNotesAreRejectedOnResume(t *testing.T) {
	p := newPublishFixture(t)
	p.github.failAt = firstIPK
	if p.publish(sha) == nil {
		t.Fatal("expected failure")
	}
	p.github.failAt = ""
	p.github.release.Body = "manual release notes"
	mustErr(t, p.publish(sha), "draft notes differ")
	if !p.github.release.Draft {
		t.Fatal("published")
	}
}

func TestPublishedAssetsAreCheckedAgain(t *testing.T) {
	p := newPublishFixture(t)
	p.github.removeAfterPublish = firstIPK
	mustErr(t, p.publish(sha), "confirm both published installers")
	mustErr(t, p.publish(sha), "Incomplete published release assets")
	if p.github.publishes != 1 {
		t.Fatal("published twice")
	}
	p.github.assets = append(p.github.assets, Asset{Name: "packages.zip"})
	mustErr(t, p.publish(sha), "Unexpected release asset")
}

func TestNewerLatestReleaseCannotBeReplaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.URL.Path != "/repos/owner/example/releases/latest" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		io.WriteString(w, `{"tag_name":"v1.10.0"}`)
	}))
	defer server.Close()
	g := NewGitHub("owner/example", "fixture")
	g.base = server.URL
	var e Error
	if err := g.RequireLatest("v1.9.1"); !errors.As(err, &e) || !strings.Contains(err.Error(), "newer latest release") {
		t.Fatal(err)
	}
	if err := g.RequireLatest("v1.11.0"); err != nil {
		t.Fatal(err)
	}
}
