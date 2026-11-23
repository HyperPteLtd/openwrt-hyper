package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Invalid fixtures FakeLatest can produce.
var FakeLatestInvalid = []string{"digest", "tag", "origin", "url", "duplicate", "draft", "prerelease", "missing"}

// FakeLatest writes GitHub's native latest-release response for the isolated router test,
// for the single package in dir. contents, if set, supplies the bytes whose hash is served.
// It returns tag/package-name.
func FakeLatest(dir, response, invalid, contents string) (string, error) {
	var packages []string
	for _, pattern := range []string{"*.apk", "*.ipk"} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return "", err
		}
		packages = append(packages, m...)
	}
	if len(packages) != 1 {
		return "", errors.New("expected exactly one package-manager-specific installation file")
	}
	pkg := packages[0]
	name := filepath.Base(pkg)
	names := map[string]*regexp.Regexp{
		".apk": regexp.MustCompile(`^luci-app-hyper-(\d+\.\d+\.\d+)-r([1-9][0-9]*)_([a-z0-9_-]+)\.apk$`),
		".ipk": regexp.MustCompile(`^luci-app-hyper_(\d+\.\d+\.\d+)-([1-9][0-9]*)_([a-z0-9_-]+)\.ipk$`),
	}
	m := names[filepath.Ext(name)].FindStringSubmatch(name)
	if m == nil || !slices.Contains(Arches[filepath.Ext(name)[1:]], m[3]) {
		return "", fmt.Errorf("unexpected Release package name: %s", name)
	}
	tag := "v" + m[1]
	repo := "https://github.com/HyperPteLtd/openwrt-hyper/releases"
	type asset struct {
		Name     string  `json:"name"`
		State    string  `json:"state"`
		Size     int64   `json:"size"`
		Digest   *string `json:"digest"`
		Download string  `json:"browser_download_url"`
	}
	resp := struct {
		TagName    string  `json:"tag_name"`
		HTMLURL    string  `json:"html_url"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}{TagName: tag, HTMLURL: repo + "/tag/" + tag}
	// A release's revision follows its last preview, so it is read from the package name.
	for _, filename := range releaseNames(m[1] + "-r" + m[2]) {
		var size int64
		var digest string
		if filename == name {
			path := pkg
			if contents != "" {
				path = contents
			}
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if digest, err = Digest(path); err != nil {
				return "", err
			}
			size = info.Size()
		} else {
			// Each CI job builds one architecture and format. These sibling entries
			// and SHA256SUMS are never downloaded.
			payload := sha256.Sum256([]byte("other package: " + filename))
			size, digest = int64(len("other package: "+filename)), hex.EncodeToString(payload[:])
		}
		d := "sha256:" + digest
		a := asset{Name: filename, State: "uploaded", Size: size, Digest: &d, Download: repo + "/download/" + tag + "/" + filename}
		if filename == name {
			switch invalid {
			case "digest":
				a.Digest = nil
			case "url":
				a.Download = strings.Replace(a.Download, "/hyper/", "/other/", 1)
			}
		}
		resp.Assets = append(resp.Assets, a)
	}
	switch invalid {
	case "duplicate":
		resp.Assets = []asset{resp.Assets[0], resp.Assets[0]}
	case "missing":
		resp.Assets = resp.Assets[:len(resp.Assets)-1]
	case "tag":
		resp.TagName = "v99.0.0"
	case "origin":
		resp.HTMLURL = strings.Replace(resp.HTMLURL, "/hyper/", "/other/", 1)
	case "draft":
		resp.Draft = true
	case "prerelease":
		resp.Prerelease = true
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(response, data, 0o644); err != nil {
		return "", err
	}
	return tag + "/" + name, nil
}
