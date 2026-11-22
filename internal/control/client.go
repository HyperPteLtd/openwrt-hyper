// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package control talks to mihomo's external controller on loopback.
//
// Only the endpoints hyper needs are wrapped: version, selection, delay, connections
// and rule provider refresh.
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
	"strconv"
	"strings"
	"time"
)

// DefaultAddr is hyper.infra.controller_port's default.
const DefaultAddr = "127.0.0.1:5334"

const maxBody = 8 << 20

// ErrUnavailable means the controller could not be reached (core not running).
var ErrUnavailable = errors.New("core controller unavailable")

// StatusError is a non-2xx controller reply.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("core controller: HTTP %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("core controller: HTTP %d", e.Code)
}

// Client is safe for concurrent use.
type Client struct {
	Addr   string
	Secret string
	HTTP   *http.Client
}

func New(addr, secret string) *Client {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Client{Addr: addr, Secret: secret, HTTP: &http.Client{
		Transport:     &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) do(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Addr+path, body)
	if err != nil {
		return nil, err
	}
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		defer res.Body.Close()
		var msg struct{ Message string }
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = json.Unmarshal(b, &msg)
		return nil, &StatusError{Code: res.StatusCode, Message: msg.Message}
	}
	return res, nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	res, err := c.do(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxBody))
		return nil
	}
	// Decode while reading: a busy router's connection list is hundreds of kilobytes and
	// is polled every few seconds, so it is never held whole in memory.
	lim := &io.LimitedReader{R: res.Body, N: maxBody + 1}
	if err := json.NewDecoder(lim).Decode(out); err != nil {
		if lim.N <= 0 {
			return errors.New("core controller: response too large")
		}
		return err
	}
	return nil
}

// name escapes one path segment (group or proxy name).
func name(s string) (string, error) {
	if s == "" || len(s) > 512 || strings.ContainsAny(s, "\x00\r\n") {
		return "", errors.New("invalid proxy name")
	}
	return url.PathEscape(s), nil
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct{ Version string }
	if err := c.call(ctx, "GET", "/version", nil, &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", errors.New("core controller: empty version")
	}
	return v.Version, nil
}

// Proxy is one entry of GET /proxies (a node or a group).
type Proxy struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Now  string `json:"now,omitempty"`
}

func (c *Client) Proxy(ctx context.Context, proxy string) (Proxy, error) {
	var p Proxy
	seg, err := name(proxy)
	if err != nil {
		return p, err
	}
	err = c.call(ctx, "GET", "/proxies/"+seg, nil, &p)
	return p, err
}

// Select switches a selector group and confirms the core applied it.
func (c *Client) Select(ctx context.Context, group, member string) error {
	seg, err := name(group)
	if err != nil {
		return err
	}
	if err = c.call(ctx, "PUT", "/proxies/"+seg, map[string]string{"name": member}, nil); err != nil {
		return err
	}
	p, err := c.Proxy(ctx, group)
	if err != nil {
		return err
	}
	if p.Now != member {
		return fmt.Errorf("core kept %q selected in %s", p.Now, group)
	}
	return nil
}

// ErrDelayFailed is a probe that timed out or could not connect.
var ErrDelayFailed = errors.New("delay test failed")

// Delay measures one proxy or group against testURL; timeout is passed to the core.
func (c *Client) Delay(ctx context.Context, proxy, testURL string, timeout time.Duration) (uint64, error) {
	seg, err := name(proxy)
	if err != nil {
		return 0, err
	}
	q := url.Values{"url": {testURL}, "timeout": {strconv.FormatInt(timeout.Milliseconds(), 10)}}
	var v struct{ Delay uint64 }
	err = c.call(ctx, "GET", "/proxies/"+seg+"/delay?"+q.Encode(), nil, &v)
	var se *StatusError
	if errors.As(err, &se) && (se.Code == 408 || se.Code == 503 || se.Code == 504) {
		return 0, ErrDelayFailed
	}
	if err == nil && v.Delay == 0 {
		return 0, ErrDelayFailed
	}
	return v.Delay, err
}

// Connection is the part of GET /connections hyper reports.
type Connection struct {
	ID       string    `json:"id"`
	Upload   uint64    `json:"upload"`
	Download uint64    `json:"download"`
	Start    time.Time `json:"start"`
	Chains   []string  `json:"chains"`
	Rule     string    `json:"rule"`
	Metadata struct {
		Network         string `json:"network"`
		SourceIP        string `json:"sourceIP"`
		SourcePort      string `json:"sourcePort"`
		DestinationIP   string `json:"destinationIP"`
		DestinationPort string `json:"destinationPort"`
		Host            string `json:"host"`
	} `json:"metadata"`
}

type Connections struct {
	DownloadTotal uint64       `json:"downloadTotal"`
	UploadTotal   uint64       `json:"uploadTotal"`
	Memory        uint64       `json:"memory"`
	Connections   []Connection `json:"connections"`
}

// Totals reads only the byte counters and memory of GET /connections; the connection
// list (hundreds of entries on a busy router) is scanned but not allocated.
func (c *Client) Totals(ctx context.Context) (Connections, error) {
	var v struct {
		DownloadTotal uint64 `json:"downloadTotal"`
		UploadTotal   uint64 `json:"uploadTotal"`
		Memory        uint64 `json:"memory"`
	}
	err := c.call(ctx, "GET", "/connections", nil, &v)
	return Connections{DownloadTotal: v.DownloadTotal, UploadTotal: v.UploadTotal, Memory: v.Memory}, err
}

func (c *Client) Connections(ctx context.Context) (Connections, error) {
	var v Connections
	err := c.call(ctx, "GET", "/connections", nil, &v)
	return v, err
}

// CloseThrough closes the connections that pass through one group. The core keeps open
// connections on the node they started with, so clients close them after the group's
// selection changes and the new node takes over at once.
func (c *Client) CloseThrough(ctx context.Context, group string) error {
	var v struct {
		Connections []struct {
			ID     string   `json:"id"`
			Chains []string `json:"chains"`
		} `json:"connections"`
	}
	if err := c.call(ctx, "GET", "/connections", nil, &v); err != nil {
		return err
	}
	var failed error
	for _, conn := range v.Connections {
		if conn.ID == "" || !slices.Contains(conn.Chains, group) {
			continue
		}
		id, err := name(conn.ID)
		if err == nil {
			err = c.call(ctx, "DELETE", "/connections/"+id, nil, nil)
		}
		var se *StatusError
		if err != nil && !(errors.As(err, &se) && se.Code == 404) { // already closed
			failed = err
		}
	}
	return failed
}

// RefreshRules makes the core re-read one rule provider (device address lists).
func (c *Client) RefreshRules(ctx context.Context, provider string) error {
	n, err := name(provider)
	if err != nil {
		return err
	}
	return c.call(ctx, "PUT", "/providers/rules/"+n, nil, nil)
}
