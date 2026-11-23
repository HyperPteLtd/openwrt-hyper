package subscription

import (
	"strings"
)

// A custom geodata feed can assign a different meaning to the same tag.
func clashDefaultGeo(doc map[string]any, kind string) bool {
	files := object(doc["geox-url"])
	keys := []string{strings.ToLower(kind)}
	switch strings.ToUpper(kind) {
	case "IP-ASN":
		keys = []string{"asn"}
	case "GEOIP":
		keys = []string{"geoip", "mmdb"}
	}
	names := map[string]string{"asn": "GeoLite2-ASN.mmdb", "geoip": "geoip.dat", "geosite": "geosite.dat", "mmdb": "country.mmdb"}
	for _, key := range keys {
		if _, present := files[key]; !present {
			continue
		}
		u, err := webURL(field(files, key), true)
		if err != nil || u.RawQuery != "" || u.Port() != "" {
			return false
		}
		name := names[key]
		switch strings.ToLower(u.Hostname()) {
		case "github.com":
			if u.EscapedPath() != "/MetaCubeX/meta-rules-dat/releases/download/latest/"+name {
				return false
			}
		case "raw.githubusercontent.com":
			if u.EscapedPath() != "/MetaCubeX/meta-rules-dat/release/"+name && u.EscapedPath() != "/MetaCubeX/meta-rules-dat/refs/heads/release/"+name {
				return false
			}
		case "cdn.jsdelivr.net", "testingcf.jsdelivr.net", "fastly.jsdelivr.net", "gcore.jsdelivr.net":
			if u.EscapedPath() != "/gh/MetaCubeX/meta-rules-dat@release/"+name {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func geoRule(line string) (kind, name, policy string, ok bool) {
	p := strings.Split(line, ",")
	if len(p) != 3 && !(len(p) == 4 && strings.EqualFold(strings.TrimSpace(p[3]), "no-resolve")) {
		return
	}
	kind = strings.ToUpper(strings.TrimSpace(p[0]))
	if kind != "GEOIP" && kind != "GEOSITE" || kind == "GEOSITE" && len(p) == 4 {
		return
	}
	name = strings.TrimSpace(p[1])
	policy = strings.TrimSpace(p[2])
	return kind, name, policy, policy != "" && policy != "REJECT-DROP" && policy != "REJECT-TINYGIF"
}
