package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// GitHub is the REST API client used by the release workflow.
type GitHub struct {
	repo, token string
	client      *http.Client
	nextWrite   time.Time
	// base is the REST origin.
	base string
}

// NewGitHub returns a client for owner/name authenticated by token.
func NewGitHub(repo, token string) *GitHub {
	return &GitHub{repo: repo, token: token, base: "https://api.github.com",
		client: &http.Client{Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // Never redirect the token.
		}}}
}

func (g *GitHub) do(method, host, path string, body io.Reader, size int64, contentType string, out any, optional bool) (bool, error) {
	if method != http.MethodGet {
		time.Sleep(time.Until(g.nextWrite))
		g.nextWrite = time.Now().Add(time.Second)
	}
	req, err := http.NewRequest(method, host+"/repos/"+g.repo+path, body)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("User-Agent", "Hyper-release")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return false, err
	}
	if optional && resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, errorf("%s %s: GitHub returned HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(content) > 0 {
		return true, json.Unmarshal(content, out)
	}
	return true, nil
}

func (g *GitHub) api(method, path string, payload, out any, optional bool) (bool, error) {
	var body io.Reader
	size := int64(-1)
	contentType := ""
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return false, err
		}
		body, size, contentType = bytes.NewReader(data), int64(len(data)), "application/json"
	}
	return g.do(method, g.base, path, body, size, contentType, out, optional)
}

func pages[T any](g *GitHub, path string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		var batch []T
		if _, err := g.api(http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil, &batch, false); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
}

// ReleaseFor lists releases, which with write access also finds drafts whose tag does not exist yet.
func (g *GitHub) ReleaseFor(tag string) (*Release, error) {
	releases, err := pages[Release](g, "/releases")
	if err != nil {
		return nil, err
	}
	var found *Release
	for i := range releases {
		if releases[i].TagName == tag {
			if found != nil {
				return nil, errorf("Multiple releases use %s", tag)
			}
			found = &releases[i]
		}
	}
	return found, nil
}

type gitObject struct {
	Object struct{ Type, SHA string } `json:"object"`
}

func (g *GitHub) TagCommit(tag string) (string, error) {
	var ref gitObject
	if ok, err := g.api(http.MethodGet, "/git/ref/tags/"+tag, nil, &ref, true); err != nil || !ok {
		return "", err
	}
	obj := ref.Object
	for obj.Type == "tag" {
		var annotated gitObject
		if _, err := g.api(http.MethodGet, "/git/tags/"+obj.SHA, nil, &annotated, false); err != nil {
			return "", err
		}
		obj = annotated.Object
	}
	if obj.Type != "commit" {
		return "", errorf("%s is not a commit", tag)
	}
	return obj.SHA, nil
}

func (g *GitHub) RequireLatest(tag string) error {
	var latest Release
	ok, err := g.api(http.MethodGet, "/releases/latest", nil, &latest, true)
	if err != nil {
		return err
	}
	if ok && ValidTag(latest.TagName) {
		prev, next := semver(latest.TagName), semver(tag)
		if prev[0] > next[0] || prev[0] == next[0] && (prev[1] > next[1] || prev[1] == next[1] && prev[2] > next[2]) {
			return errorf("Refusing to replace newer latest release %s with %s", latest.TagName, tag)
		}
	}
	return nil
}

func (g *GitHub) RequireMain(commit string) error {
	var ref gitObject
	if _, err := g.api(http.MethodGet, "/git/ref/heads/main", nil, &ref, false); err != nil {
		return err
	}
	if ref.Object.SHA != commit {
		return Error("main advanced; only the current, tested commit may publish")
	}
	return nil
}

func (g *GitHub) Assets(release int64) ([]Asset, error) {
	return pages[Asset](g, fmt.Sprintf("/releases/%d/assets", release))
}

func (g *GitHub) DeleteAsset(id int64) error {
	_, err := g.api(http.MethodDelete, fmt.Sprintf("/releases/assets/%d", id), nil, nil, false)
	return err
}

func (g *GitHub) CreateDraft(tag, commit, notes string) (*Release, error) {
	var r Release
	_, err := g.api(http.MethodPost, "/releases", map[string]any{"tag_name": tag, "target_commitish": commit,
		"name": tag, "body": notes, "draft": true, "prerelease": false}, &r, false)
	return &r, err
}

// Upload sends raw binary data to the separate upload origin.
func (g *GitHub) Upload(release int64, name string, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/releases/%d/assets?%s", release, url.Values{"name": {name}}.Encode())
	_, err = g.do(http.MethodPost, "https://uploads.github.com", path, file, info.Size(), "application/octet-stream", nil, false)
	return err
}

func (g *GitHub) GetRelease(release int64) (*Release, error) {
	var r Release
	_, err := g.api(http.MethodGet, "/releases/"+strconv.FormatInt(release, 10), nil, &r, false)
	return &r, err
}

func (g *GitHub) MakeLatest(release int64) (*Release, error) {
	var r Release
	_, err := g.api(http.MethodPatch, "/releases/"+strconv.FormatInt(release, 10),
		map[string]any{"draft": false, "make_latest": "true"}, &r, false)
	return &r, err
}
