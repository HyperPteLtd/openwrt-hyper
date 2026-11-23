package subscription

import (
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/proxyspec"
)

// The router expands a proxy provider into the document itself: it downloads the provider
// and applies the provider's mihomo options to its nodes. An option that would let the
// document reach into the router is refused, and one the router cannot follow is ignored,
// each with a notice; the provider's nodes still load. Scheduling (interval), health checks
// and the core's cache path are the router's own.
var proxyProviderOptions = set("type url path interval health-check payload size-limit header proxy filter exclude-filter exclude-type dialer-proxy override")

// Override options that set one node option. mihomo's override-expr is a query language the
// router does not evaluate.
var overrideOptions = set("tfo mptcp udp udp-over-tcp up down dialer-proxy skip-cert-verify name-cert-verify ip-version")

const maxProviderHeader = 8 << 10

type proxyProvider struct {
	// input is empty for an inline provider.
	input providerInput
	shape func([]map[string]any) []map[string]any
}

func (s *providers) notice(code, provider, option string) {
	n := Notice{Code: code, At: publicLabel(provider+" · "+option, "provider")}
	if !slices.Contains(s.notices, n) {
		s.notices = append(s.notices, n)
	}
}

func optionLabel(k string) string {
	if len(k) <= 64 && safeLabel(k) {
		return k
	}
	return "option"
}

// httpInput reads the download options mihomo gives an http provider of either kind.
func (s *providers) httpInput(name string, v map[string]any, kind string, limit int64) providerInput {
	in := providerInput{raw: field(v, "url"), kind: kind, limit: limit}
	if x, ok := v["header"]; ok {
		in.header = s.providerHeader(name, x)
	}
	if x, ok := v["size-limit"]; ok {
		if n, ok := integer(x); !ok || n < 0 {
			s.notice("provider_option_ignored", name, "size-limit")
		} else if n > 0 {
			in.limit = min(limit, int64(n))
		}
	}
	if _, ok := v["proxy"]; ok {
		// The router downloads over its own way out, not through a group of the document.
		s.notice("provider_option_ignored", name, "proxy")
	}
	return in
}

// proxyProvider reads a provider the router can expand: one it downloads or one written
// inline, but not a local file nor content encrypted for the core (age-secret-key).
func (s *providers) proxyProvider(name string, v map[string]any) (proxyProvider, bool) {
	kind := field(v, "type")
	if kind != "http" && kind != "inline" || kind == "http" && field(v, "url") == "" || v["age-secret-key"] != nil {
		return proxyProvider{}, false
	}
	p := proxyProvider{}
	if kind == "http" {
		p.input = s.httpInput(name, v, "node", maxProviderBody)
	}
	var dialer string
	var types []string
	for _, k := range sortedKeys(v) {
		x := v[k]
		switch k {
		case "proxy", "header", "size-limit":
		case "exclude-type":
			if t, ok := x.(string); ok {
				types = strings.Split(t, "|")
			} else {
				s.notice("provider_option_ignored", name, k)
			}
		case "dialer-proxy":
			if proxyspec.Unsafe(k, x) {
				s.notice("provider_option_unsafe", name, k)
			} else {
				dialer = x.(string)
			}
		default:
			if !proxyProviderOptions[k] {
				s.notice("provider_option_ignored", name, optionLabel(k))
			}
		}
	}
	include, exclude := s.patterns(name, v, "filter"), s.patterns(name, v, "exclude-filter")
	override := s.override(name, v["override"])
	// As mihomo does: each filter in turn picks the nodes it matches, a name counts once, and
	// filters see the names the provider gave while groups see the overridden ones.
	if len(include) == 0 {
		include = []*regexp.Regexp{nil}
	}
	p.shape = func(nodes []map[string]any) []map[string]any {
		seen := map[string]bool{}
		var out []map[string]any
		for _, r := range include {
			for _, n := range nodes {
				label, ok := n["name"].(string)
				matches := func(x *regexp.Regexp) bool { return x.MatchString(label) }
				if !ok || seen[label] || r != nil && !r.MatchString(label) || slices.ContainsFunc(exclude, matches) ||
					slices.ContainsFunc(types, func(t string) bool { return strings.EqualFold(t, field(n, "type")) }) {
					continue
				}
				seen[label] = true
				if dialer != "" {
					n["dialer-proxy"] = dialer
				}
				override(n)
				out = append(out, n)
			}
		}
		return out
	}
	return p, true
}

