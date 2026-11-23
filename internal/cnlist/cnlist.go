// Package cnlist keeps the mainland China domain and address lists current. The plugin
// package pins one copy of each; plugin updates are manual, so a weekly download from the
// branch MetaCubeX rebuilds daily replaces it once the download passes Check.
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

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// The two lists, by the names of their packaged files.
const (
	Domains   = "geosite-cn"
	Addresses = "geoip-cn"
)

var (
	// Dir keeps accepted downloads with the other persistent data.
	Dir = "/etc/hyper/cnlists"
	// Packaged holds the copies pinned in SOURCES.
	Packaged = "/usr/share/hyper/rules"
)

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
// near-empty list never replaces a working one. The firewall sends the address ranges
// around the core, so they also have to pass the resolver probes.
func Check(list string, data []byte) error {
	s, ok := specs[list]
	if !ok || len(data) < s.minSize || len(data) > s.maxSize {
		return errInvalid
	}
	if n, err := subscription.MRSEntries(data, s.behavior); err != nil || n < s.minEntries {
		return errInvalid
	}
	if list != Addresses {
		return nil
	}
	ranges, err := subscription.MRSRanges(data)
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

// Path returns the file to read for list: the download while it passes Check and is newer
// than the packaged copy, else the packaged copy. A plugin update can ship a newer list
// than a download whose updates kept failing.
func Path(list string) string {
	if _, ok := downloaded(list); ok {
		return filepath.Join(Dir, list+".mrs")
	}
	return filepath.Join(Packaged, list+".mrs")
}

// Updated returns when the lists in use were downloaded, the older of the two, or zero
// while either is the packaged copy.
func Updated() time.Time {
	var oldest time.Time
	for _, list := range []string{Domains, Addresses} {
		mod, ok := downloaded(list)
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

// A download is checked once per file, not on each configuration build or status poll.
var verdicts = struct {
	sync.Mutex
	m map[string]verdict
}{m: map[string]verdict{}}

func downloaded(list string) (time.Time, bool) {
	f, err := os.Open(filepath.Join(Dir, list+".mrs"))
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return time.Time{}, false
	}
	if p, e := os.Stat(filepath.Join(Packaged, list+".mrs")); e == nil && p.ModTime().After(st.ModTime()) {
		return time.Time{}, false
	}
	verdicts.Lock()
	defer verdicts.Unlock()
	v, seen := verdicts.m[list]
	if !seen || !os.SameFile(v.file, st) || !v.file.ModTime().Equal(st.ModTime()) || v.file.Size() != st.Size() {
		data, e := io.ReadAll(io.LimitReader(f, int64(specs[list].maxSize)+1))
		v = verdict{st, e == nil && Check(list, data) == nil}
		verdicts.m[list] = v
	}
	return st.ModTime(), v.ok
}

const week = 7 * 24 * time.Hour

// Due reports whether to check the lists at now. A process waits a week after its last
// check that reached the source; after a start, the age of the copies in use decides
// (updated, zero for packaged ones). Failed checks retry after an hour, doubling to a day.
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

// Updater fetches the published lists over the router's normal route. It remembers each
// ETag, so an unchanged list costs a 304.
type Updater struct {
	client *http.Client
	urls   map[string]string
	etags  map[string]string
}

func NewUpdater() *Updater {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	u := &Updater{
		client: &http.Client{Transport: tr, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		urls:   map[string]string{}, etags: map[string]string{},
	}
	for list, s := range specs {
		u.urls[list] = s.url
	}
	return u
}

// Update fetches both lists and keeps each download that passes Check and differs from the
// copy in use. It returns the lists whose file changed and the first failure; one failed
// list does not hold back the other.
func (u *Updater) Update(ctx context.Context) (changed []string, err error) {
	defer u.client.CloseIdleConnections()
	for _, list := range []string{Domains, Addresses} {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.urls[list], nil)
	if err != nil {
		return false, err
	}
	if tag := u.etags[list]; tag != "" {
		req.Header.Set("If-None-Match", tag)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && u.etags[list] != "" {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s: HTTP %d", list, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(specs[list].maxSize)+1))
	if err != nil {
		return false, err
	}
	if err = Check(list, data); err != nil {
		return false, err
	}
	// Flash wear: only a different list is written.
	changed := true
	if current, e := os.ReadFile(Path(list)); e == nil && bytes.Equal(current, data) {
		changed = false
	} else if err = write(list, data); err != nil {
		return false, err
	}
	u.etags[list] = resp.Header.Get("ETag")
	return changed, nil
}

// write replaces a download in one rename, so the core and the firewall helper only ever
// read a complete file. Both read it under their own accounts.
// Not atomicfile: its fixed temporary name means a crash leaves at most one on flash, reused next time.
func write(list string, data []byte) error {
	if err := os.MkdirAll(Dir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(Dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(Dir, "."+list+".new")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(Dir, list+".mrs"))
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return atomicfile.SyncDir(Dir)
}
