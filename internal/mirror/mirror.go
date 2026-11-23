// Package mirror spreads GitHub downloads over GitHub itself and a user-edited list of
// accelerator prefixes ("https://example.org/" + the original URL). Every caller still
// verifies what it downloads against a size and SHA-256 learned elsewhere, so a mirror can
// make a download fail but never change what is installed.
package mirror

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

// Defaults are used until the list is saved from LuCI.
var Defaults = []string{"https://ghfast.top/", "https://gh-proxy.com/", "https://ghproxy.net/", "https://gh.idayer.com/", "https://cors.isteed.cc/", "https://ghm.078465.xyz/", "https://ghfile.geekertao.top/", "https://gh.ddlc.top/"}

// Hosts whose URLs may be fetched through a mirror.
var Hosts = map[string]bool{"github.com": true, "raw.githubusercontent.com": true}

// StateFile remembers the prefix ("" for GitHub itself) of the last successful download.
var StateFile = "/var/run/hyper/download-mirror"

// Limit bounds the number of configured prefixes.
const Limit = 8

// Valid reports whether prefix is an HTTPS URL prefix ending in "/" without credentials,
// query or fragment.
func Valid(prefix string) bool {
	u, err := url.Parse(prefix)
	return err == nil && len(prefix) <= 200 && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == "" && strings.HasSuffix(prefix, "/")
}

var run = func(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "uci", append([]string{"-q"}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// Configured returns the saved prefixes, or Defaults before the list was first saved.
func Configured(ctx context.Context) []string {
	if saved, _ := run(ctx, "get", "hyper.main.download_mirrors_saved"); saved != "1" {
		return Defaults
	}
	list, _ := run(ctx, "get", "hyper.main.download_mirror")
	var out []string
	for _, p := range strings.Fields(list) {
		p = strings.Trim(p, "'")
		if Valid(p) && len(out) < Limit {
			out = append(out, p)
		}
	}
	return out
}

// Candidate is one way to fetch a URL.
type Candidate struct {
	Prefix string // "" fetches the original URL
	URL    string
}

// RawCDNs serve repository files (not release assets) as https://<cdn>/gh/<owner>/<repo>@<ref>/<path>.
var RawCDNs = []string{"https://cdn.jsdmirror.com/gh/", "https://cdn.jsdmirror.cn/gh/", "https://fastly.jsdelivr.net/gh/", "https://gcore.jsdelivr.net/gh/", "https://cdn.jsdelivr.net/gh/"}

// Candidates lists the ways to fetch target: the remembered prefix first, then GitHub,
// the configured prefixes in order, and for raw.githubusercontent.com files the CDNs above.
// URLs outside Hosts are only fetched directly.
func Candidates(target string, prefixes []string) []Candidate {
	u, err := url.Parse(target)
	if err != nil || !Hosts[u.Hostname()] {
		return []Candidate{{URL: target}}
	}
	out := []Candidate{{URL: target}}
	for _, p := range prefixes {
		out = append(out, Candidate{Prefix: p, URL: p + target})
	}
	if u.Hostname() == "raw.githubusercontent.com" {
		// /<owner>/<repo>/<ref>/<path>
		if parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 4); len(parts) == 4 && u.RawQuery == "" {
			for _, cdn := range RawCDNs {
				out = append(out, Candidate{Prefix: cdn, URL: cdn + parts[0] + "/" + parts[1] + "@" + parts[2] + "/" + parts[3]})
			}
		}
	}
	if last := Preferred(); last != "" {
		for i, c := range out {
			if c.Prefix == last {
				out = append([]Candidate{c}, append(out[:i:i], out[i+1:]...)...)
				break
			}
		}
	}
	return out
}

// Preferred returns the prefix that last worked.
func Preferred() string {
	b, err := os.ReadFile(StateFile)
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if p != "" && !Valid(p) {
		return ""
	}
	return p
}

// Remember records the prefix that just worked, so later downloads start with it.
func Remember(prefix string) {
	if prefix == Preferred() {
		return
	}
	if os.MkdirAll(filepath.Dir(StateFile), 0o755) != nil {
		return
	}
	_ = atomicfile.WriteFile(StateFile, []byte(prefix+"\n"), 0o644, "."+filepath.Base(StateFile)+"-*")
}
