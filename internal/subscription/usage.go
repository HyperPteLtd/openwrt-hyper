package subscription

import (
	"strconv"
	"strings"
	"time"
)

// Only numeric counters are accepted from the optional subscription-userinfo
// header. Other response headers can carry session cookies or private URLs.
type Usage struct {
	Upload   uint64  `json:"upload"`
	Download uint64  `json:"download"`
	Total    *uint64 `json:"total,omitempty"`
	Expires  string  `json:"expireAt,omitempty"`
	NoExpiry bool    `json:"no_expiry,omitempty"`
}

func parseUsage(raw string) *Usage {
	if raw == "" || len(raw) > 1024 {
		return nil
	}
	vals := map[string]uint64{}
	for _, item := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case "upload", "download", "total", "expire":
		default:
			continue
		}
		n, e := strconv.ParseUint(strings.TrimSpace(value), 10, 53)
		if e != nil {
			return nil
		}
		if _, seen := vals[name]; seen {
			return nil
		}
		vals[name] = n
	}
	up, uok := vals["upload"]
	down, dok := vals["download"]
	if !uok || !dok {
		return nil
	}
	info := &Usage{Upload: up, Download: down}
	if total, ok := vals["total"]; ok && total > 0 {
		info.Total = &total
	}
	if expire, ok := vals["expire"]; ok {
		if expire == 0 {
			info.NoExpiry = true
		} else if expire <= 253402300799 {
			info.Expires = time.Unix(int64(expire), 0).UTC().Format(time.RFC3339)
		}
	}
	return info
}