// patterns compiles a filter: regular expressions joined by backquotes. mihomo's engine
// accepts some (lookarounds) Go's does not; such a filter is ignored rather than guessed.
func (s *providers) patterns(name string, v map[string]any, key string) []*regexp.Regexp {
	raw, ok := v[key].(string)
	if v[key] == nil || ok && raw == "" {
		return nil
	}
	var out []*regexp.Regexp
	for _, pattern := range strings.Split(raw, "`") {
		r, err := regexp.Compile(pattern)
		if !ok || len(raw) > 4096 || err != nil {
			s.notice("provider_option_ignored", name, key)
			return nil
		}
		out = append(out, r)
	}
	return out
}

// override returns what a provider's override does to each of its nodes. Every option passes
// the same router-safety policy as the nodes themselves.
func (s *providers) override(name string, raw any) func(map[string]any) {
	o := object(raw)
	if raw != nil && o == nil {
		s.notice("provider_option_ignored", name, "override")
	}
	values := map[string]any{}
	var prefix, suffix string
	var renames []func(string) string
	for _, k := range sortedKeys(o) {
		x, label := o[k], "override "+optionLabel(k)
		switch {
		case proxyspec.Unsafe(k, x):
			s.notice("provider_option_unsafe", name, label)
		case overrideOptions[k] && scalar(x):
			values[k] = x
		case k == "additional-prefix" || k == "additional-suffix":
			text, ok := x.(string)
			if !ok {
				s.notice("provider_option_ignored", name, label)
			} else if k == "additional-prefix" {
				prefix = text
			} else {
				suffix = text
			}
		case k == "proxy-name" && list(x) != nil:
			for _, item := range list(x) {
				rule := object(item)
				target, ok := rule["target"].(string)
				r, err := regexp.Compile(field(rule, "pattern"))
				if !ok || field(rule, "pattern") == "" || err != nil {
					s.notice("provider_option_ignored", name, label)
					continue
				}
				renames = append(renames, func(label string) string { return r.ReplaceAllString(label, target) })
			}
		default:
			s.notice("provider_option_ignored", name, label)
		}
	}
	return func(n map[string]any) {
		maps.Copy(n, values)
		label, ok := n["name"].(string)
		if !ok || prefix == "" && suffix == "" && len(renames) == 0 {
			return
		}
		for _, rename := range renames {
			label = rename(label)
		}
		n["name"] = prefix + label + suffix
	}
}

func scalar(v any) bool {
	switch v.(type) {
	case string, bool, int, int64, uint64, float64, json.Number:
		return true
	}
	return false
}

// providerHeader keeps the request headers a document gives a provider download that the
// router can send as given: framing, connection and proxy headers belong to its own client.
func (s *providers) providerHeader(name string, raw any) http.Header {
	m := object(raw)
	if m == nil {
		s.notice("provider_option_ignored", name, "header")
		return nil
	}
	h := http.Header{}
	size := 0
	for _, k := range sortedKeys(m) {
		values, ok := textlist(m[k])
		key := http.CanonicalHeaderKey(k)
		for _, v := range values {
			size += len(k) + len(v)
		}
		if !ok || !headerToken(k) || reservedHeader(key) || slices.ContainsFunc(values, badHeaderValue) || size > maxProviderHeader {
			s.notice("provider_option_ignored", name, "header")
			continue
		}
		h[key] = values
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

func reservedHeader(k string) bool {
	switch k {
	case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Upgrade", "Te", "Trailer", "Expect", "Accept-Encoding":
		return true
	}
	return strings.HasPrefix(k, "Proxy-")
}

func headerToken(k string) bool {
	return k != "" && len(k) <= 64 && strings.Trim(k, "!#$%&'*+-.^_`|~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") == ""
}

func badHeaderValue(v string) bool {
	return len(v) > 1024 || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f })
}
