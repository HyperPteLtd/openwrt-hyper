// Package main tests content negotiation and last-good retention using the production fetcher.
// URL and node material stay under the private local directory; stdout contains counts only.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

type capture struct {
	url     string
	status  int
	primary []byte
}

func (c *capture) RoundTrip(req *http.Request) (*http.Response, error) {
	r, e := http.DefaultTransport.RoundTrip(req)
	if e != nil || req.URL.String() != c.url {
		return r, e
	}
	c.status = r.StatusCode
	if r.StatusCode != http.StatusOK {
		return r, nil
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, subscription.MaxBody+1))
	r.Body.Close()
	if e != nil {
		return nil, e
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	c.primary = b
	return r, nil
}

var errFixture = errors.New("invalid private input or fixture state")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 3 || len(os.Args) > 20 {
		return errFixture
	}
	fi, e := os.Lstat(os.Args[1])
	if e != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return errFixture
	}
	b, e := os.ReadFile(os.Args[1])
	if e != nil {
		return errFixture
	}
	raw := strings.TrimSpace(string(b))
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return errFixture
	}
	dir, e := os.MkdirTemp(os.Args[2], "fetch-")
	if e != nil {
		return errFixture
	}
	defer os.RemoveAll(dir)
	store := subscription.OpenStore(filepath.Join(dir, "sources"))
	id := "external-qualification"
	if store.Configure(subscription.Source{ID: id, Name: "External qualification", Kind: "external", URL: raw}) != nil {
		return errFixture
	}
	var rows []map[string]any
	agents := subscription.UserAgents
	if len(os.Args) > 3 {
		agents = os.Args[3:]
	}
	seen := make(map[string]bool)
	for _, agent := range agents {
		if seen[agent] {
			continue
		}
		seen[agent] = true
		if e = store.Preferences(id, subscription.Preferences{UserAgent: agent}); e != nil {
			return errFixture
		}
		before, _ := store.Load(id)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		monitor := &capture{url: raw}
		f := subscription.Fetcher{Client: &http.Client{Transport: monitor, Timeout: 25 * time.Second}}
		c, e := f.Refresh(ctx, store, id, nil, subscription.Policy{})
		cancel()
		label := agent
		controls := []string{"sing-box", "sing-box/1.14.0", "sing-box/1.15.0-alpha.13", "clash", "clash.meta", "Clash", "Surge", "Loon", "Shadowrocket", "Quantumult X", "Hyper"}
		if !slices.Contains(controls, agent) {
			label = "custom"
		}
		row := map[string]any{"checked_utc": time.Now().UTC().Format(time.RFC3339), "primary_http": monitor.status, "user_agent": label, "decision": c.Decision, "prepared_nodes": len(c.Profile.Nodes)}
		if monitor.primary != nil {
			p, err := subscription.Parse("input", monitor.primary)
			row["raw_format"] = p.Format
			row["raw_parsed_nodes"] = len(p.Nodes)
			row["raw_parsed_groups"] = len(p.Groups)
			row["raw_parsed_rules"] = len(p.Rules)
			types := map[string]int{}
			for _, n := range p.Nodes {
				types[n.Protocol]++
			}
			row["raw_protocols"] = types
			notices := map[string]int{}
			for _, n := range p.Notices {
				notices[n.Code]++
			}
			row["raw_notices"] = notices
			if err != nil {
				row["raw_error"] = err.Error()
			}
		}
		if e != nil {
			row["error"] = e.Error()
		}
		after, _ := store.Load(id)
		row["saved_nodes"] = len(after.Nodes)
		row["format"] = after.Format
		row["saved_groups"] = len(after.Groups)
		row["saved_rules"] = len(after.Rules)
		row["prepared_groups"] = len(c.Profile.Groups)
		row["prepared_rules"] = len(c.Profile.Rules)
		if e != nil || c.Decision != "ready" {
			row["previous_preserved"] = reflect.DeepEqual(before, after)
		}
		counts := map[string]int{}
		for _, n := range c.Notices {
			counts[n.Code]++
		}
		row["notices"] = counts
		for _, n := range c.Notices {
			if n.Dangerous {
				row["dangerous_notice"] = true
			}
		}
		rows = append(rows, row)
	}
	out, _ := json.MarshalIndent(rows, "", "  ")
	fmt.Println(string(out))
	return nil
}
