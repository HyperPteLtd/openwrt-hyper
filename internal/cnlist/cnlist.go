// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package cnlist keeps the mainland China domain and address lists current.
// The package ships one pinned copy of each; plugin updates are manual, so a
// weekly download from the branch MetaCubeX rebuilds daily replaces it once the download
// passes Check.
package cnlist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// The two lists, by the names of their packaged files (and the core's rule providers).
const (
	Domains   = "geosite-cn"
	Addresses = "geoip-cn"
)

// Names lists both, domains first.
var Names = []string{Domains, Addresses}

type spec struct {
	url, behavior    string
	minSize, maxSize int
	minEntries       int
}

// Bounds sit around a quarter and sixteen times today's size, and half today's entries
// (532 KB and 110k domains, 38 KB and 9.7k networks).
var specs = map[string]spec{
	Domains:   {"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/cn.mrs", "domain", 128 << 10, 8 << 20, 50000},
	Addresses: {"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geoip/cn.mrs", "ipcidr", 8 << 10, 2 << 20, 4000},
}

// Mainland public resolvers must stay in the address list and foreign ones out.
var addressProbes = map[string]bool{
	"114.114.114.114": true, "223.5.5.5": true, "2400:3200::1": true,
	"8.8.8.8": false, "1.1.1.1": false, "2001:4860:4860::8888": false,
}

var errInvalid = errors.New("invalid China list")

// Check accepts data as list only when it is a complete MRS file of the list's behavior
// with a plausible size and entry count, so an error page, a truncated download or a
// near-empty list never replaces a working one. The address list must also place the
// probe resolvers on the right side.
func Check(list string, data []byte) error {
	s, ok := specs[list]
	if !ok || len(data) < s.minSize || len(data) > s.maxSize {
		return errInvalid
	}
	if n, err := mrsEntries(data, s.behavior); err != nil || n < s.minEntries {
		return errInvalid
	}
	if list != Addresses {
		return nil
	}
	ranges, err := mrsRanges(data)
	if err != nil {
		return errInvalid
	}
	for probe, want := range addressProbes {
		ip, found := netip.MustParseAddr(probe), false
		for _, r := range ranges {
			if found = r[0].Compare(ip) <= 0 && ip.Compare(r[1]) <= 0; found {
				break
			}
		}
		if found != want {
			return errInvalid
		}
	}
	return nil
}

// Lists are the two copies of each list: the accepted downloads and the packaged ones.
type Lists struct {
	Dir      string // accepted downloads, kept across reboots
	Packaged string // the pinned copies in the package
}

// Path returns the file to read for list: the download while it passes Check and is newer
// than the packaged copy, else the packaged copy (a plugin update may ship a newer list
// than a download whose updates kept failing).
func (l Lists) Path(list string) string {
	if _, ok := l.downloaded(list); ok {
		return filepath.Join(l.Dir, list+".mrs")
	}
	return filepath.Join(l.Packaged, list+".mrs")
}

// Updated returns when the lists in use were downloaded, the older of the two, or zero
// while either is the packaged copy.
func (l Lists) Updated() time.Time {
	var oldest time.Time
	for _, list := range Names {
		mod, ok := l.downloaded(list)
		if !ok {
			return time.Time{}
		}
		if oldest.IsZero() || mod.Before(oldest) {
			oldest = mod
		}
	}
	return oldest
}

type verdict struct {
	file os.FileInfo
	ok   bool
}

// A download is checked once per file, not on every config render.
var verdicts = struct {
	sync.Mutex
	m map[string]verdict
}{m: map[string]verdict{}}

