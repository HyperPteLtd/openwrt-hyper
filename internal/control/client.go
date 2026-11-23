// Package control speaks to the authenticated Mihomo External Controller on loopback.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

const Address = "127.0.0.1:19761"
const maxResponse = 8 << 20

type Client struct{ Secret string }

var transport = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("local control returned HTTP %d", int(e)) }
func (c Client) request(ctx context.Context, method, path string, payload any) (io.ReadCloser, error) {
	if len(c.Secret) < 16 {
		return nil, errors.New("missing local control credential")
	}
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://"+Address+path, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, e := transport.Do(req)
	if e != nil {
		return nil, fmt.Errorf("local control unavailable: %w", e)
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		r.Body.Close()
		return nil, statusError(r.StatusCode)
	}
	return r.Body, nil
}
func (c Client) call(ctx context.Context, method, path string, payload, result any) error {
	r, e := c.request(ctx, method, path, payload)
	if e != nil {
		return e
	}
	defer r.Close()
	if result == nil {
		return nil
	}
	b, e := io.ReadAll(io.LimitReader(r, maxResponse+1))
	if e != nil {
		return e
	}
	if len(b) > maxResponse {
		return errors.New("oversized local control response")
	}
	return json.Unmarshal(b, result)
}
func segment(name string) (string, error) {
	if name == "" || name == "." || name == ".." || len(name) > 1024 || strings.ContainsAny(name, "\x00\r\n") {
		return "", errors.New("invalid control object name")
	}
	return url.PathEscape(name), nil
}
func (c Client) Version(ctx context.Context) (string, error) {
	var r struct{ Version string }
	e := c.call(ctx, "GET", "/version", nil, &r)
	if e == nil && r.Version == "" {
		e = errors.New("empty core version")
	}
	return r.Version, e
}
func fromCore(s string) string {
	switch s {
	case "DIRECT":
		return "@direct"
	case "REJECT":
		return "@reject"
	}
	return s
}
func toCore(s string) string {
	switch s {
	case "@direct":
		return "DIRECT"
	case "@reject":
		return "REJECT"
	}
	return s
}

type Group struct {
	Type       string   `json:"type"`
	Selectable bool     `json:"selectable"`
	Tag        string   `json:"tag"`
	Selected   string   `json:"selected"`
	Members    []string `json:"members"`
}
type proxy struct {
	Type, Now string
	All       []string
	History   []struct {
		Time  time.Time
		Delay uint64
	}
}

func (c Client) proxies(ctx context.Context) (map[string]proxy, error) {
	var r struct{ Proxies map[string]proxy }
	e := c.call(ctx, "GET", "/proxies", nil, &r)
	if e == nil && r.Proxies == nil {
		e = errors.New("missing controller proxies")
	}
	return r.Proxies, e
}
func (c Client) Groups(ctx context.Context) ([]Group, error) {
	ps, e := c.proxies(ctx)
	if e != nil {
		return nil, e
	}
	groups := []Group{}
	for name, p := range ps {
		if name == "GLOBAL" {
			continue
		}
		kind := strings.ToLower(p.Type)
		if !slices.Contains([]string{"selector", "urltest", "fallback", "loadbalance", "relay"}, kind) {
			continue
		}
		for i, member := range p.All {
			p.All[i] = fromCore(member)
		}
		groups = append(groups, Group{Type: kind, Tag: name, Selected: fromCore(p.Now), Members: p.All, Selectable: kind == "selector"})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Tag < groups[j].Tag })
	return groups, nil
}
func (c Client) Select(ctx context.Context, group, node string) error {
	name, e := segment(group)
	if e != nil {
		return e
	}
	path := "/proxies/" + name
	if e = c.call(ctx, "PUT", path, map[string]string{"name": toCore(node)}, nil); e != nil {
		return e
	}
	var actual proxy
	if e = c.call(ctx, "GET", path, nil, &actual); e != nil {
		return e
	}
	if fromCore(actual.Now) != node {
		return errors.New("core did not confirm selection")
	}
	return nil
}

// ReloadPayload replaces the running configuration without recreating
// unchanged listeners.
func (c Client) ReloadPayload(ctx context.Context, config []byte) error {
	return c.call(ctx, "PUT", "/configs", map[string]string{"payload": string(config)}, nil)
}
func (c Client) RefreshRules(ctx context.Context, provider string) error {
	name, e := segment(provider)
	if e != nil {
		return e
	}
	return c.call(ctx, "PUT", "/providers/rules/"+name, nil, nil)
}

// CloseConnections closes every connection the core relays. Mihomo keeps open connections on
// the route they started with when its configuration or a selection changes, so clients close
// them to make the change take effect at once.
func (c Client) CloseConnections(ctx context.Context) error {
	return c.call(ctx, "DELETE", "/connections", nil, nil)
}

// CloseThrough closes the connections that pass through one group, as clients do after its
// selection changes.
func (c Client) CloseThrough(ctx context.Context, group string) error {
	var snapshot struct{ Connections []tracked }
	if e := c.call(ctx, "GET", "/connections", nil, &snapshot); e != nil {
		return e
	}
	var failed error
	for _, t := range snapshot.Connections {
		if t.ID != "" && slices.Contains(t.Chains, group) {
			if e := c.CloseConnection(ctx, t.ID); e != nil {
				failed = e
			}
		}
	}
	return failed
}

func (c Client) CloseConnection(ctx context.Context, id string) error {
	name, e := segment(id)
	if e != nil {
		return e
	}
	return c.call(ctx, "DELETE", "/connections/"+name, nil, nil)
}
