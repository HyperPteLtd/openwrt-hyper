package subscription

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// The receipt belongs to the current private snapshot. Scheduled refreshes still
// fetch the parent subscription so changes to nodes, policy or access take effect.
type providerReceipt struct {
	Key     string    `json:"key"`
	Checked time.Time `json:"checked"`
	Expires time.Time `json:"expires"`
}

func (r *providerReceipt) fresh(key string, now time.Time) bool {
	return r != nil && key == r.Key && !now.Before(r.Checked) && now.Before(r.Expires)
}

func providerSnapshotKey(src Source, body []byte) string {
	return digest([]string{"provider-import-v1", src.ID, src.URL, src.subscriptionAgent(), src.Rules, fmt.Sprintf("%x", sha256.Sum256(body))})
}

func providerLifetime(m map[string]any) time.Duration {
	ttl := 24 * time.Hour
	defs := object(m["rule-providers"])
	if len(defs) == 0 || len(object(m["proxy-providers"])) > 0 {
		return 0
	}
	for _, def := range defs {
		v := object(def)
		seconds, ok := integer(v["interval"])
		if field(v, "type") != "http" || !ok || seconds <= 0 {
			return 0
		}
		ttl = min(ttl, time.Duration(min(seconds, 86400))*time.Second)
	}
	return ttl
}
