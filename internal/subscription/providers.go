package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openweir/subconv"
	"go.yaml.in/yaml/v3"
)

const maxProviderBody = 2 << 20
const maxProviders = 96

// Embedded URLs are selected by the document, unlike an administrator's
// subscription URL. Resolve and dial the inspected IP, including after redirects.
func publicProviderTransport() *http.Transport {
	return providerTransport(net.DefaultResolver.LookupIPAddr, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}

func providerTransport(lookup lookupFunc, dial dialFunc) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialResolved(ctx, network, address, lookup, dial, true)
	}
	return tr
}

// dialResolved dials the addresses it resolved itself. With public, a host with any
// address outside the public internet is refused before anything is dialed.
func dialResolved(ctx context.Context, network, address string, lookup lookupFunc, dial dialFunc, public bool) (net.Conn, error) {
	h, p, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := lookup(ctx, h)
	if e != nil {
		return nil, e
	}
	for _, ip := range ips {
		if public && (!ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.To4() != nil && ip.IP.To4()[0] == 100 && (ip.IP.To4()[1]&192) == 64) {
			return nil, errors.New("private_provider_address")
		}
	}
	for _, ip := range ips {
		c, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), p))
		e = err
		if err == nil {
			return c, nil
		}
	}
	if e != nil {
		return nil, e
	}
	return nil, errors.New("provider_unreachable")
}

type providers struct {
	trustedOrigin string
	trusted       Fetcher
	binary        []BinarySet
	f             Fetcher
	ctx           context.Context
	cache         map[string][]byte
	// kept is what the refresh's next reading of the subscription takes from this one: the
	// proxy providers, which every reading downloads (expandSourceProviders). nil keeps none.
	kept    map[string][]byte
	notices []Notice
	// template holds the rule sets of subconv's routing template, when the document uses it.
	template map[string][]byte
}

type providerInput struct {
	raw, kind string
	limit     int64
	header    http.Header
}

// key identifies a download: the same URL with other headers may serve other content.
func (i providerInput) key() string {
	if len(i.header) == 0 {
		return i.raw
	}
	return i.raw + "\x00" + digest(i.header)
}

func (s *providers) download(ctx context.Context, input providerInput) ([]byte, error) {
	raw := input.raw
	client := s.f
	trusted := false
	if strings.HasPrefix(raw, s.trustedOrigin+"/api/v1/app/proxy/subscriptions/rulesets/") && s.trustedOrigin != "" {
		u, err := webURL(raw, true)
		if err != nil {
			return nil, errors.New("invalid_provider_url")
		}
		rest := strings.TrimPrefix(u.EscapedPath(), "/api/v1/app/proxy/subscriptions/rulesets/")
		id, format, found := strings.Cut(rest, "/")
		if found && validTarget(id) && validTarget(format) && u.RawQuery == "" {
			client = s.trusted
			trusted = true
		}
	}
	// Even for the configured platform, an embedded provider receives no bearer
	// or cookies, only the headers its document gave it. The same-origin
	// allowlist also forbids redirects.
	client.header = input.header
	r, e := client.request(ctx, "GET", raw, nil, "", "Hyper", trusted, input.limit)
	if e != nil {
		return nil, providerRequestCause(e)
	}
	if r.status != http.StatusOK {
		return nil, fmt.Errorf("provider_http_%d", r.status)
	}
	return r.body, nil
}

// Only the downloads run in parallel. Expansion still applies the original
// order. The aggregate size limit applies to all unique URLs in this refresh.
const providerWorkers = 3

