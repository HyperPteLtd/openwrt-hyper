package cnlist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func bundled(t *testing.T, list string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../files/usr/share/hyper/rules/" + list + ".mrs")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// mrs builds a structurally valid rule set whose header claims count entries.
func mrs(t *testing.T, behavior byte, count uint64, blocks ...[]byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	raw.WriteString("MRS\x01")
	raw.WriteByte(behavior)
	binary.Write(&raw, binary.BigEndian, count)
	binary.Write(&raw, binary.BigEndian, uint64(0)) // no extra block
	raw.WriteByte(1)
	sizes := map[byte][]int{0: {8, 8, 1}, 1: {32}}[behavior]
	for i, b := range blocks {
		binary.Write(&raw, binary.BigEndian, uint64(len(b)/sizes[i]))
		raw.Write(b)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	return enc.EncodeAll(raw.Bytes(), nil)
}

func ranges(spans ...string) []byte {
	var b []byte
	for _, s := range spans {
		from, to, _ := strings.Cut(s, "-")
		f, l := netip.MustParseAddr(from).As16(), netip.MustParseAddr(to).As16()
		b = append(append(b, f[:]...), l[:]...)
	}
	return b
}

// Small lists pass the size bounds only where a test lowers them.
func relaxSizes(t *testing.T) {
	saved := map[string]spec{}
	for list, s := range specs {
		saved[list] = s
		s.minSize = 0
		specs[list] = s
	}
	t.Cleanup(func() {
		for list, s := range saved {
			specs[list] = s
		}
	})
}

var (
	mainland = []string{"114.0.0.0-114.255.255.255", "223.0.0.0-223.255.255.255", "2400:3200::-2400:3200:ffff:ffff:ffff:ffff:ffff:ffff"}
	domains  = [][]byte{make([]byte, 8), make([]byte, 8), {1}}
)

func TestCheck(t *testing.T) {
	site, ip := bundled(t, Domains), bundled(t, Addresses)
	if err := Check(Domains, site); err != nil {
		t.Fatal("packaged domains rejected", err)
	}
	if err := Check(Addresses, ip); err != nil {
		t.Fatal("packaged addresses rejected", err)
	}
	page := []byte(strings.Repeat("<html><body>404: Not Found</body></html>\n", 4000))
	for name, c := range map[string]struct {
		list string
		data []byte
	}{
		"wrong behavior": {Addresses, site},
		"truncated":      {Domains, site[:len(site)/2]},
		"error page":     {Domains, page},
		"too large":      {Addresses, append(slices.Clone(ip), make([]byte, 2<<20)...)},
		"too small":      {Addresses, ip[:4<<10]},
		"unknown list":   {"geosite-us", site},
	} {
		if Check(c.list, c.data) == nil {
			t.Error(name, "accepted")
		}
	}
	relaxSizes(t)
	if err := Check(Addresses, mrs(t, 1, 5000, ranges(mainland...))); err != nil {
		t.Fatal("plausible addresses rejected", err)
	}
	if err := Check(Domains, mrs(t, 0, 60000, domains...)); err != nil {
		t.Fatal("plausible domains rejected", err)
	}
	for name, c := range map[string]struct {
		list string
		data []byte
	}{
		"few addresses":    {Addresses, mrs(t, 1, 300, ranges(mainland...))},
		"few domains":      {Domains, mrs(t, 0, 3000, domains...)},
		"everything":       {Addresses, mrs(t, 1, 5000, ranges("0.0.0.0-255.255.255.255", mainland[2]))},
		"missing resolver": {Addresses, mrs(t, 1, 5000, ranges(mainland[0], mainland[2]))},
		"foreign v6":       {Addresses, mrs(t, 1, 5000, ranges(append(mainland, "2001:4860::-2001:4860:ffff:ffff:ffff:ffff:ffff:ffff")...))},
	} {
		if Check(c.list, c.data) == nil {
			t.Error(name, "accepted")
		}
	}
}

// lists points Dir and Packaged at fresh directories holding the packaged copies, dated
// a month ago.
func lists(t *testing.T) (packaged time.Time) {
	t.Helper()
	savedDir, savedPackaged := Dir, Packaged
	t.Cleanup(func() { Dir, Packaged = savedDir, savedPackaged })
	Dir, Packaged = filepath.Join(t.TempDir(), "cnlists"), t.TempDir()
	packaged = time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	for _, list := range []string{Domains, Addresses} {
		file := filepath.Join(Packaged, list+".mrs")
		if err := os.WriteFile(file, bundled(t, list), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, packaged, packaged); err != nil {
			t.Fatal(err)
		}
	}
	return packaged
}

func download(t *testing.T, list string, data []byte, mod time.Time) {
	t.Helper()
	if err := write(list, data); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(Dir, list+".mrs"), mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestPath(t *testing.T) {
	packaged := lists(t)
	if Path(Domains) != filepath.Join(Packaged, "geosite-cn.mrs") || !Updated().IsZero() {
		t.Fatal("no download must read the packaged copy", Path(Domains), Updated())
	}
	week := packaged.Add(7 * 24 * time.Hour)
	download(t, Domains, bundled(t, Domains), week)
	if Path(Domains) != filepath.Join(Dir, "geosite-cn.mrs") || Path(Addresses) != filepath.Join(Packaged, "geoip-cn.mrs") {
		t.Fatal("a valid download is read", Path(Domains), Path(Addresses))
	}
	if !Updated().IsZero() {
		t.Fatal("a packaged list still in use dates both", Updated())
	}
	download(t, Addresses, bundled(t, Addresses), week.Add(time.Hour))
	if !Updated().Equal(week) {
		t.Fatal("the older download dates both", Updated())
	}
	// A replaced file is checked again even with the same size and date, and a broken one
	// falls back.
	broken := bundled(t, Addresses)
	clear(broken[20000:])
	download(t, Addresses, broken, week.Add(time.Hour))
	if Path(Addresses) != filepath.Join(Packaged, "geoip-cn.mrs") || !Updated().IsZero() {
		t.Fatal("an invalid download is ignored", Path(Addresses))
	}
	// A plugin update with a newer packaged list wins over an older download.
	if err := os.Chtimes(filepath.Join(Packaged, "geosite-cn.mrs"), week.Add(time.Minute), week.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if Path(Domains) != filepath.Join(Packaged, "geosite-cn.mrs") {
		t.Fatal("a newer packaged list is read", Path(Domains))
	}
	if st, err := os.Stat(Dir); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatal("the core reads the downloads under its own account", st.Mode(), err)
	}
	if st, err := os.Stat(filepath.Join(Dir, "geosite-cn.mrs")); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatal("the core reads the downloads under its own account", st.Mode(), err)
	}
}

func TestDue(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	var zero time.Time
	for _, c := range []struct {
		name                      string
		updated, checked, attempt time.Time
		failures                  int
		want                      bool
	}{
		{"packaged copy after a start", zero, zero, zero, 0, true},
		{"recent download after a start", now.Add(-3 * day), zero, zero, 0, false},
		{"week-old download after a start", now.Add(-8 * day), zero, zero, 0, true},
		{"download dated in the future", now.Add(30 * day), zero, zero, 0, true},
		{"checked yesterday, unchanged for a month", now.Add(-30 * day), now.Add(-day), now.Add(-day), 0, false},
		{"checked a week ago", now.Add(-30 * day), now.Add(-7 * day), now.Add(-7 * day), 0, true},
		{"first retry too soon", zero, zero, now.Add(-30 * time.Minute), 1, false},
		{"first retry", zero, zero, now.Add(-61 * time.Minute), 1, true},
		{"third retry too soon", zero, zero, now.Add(-3 * time.Hour), 3, false},
		{"third retry", zero, zero, now.Add(-5 * time.Hour), 3, true},
		{"retries cap at a day", zero, zero, now.Add(-23 * time.Hour), 40, false},
		{"daily retry", zero, zero, now.Add(-25 * time.Hour), 40, true},
		{"retry still waits for the week", now.Add(-2 * day), zero, now.Add(-2 * time.Hour), 1, false},
	} {
		if got := Due(now, c.updated, c.checked, c.attempt, c.failures); got != c.want {
			t.Error(c.name, got)
		}
	}
}

func TestUpdate(t *testing.T) {
	lists(t)
	relaxSizes(t)
	// The packaged copies are older and smaller than what the source publishes.
	for list, data := range map[string][]byte{Domains: mrs(t, 0, 60000, domains...), Addresses: mrs(t, 1, 5000, ranges(mainland...))} {
		if err := os.WriteFile(filepath.Join(Packaged, list+".mrs"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-30 * 24 * time.Hour)
		os.Chtimes(filepath.Join(Packaged, list+".mrs"), old, old)
	}
	served := map[string][]byte{"/geosite": bundled(t, Domains), "/geoip": bundled(t, Addresses)}
	var conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := served[r.URL.Path]
		tag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))
		if r.Header.Get("If-None-Match") == tag {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
		w.Write(data)
	}))
	defer srv.Close()
	u := NewUpdater()
	u.urls = map[string]string{Domains: srv.URL + "/geosite", Addresses: srv.URL + "/geoip"}
	changed, err := u.Update(context.Background())
	if err != nil || !slices.Equal(changed, []string{Domains, Addresses}) || Path(Addresses) != filepath.Join(Dir, "geoip-cn.mrs") {
		t.Fatal("newer lists are kept", changed, err)
	}
	if changed, err = u.Update(context.Background()); err != nil || len(changed) != 0 || conditional != 2 {
		t.Fatal("unchanged lists are not fetched again", changed, err, conditional)
	}
	// A check that fails leaves the list in use alone and does not stop the other list.
	before, _ := os.Stat(filepath.Join(Dir, "geosite-cn.mrs"))
	served["/geosite"] = []byte(strings.Repeat("<html>rate limited</html>", 10000))
	served["/geoip"] = mrs(t, 1, 5000, ranges(append(mainland, "8.8.8.0-8.8.8.255")...))
	if changed, err = u.Update(context.Background()); err == nil || len(changed) != 0 {
		t.Fatal("invalid lists are refused", changed, err)
	}
	after, _ := os.Stat(filepath.Join(Dir, "geosite-cn.mrs"))
	if !os.SameFile(before, after) || Path(Domains) != filepath.Join(Dir, "geosite-cn.mrs") {
		t.Fatal("a refused download replaced the list in use")
	}
	// Flash wear: the same bytes under a new ETag are not written again.
	served["/geosite"], served["/geoip"] = bundled(t, Domains), bundled(t, Addresses)
	u.etags = map[string]string{}
	if changed, err = u.Update(context.Background()); err != nil || len(changed) != 0 {
		t.Fatal("identical lists are not rewritten", changed, err)
	}
	if now, _ := os.Stat(filepath.Join(Dir, "geosite-cn.mrs")); !os.SameFile(before, now) {
		t.Fatal("an identical list was rewritten")
	}
	if entries, _ := os.ReadDir(Dir); len(entries) != 2 {
		t.Fatal("temporary files left behind", entries)
	}
}
