package subscription

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func proxyProtocol(m map[string]any) string {
	kind := field(m, "type")
	switch kind {
	case "ss":
		return "shadowsocks"
	case "ssr":
		return "shadowsocksr"
	case "http":
		if m["tls"] == true {
			return "https"
		}
	}
	return kind
}
