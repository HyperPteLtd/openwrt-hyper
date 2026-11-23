package subscription

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mirror"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type geoPin struct {
	digest    string
	size      int64
	name, url string
}

var geoURL = regexp.MustCompile(`^https://raw\.githubusercontent\.com/MetaCubeX/meta-rules-dat/[0-9a-f]{40}/(geoip\.dat|geosite\.dat|country\.mmdb|GeoLite2-ASN\.mmdb)$`)

func pinnedFile(path string, p geoPin) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != p.size {
		return false
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, p.size+1))
	return e == nil && n == p.size && hex.EncodeToString(h.Sum(nil)) == p.digest
}

// PrepareGeodata acquires only the full datasets referenced by the generated
// rules. geoip-mode is DAT; MMDB is also supported for callers using MMDB mode.
func PrepareGeodata(ctx context.Context, rules []string, dir, manifest string) error {
	needed := map[string]bool{}
	for _, rule := range rules {
		r := "(" + strings.ToUpper(rule)
		if strings.Contains(r, "(GEOSITE,") {
			needed["GeoSite.dat"] = true
		}
		if strings.Contains(r, "(GEOIP,") || strings.Contains(r, "(SRC-GEOIP,") {
			needed["GeoIP.dat"] = true
		}
		if strings.Contains(r, "(IP-ASN,") || strings.Contains(r, "(SRC-IP-ASN,") {
			needed["ASN.mmdb"] = true
		}
		if rule == "country.mmdb" {
			needed["country.mmdb"] = true
		}
	}
	if len(needed) == 0 {
		return nil
	}
	f, e := os.Open(manifest)
	if e != nil {
		return e
	}
	defer f.Close()
	pins := map[string]geoPin{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var p geoPin
		if n, e := fmt.Sscan(sc.Text(), &p.digest, &p.size, &p.name, &p.url); n != 4 || e != nil {
			return errRulePayload
		}
		m := geoURL.FindStringSubmatch(p.url)
		h, e := hex.DecodeString(p.digest)
		if e != nil || len(h) != 32 || len(m) != 2 || (m[1] != strings.ToLower(p.name) && !(p.name == "ASN.mmdb" && m[1] == "GeoLite2-ASN.mmdb")) || p.size < 1 || p.size > 64<<20 {
			return errRulePayload
		}
		if _, dup := pins[p.name]; dup {
			return errRulePayload
		}
		pins[p.name] = p
	}
	if e = sc.Err(); e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	tr := publicTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	for _, name := range []string{"GeoIP.dat", "GeoSite.dat", "country.mmdb", "ASN.mmdb"} {
		if !needed[name] {
			continue
		}
		p, ok := pins[name]
		if !ok {
			return errRulePayload
		}
		dst := filepath.Join(dir, name)
		if pinnedFile(dst, p) {
			continue
		}
		if err := acquireGeo(ctx, client, dst, p); err != nil {
			return err
		}
	}
	return nil
}

// acquireGeo tries GitHub and the download mirrors in turn; each copy must match the pin.
func acquireGeo(ctx context.Context, c *http.Client, dst string, p geoPin) error {
	var first error
	for _, m := range mirror.Candidates(p.url, mirror.Configured(ctx)) {
		err := acquireGeoFrom(ctx, c, dst, p, m.URL)
		if err == nil {
			mirror.Remember(m.Prefix)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if first == nil {
			first = err
		}
	}
	return first
}
func acquireGeoFrom(ctx context.Context, c *http.Client, dst string, p geoPin, address string) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if e != nil {
		return e
	}
	r, e := c.Do(req)
	if e != nil {
		return errors.New("geodata_download_failed")
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return errors.New("geodata_download_failed")
	}
	return atomicfile.Write(dst, 0644, ".geodata-*", func(f *os.File) error {
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, p.size+1))
		if e != nil || n != p.size || hex.EncodeToString(h.Sum(nil)) != p.digest {
			return errors.New("geodata_digest_mismatch")
		}
		return nil
	})
}