func (l Lists) downloaded(list string) (time.Time, bool) {
	path := filepath.Join(l.Dir, list+".mrs")
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return time.Time{}, false
	}
	if p, err := os.Stat(filepath.Join(l.Packaged, list+".mrs")); err == nil && p.ModTime().After(st.ModTime()) {
		return time.Time{}, false
	}
	verdicts.Lock()
	defer verdicts.Unlock()
	v, seen := verdicts.m[path]
	if !seen || !os.SameFile(v.file, st) || !v.file.ModTime().Equal(st.ModTime()) || v.file.Size() != st.Size() {
		data, err := io.ReadAll(io.LimitReader(f, int64(specs[list].maxSize)+1))
		v = verdict{st, err == nil && Check(list, data) == nil}
		verdicts.m[path] = v
	}
	return st.ModTime(), v.ok
}

const week = 7 * 24 * time.Hour

// Due reports whether to check the lists at now. After a check that reached the source the
// next waits a week; after a start, the age of the copies in use decides (updated, zero for
// packaged ones). Failed checks retry after an hour, doubling to a day.
func Due(now, updated, checked, attempt time.Time, failures int) bool {
	if failures > 0 {
		wait := time.Hour
		for i := 1; i < failures && wait < 24*time.Hour; i++ {
			wait = min(2*wait, 24*time.Hour)
		}
		if now.Sub(attempt) < wait {
			return false
		}
	}
	last := checked
	if last.IsZero() {
		last = updated
	}
	// A copy dated in the future was written under a wrong clock.
	return last.IsZero() || last.After(now) || now.Sub(last) >= week
}

// Updater fetches the published lists. It remembers each ETag, so an unchanged list costs
// a 304.
type Updater struct {
	Lists Lists
	// HTTP builds a client for a proxy URL ("" = direct).
	HTTP func(proxy string) *http.Client
	// Proxy is the core's loopback proxy, "" while it is not running. A download that
	// fails directly (GitHub is often unreachable from the mainland) retries through it.
	Proxy func() string
	// URLs overrides the sources (tests).
	URLs  map[string]string
	etags map[string]string
}

// Update fetches both lists and keeps each download that passes Check and differs from the
// copy in use. It returns the lists whose file changed and the first failure; one failed
// list does not hold back the other.
func (u *Updater) Update(ctx context.Context) (changed []string, err error) {
	if u.etags == nil {
		u.etags = map[string]string{}
	}
	for _, list := range Names {
		ok, e := u.update(ctx, list)
		if ok {
			changed = append(changed, list)
		}
		if err == nil {
			err = e
		}
	}
	return changed, err
}

func (u *Updater) update(ctx context.Context, list string) (bool, error) {
	url := specs[list].url
	if v := u.URLs[list]; v != "" {
		url = v
	}
	data, notModified, err := u.fetch(ctx, list, url, "")
	if err != nil && u.Proxy != nil {
		if proxy := u.Proxy(); proxy != "" {
			data, notModified, err = u.fetch(ctx, list, url, proxy)
		}
	}
	if err != nil || notModified {
		return false, err
	}
	if err = Check(list, data); err != nil {
		return false, fmt.Errorf("%s: %w", list, err)
	}
	// Flash wear: only a different list is written.
	changed := true
	if current, e := os.ReadFile(u.Lists.Path(list)); e == nil && bytes.Equal(current, data) {
		changed = false
	} else if err = u.Lists.write(list, data); err != nil {
		return false, err
	}
	return changed, nil
}

func (u *Updater) fetch(ctx context.Context, list, url, proxy string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	if tag := u.etags[list]; tag != "" {
		req.Header.Set("If-None-Match", tag)
	}
	client := u.HTTP(proxy)
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && u.etags[list] != "" {
		return nil, true, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("%s: HTTP %d", list, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, int64(specs[list].maxSize)+1))
	if err == nil {
		u.etags[list] = res.Header.Get("ETag")
	}
	return data, false, err
}

// write replaces a download in one rename, so the core only ever reads a complete file.
// The core runs as root, so the copies stay root's own.
func (l Lists) write(list string, data []byte) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(l.Dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(l.Dir, "."+list+".new")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(l.Dir, list+".mrs"))
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	d, err := os.Open(l.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
