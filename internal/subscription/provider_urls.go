package subscription

import "strings"

// Paths and queries are opaque to the router. Case folding either can change
// the object, the branch of a repository, or the signature in a query.
func providerAddress(raw string) string {
	u, err := webURL(raw, true)
	if err != nil {
		return raw // The downloader reports the failure with provider context.
	}
	authority, zone, scoped := strings.Cut(u.Host, "%")
	u.Host = strings.ToLower(authority)
	if scoped {
		u.Host += "%" + zone
	}
	return u.String()
}

func normalizeProviderAddresses(doc map[string]any) {
	for _, key := range []string{"proxy-providers", "rule-providers"} {
		for _, entry := range object(doc[key]) {
			p := object(entry)
			if raw := field(p, "url"); raw != "" {
				p["url"] = providerAddress(raw)
			}
		}
	}
}
