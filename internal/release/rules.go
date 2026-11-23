package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const rulesRoot = `https://raw\.githubusercontent\.com/MetaCubeX/meta-rules-dat/[0-9a-f]{40}/`

var (
	ruleAssets = map[string]string{"geoip-cn.mrs": "geo/geoip/cn.mrs", "geosite-cn.mrs": "geo/geosite/cn.mrs"}
	geoData    = map[string]string{"GeoIP.dat": "geoip.dat", "GeoSite.dat": "geosite.dat", "country.mmdb": "country.mmdb", "ASN.mmdb": "GeoLite2-ASN.mmdb"}
	hexSHA     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type pin struct {
	name, sha, url string
	length         int
}

func readCatalog(path string, expected map[string]string) ([]pin, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pins []pin
	seen := map[string]bool{}
	for _, row := range splitLines(string(raw)) {
		f := strings.Fields(row)
		if len(f) != 4 {
			return nil, fmt.Errorf("invalid asset record in %s", filepath.Base(path))
		}
		length, err := strconv.Atoi(f[1])
		source, known := expected[f[2]]
		if err != nil || !known || seen[f[2]] || !hexSHA.MatchString(f[0]) || length <= 0 || length >= 64<<20 ||
			!regexp.MustCompile("^"+rulesRoot+regexp.QuoteMeta(source)+"$").MatchString(f[3]) {
			return nil, fmt.Errorf("invalid asset record for %s", f[2])
		}
		seen[f[2]] = true
		pins = append(pins, pin{f[2], f[0], f[3], length})
	}
	if len(seen) != len(expected) {
		return nil, fmt.Errorf("missing pins in %s", filepath.Base(path))
	}
	return pins, nil
}

// CheckRules verifies the pinned MetaCubeX rule assets in dir. With refresh, it downloads
// the pinned sources and rewrites the files instead of reading them.
func CheckRules(dir string, refresh bool) error {
	pinned, err := readCatalog(filepath.Join(dir, "CATALOG"), ruleAssets)
	if err != nil {
		return err
	}
	if _, err := readCatalog(filepath.Join(dir, "GEODATA"), geoData); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "SOURCES"))
	if err != nil {
		return err
	}
	urls := splitLines(string(raw))
	if len(urls) != len(pinned) {
		return fmt.Errorf("pinned source list mismatch")
	}
	for i, p := range pinned {
		if urls[i] != p.url {
			return fmt.Errorf("pinned source list mismatch")
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	for _, p := range pinned {
		path := filepath.Join(dir, p.name)
		var data []byte
		if refresh {
			resp, err := client.Get(p.url)
			if err != nil {
				return err
			}
			data, err = io.ReadAll(io.LimitReader(resp.Body, int64(p.length)+1))
			resp.Body.Close()
			if err != nil {
				return err
			}
		} else if data, err = os.ReadFile(path); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if len(data) != p.length || hex.EncodeToString(sum[:]) != p.sha {
			return fmt.Errorf("incorrect content for %s", p.name)
		}
		if refresh {
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