func (s *providers) prefetchInputs(inputs []providerInput, budget int) error {
	seen := make(map[string]providerInput)
	for _, input := range inputs {
		if _, err := webURL(input.raw, true); err != nil {
			return providerFailure(input.kind, input.raw, errors.New("invalid_provider_url"))
		}
		if prev, ok := seen[input.key()]; !ok || input.limit < prev.limit {
			seen[input.key()] = input
		}
	}
	jobs := make(chan providerInput, len(seen))
	total := 0
	for _, input := range seen {
		// An earlier reading of the subscription in this refresh downloaded it.
		if b, ok := s.kept[input.key()]; ok && int64(len(b)) <= input.limit {
			if total += len(b); total > budget {
				return providerFailure(input.kind, input.raw, errors.New("provider_size_limit"))
			}
			s.cache[input.key()] = b
			continue
		}
		jobs <- input
	}
	close(jobs)
	ctx, stop := context.WithCancel(s.ctx)
	defer stop()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	for range min(providerWorkers, len(seen)) {
		wg.Go(func() {
			for input := range jobs {
				if ctx.Err() != nil {
					return
				}
				b, err := s.download(ctx, input)
				mu.Lock()
				if err == nil && total+len(b) > budget {
					err = errors.New("provider_size_limit")
				}
				if err != nil && failure == nil {
					failure = providerFailure(input.kind, input.raw, err)
					stop()
				}
				if failure == nil {
					s.cache[input.key()] = b
					if input.kind == "node" && s.kept != nil {
						s.kept[input.key()] = b
					}
					total += len(b)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if failure != nil {
		return failure
	}
	return s.ctx.Err()
}

// templateSetError is a failure of the rule sets of subconv's routing template: the router
// cannot use one (ruleSet), or all of them together. The refresh routes as when they did not
// arrive (readConverted).
type templateSetError struct{ error }

func (e templateSetError) Unwrap() error { return e.error }

// expandSourceProviders downloads a converted document's providers into it. It returns the rule
// sets the document gained and notices about provider content and options. The rule sets of
// subconv's routing template come with it (template, by provider name; fetchTemplateSets), and
// make up all of its rule sets then: their failures are templateSetErrors. downloads keeps the
// proxy providers downloaded, by providerInput.key, for the next reading of the subscription in
// the refresh, which downloads none of them again (nil: none does). Its rule providers are not
// kept: a later reading routes by the template, or by default.
func (f Fetcher) expandSourceProviders(ctx context.Context, m map[string]any, origin string, template map[string][]byte, downloads map[string][]byte) ([]BinarySet, []Notice, error) {
	s := &providers{trustedOrigin: strings.TrimSuffix(providerAddress(origin), "/"), trusted: f, ctx: ctx, cache: map[string][]byte{}, kept: downloads, template: template}
	var done func()
	s.f, done = f.embeddedFetcher()
	defer done()
	normalizeProviderAddresses(m)
	if r := f.providerReceipt; r != nil && r.Key != "" {
		if ttl := providerLifetime(m); ttl > 0 {
			r.Expires = r.Checked.Add(ttl)
		}
	}
	if e := s.clash(m); e != nil {
		return nil, nil, e
	}
	total := 0
	for _, rs := range s.binary {
		total += len(rs.Data)
		if total > MaxBody {
			if template != nil {
				return nil, nil, templateSetError{errors.New("provider_size_limit")}
			}
			return nil, nil, errors.New("provider_size_limit")
		}
	}
	if out, e := json.Marshal(m); e != nil || len(out) > MaxBody {
		return nil, nil, errors.New("expanded_subscription_limit")
	}
	return s.binary, s.notices, nil
}
func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
func (s *providers) clash(m map[string]any) error {
	pp := object(m["proxy-providers"])
	rp := object(m["rule-providers"])
	if len(pp)+len(rp) > maxProviders {
		return errors.New("provider_limit")
	}
	var inputs []providerInput
	expandable := map[string]proxyProvider{}
	for _, k := range sortedKeys(pp) {
		if p, ok := s.proxyProvider(k, object(pp[k])); ok {
			expandable[k] = p
			if p.input.raw != "" {
				inputs = append(inputs, p.input)
			}
		}
	}
	ruleInputs := map[string]providerInput{}
	for _, k := range sortedKeys(rp) {
		if v := object(rp[k]); field(v, "type") == "http" && field(v, "url") != "" && s.template == nil {
			ruleInputs[k] = s.httpInput(k, v, "rule", MaxBody)
			inputs = append(inputs, ruleInputs[k])
		}
	}
	if err := s.prefetchInputs(inputs, maxSetMaterialization); err != nil {
		return err
	}
	names := map[string][]any{}
	var standalone []any
	// The type of each node, by provider ("" for the document's own) and name, for exclude-type.
	kinds := map[string]string{}
	for _, n := range list(m["proxies"]) {
		standalone = append(standalone, object(n)["name"])
		kinds["\x00"+field(object(n), "name")] = field(object(n), "type")
	}
	allProviderNames := sortedKeys(pp)

	nodes := list(m["proxies"])
	if nodes == nil {
		nodes = []any{}
	}
	for _, k := range sortedKeys(pp) {
		p, ok := expandable[k]
		if !ok {
			continue
		}
		// subconv checked an inline payload with the document; downloaded content may be in any
		// format a provider serves.
		payload := list(object(pp[k])["payload"])
		if p.input.raw != "" {
			var err error
			if payload, err = s.providerProxies(s.cache[p.input.key()]); err != nil {
				return providerFailure("node", p.input.raw, err)
			}
		}
		var proxies []map[string]any
		for _, elem := range payload {
			if n := object(elem); n != nil {
				proxies = append(proxies, n)
			}
		}
		proxies = p.shape(proxies)
		if len(nodes)+len(proxies) > maxNodes {
			return errors.New("node_limit")
		}
		members := []any{}
		for _, n := range proxies {
			members = append(members, n["name"])
			nodes = append(nodes, n)
			kinds[k+"\x00"+field(n, "name")] = field(n, "type")
		}
		names[k] = members
		delete(pp, k)
	}
	m["proxies"] = nodes
	for _, item := range list(m["proxy-groups"]) {
		g := object(item)
		if g["include-all"] == true {
			g["include-all-providers"] = true
			g["include-all-proxies"] = true
			delete(g, "include-all")
		}
		if g["include-all-providers"] == true {
			g["use"] = allProviderNames
			delete(g, "include-all-providers")
		}
		if g["use"] == nil && g["include-all-proxies"] != true {
			continue
		}
		var use []string
		if g["use"] != nil {
			var ok bool
			use, ok = textlist(g["use"])
			if !ok {
				continue
			}
		}
		matches, ok := groupFilter(g)
		if !ok {
			continue
		}
		excluded := excludedTypes(g)
		members := list(g["proxies"])
		if g["include-all-proxies"] == true {
			for _, x := range standalone {
				if name, ok := x.(string); ok && matches(name) && !excluded(kinds["\x00"+name]) {
					members = append(members, name)
				}
			}
			delete(g, "include-all-proxies")
		}
		complete := true
		for _, k := range use {
			n, ok := names[k]
			if !ok {
				complete = false
				break
			}
			if len(members)+len(n) > maxNodes {
				return errors.New("node_limit")
			}
			for _, x := range n {
				if name, ok := x.(string); ok && matches(name) && !excluded(kinds[k+"\x00"+name]) {
					members = append(members, name)
				}
			}
		}
		if complete {
			delete(g, "use")
			delete(g, "filter")
			delete(g, "exclude-filter")
			delete(g, "exclude-type")
			if len(members) == 0 {
				if fallback := field(g, "empty-fallback"); fallback != "" {
					members = append(members, fallback)
				}
			}
			delete(g, "empty-fallback")
			g["proxies"] = members
		}
	}
	// Provider names only address rules: template paths never become local paths. A file is one
	// the administrator imported beside the subscription (localClashSet).
	for _, name := range sortedKeys(rp) {
		v := object(rp[name])
		var raw []byte
		var err error
		fromTemplate := false
		switch field(v, "type") {
		case "http":
			if s.template == nil {
				raw = s.cache[ruleInputs[name].key()]
			} else {
				raw, fromTemplate = s.template[name], true
			}
		case "file":
			if raw, err = s.trusted.localClashSet(field(v, "path")); err != nil {
				continue
			}
		case "inline":
			if raw, err = yaml.Marshal(map[string]any{"payload": v["payload"]}); err != nil {
				return errRulePayload
			}
		default:
			continue
		}
		set, err := ruleSet(name, field(v, "format"), field(v, "behavior"), raw)
		switch {
		case name == DomesticIPSet || name == DomesticDomainSet:
			err = errRulePayload
		case err != nil:
			err = providerFailure("rule", field(v, "url"), err)
		}
		if err != nil && fromTemplate {
			return templateSetError{err}
		}
		if err != nil {
			return err
		}
		s.binary = append(s.binary, set)
		delete(rp, name)
	}
	return nil
}

// providerProxies converts a downloaded proxy provider: subconv reads the formats mihomo's
// providers accept and more, and repairs or leaves out each proxy on its own.
func (s *providers) providerProxies(b []byte) ([]any, error) {
	r, err := subconv.ConvertProxies(b)
	if err != nil {
		return nil, errors.New("invalid_proxy_provider")
	}
	for _, w := range r.Warnings {
		s.notices = append(s.notices, warningNotice(w))
	}
	doc, err := document(r)
	if err != nil {
		return nil, errors.New("invalid_proxy_provider")
	}
	return list(doc["proxies"]), nil
}

// excludedTypes reports the node types a group's exclude-type leaves out. mihomo compares it,
// in any case, with its own names of the types, which differ from the config's only for ss
// (Shadowsocks) and ssr (ShadowsocksR).
func excludedTypes(g map[string]any) func(string) bool {
	types := strings.Split(strings.ToLower(field(g, "exclude-type")), "|")
	return func(kind string) bool {
		name := map[string]string{"ss": "shadowsocks", "ssr": "shadowsocksr"}[kind]
		return kind != "" && (slices.Contains(types, kind) || name != "" && slices.Contains(types, name))
	}
}

func groupFilter(g map[string]any) (func(string) bool, bool) {
	compile := func(k string) ([]*regexp.Regexp, bool) {
		v, ok := g[k]
		if !ok {
			return nil, true
		}
		raw, ok := v.(string)
		if !ok || len(raw) > 4096 {
			return nil, false
		}
		if raw == "" {
			return nil, true
		}
		var out []*regexp.Regexp
		for _, pattern := range strings.Split(raw, string(rune(96))) {
			r, e := regexp.Compile(pattern)
			if e != nil {
				return nil, false
			}
			out = append(out, r)
		}
		return out, true
	}
	incl, ok := compile("filter")
	if !ok {
		return nil, false
	}
	excl, ok := compile("exclude-filter")
	if !ok {
		return nil, false
	}
	return func(s string) bool {
		for _, r := range excl {
			if r.MatchString(s) {
				return false
			}
		}
		for _, r := range incl {
			if r.MatchString(s) {
				return true
			}
		}
		return len(incl) == 0
	}, true
}
