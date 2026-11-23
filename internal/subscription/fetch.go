package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

// Source holds private request details. It must not be returned by an RPC read.
type Source struct {
	Plan               *Plan             `json:"plan,omitempty"`
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	AutoName           bool              `json:"auto_name,omitempty"` // named after the link; the subscription's own title replaces it
	Kind               string            `json:"kind"`
	URL                string            `json:"url,omitempty"`
	UserAgent          string            `json:"user_agent,omitempty"`
	Origin             string            `json:"origin,omitempty"`
	UserID             string            `json:"user_id,omitempty"`
	Token              string            `json:"token,omitempty"`
	PlanID             int               `json:"plan_id,omitempty"`
	Filters            map[string]string `json:"filters,omitempty"`
	AllowLocalFallback bool              `json:"allow_local_fallback,omitempty"`
	// Rules is the source's routing choice: RoutingOwn or RoutingTemplate.
	Rules string `json:"rules,omitempty"`
}

const defaultSubscriptionUserAgent = "clash"
const platformUserAgent = "Hyper"

// The Hyper platform matches its first-party client by the product/version prefix.
var packageVersion = "dev"

// PackageVersion is the plugin version this build was packaged as.
func PackageVersion() string { return packageVersion }

func subscriptionHeader(agent string) string {
	if agent == platformUserAgent {
		return platformUserAgent + "/" + packageVersion + " (OpenWrt)"
	}
	return agent
}

var UserAgents = []string{defaultSubscriptionUserAgent, "sing-box", platformUserAgent}

func webURL(s string, secure bool) (*url.URL, error) {
	u, e := url.Parse(s)
	if e != nil || len(s) > 8192 || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && (secure || u.Scheme != "http")) {
		return nil, errors.New("invalid_source_url")
	}
	return u, nil
}
func validOrigin(raw string) bool {
	u, e := webURL(raw, true)
	return e == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery
}
func sourceIDValid(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == ':' || c == '.') {
			return false
		}
	}
	return true
}
func validTarget(k string) bool {
	if k == "" || len(k) > 64 || strings.EqualFold(k, "hyper") || strings.EqualFold(k, "sip008") {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (s Source) validate() error {
	if !sourceIDValid(s.ID) || (strings.HasPrefix(s.ID, "plan:") && s.Kind != "platform") || !safeLabel(s.ID) || !safeLabel(s.Name) || len(s.UserAgent) > 256 || strings.ContainsAny(s.UserAgent, "\r\n\x00") || s.Rules != RoutingOwn && s.Rules != RoutingTemplate {
		return errors.New("invalid_source")
	}
	switch s.Kind {
	case "external":
		if s.Token != "" || s.Origin != "" || s.PlanID != 0 || s.UserID != "" {
			return errors.New("invalid_external_source")
		}
		_, e := webURL(s.URL, false)
		return e
	case "file":
		if s.URL != "" || s.Token != "" || s.Origin != "" || s.UserID != "" || s.PlanID != 0 {
			return errors.New("invalid_file_source")
		}
		return nil
	case "platform":
		if !validOrigin(s.Origin) || !safeLabel(s.UserID) || s.PlanID < 1 || s.Token == "" || len(s.Token) > 8192 || strings.ContainsAny(s.Token, "\r\n\x00") || s.URL != "" {
			return errors.New("invalid_platform_source")
		}
		allowed := set("flag include exclude rename sort emoji")
		for k, v := range s.Filters {
			if !allowed[k] || len(v) > 200 {
				return errors.New("invalid_template_filter")
			}
		}
		return nil
	default:
		return errors.New("invalid_source_kind")
	}
}

type Validator func(context.Context, Profile) (func() error, error)
type Fetcher struct {
	previous        *record
	providerReceipt *providerReceipt
	egress          *egress
	reuseProviders  bool
	// embedded marks URLs a document chose (providers): every path checks their addresses,
	// and header is what the document asked to send with them (providerHeader).
	embedded       bool
	header         http.Header
	sourceID       string
	ruleImportRoot string
	// Tests reach the URLs documents and the rules branch choose on loopback.
	embeddedClient *http.Client
	ReplacementURL string
	Client         *http.Client
	BeforeCommit   Validator
	// Fallback retries a request that failed in transit once over the router's other way
	// out (fallback.go).
	Fallback bool
	// Routing is a routing choice (Source.Rules) a refresh reads the subscription with in
	// place of the source's. Like ReplacementURL, the source takes it only with the update the
	// refresh commits: one cancelled, held for confirmation or blocked leaves the source's
	// choice, which its scheduled refreshes go on reading with.
	Routing *string
}

// withEgress lets the requests of one operation share what they learn about the way out.
func (f Fetcher) withEgress() Fetcher {
	if f.Fallback && f.egress == nil {
		f.egress = new(egress)
	}
	return f
}

type response struct {
	body   []byte
	header http.Header
	status int
}

func (f Fetcher) request(ctx context.Context, method, raw string, payload []byte, token, ua string, authenticated bool, limit int64) (response, error) {
	u, e := webURL(raw, authenticated)
	if e != nil {
		return response{}, e
	}
	c := http.Client{Timeout: 25 * time.Second}
	if f.Client != nil {
		c = *f.Client
		if c.Timeout == 0 {
			c.Timeout = 25 * time.Second
		}
	}
	c.Jar = nil
	c.CheckRedirect = func(next *http.Request, prev []*http.Request) error {
		if len(prev) > 4 || next.URL.User != nil || next.URL.Scheme != "https" && (u.Scheme == "https" || next.URL.Scheme != "http") {
			return errRedirectRefused
		}
		if authenticated {
			return errAuthenticatedRedirect
		}
		if !strings.EqualFold(next.URL.Host, u.Host) || next.URL.Scheme != u.Scheme {
			next.Header = make(http.Header)
			redirectAgent := platformUserAgent
			for _, preset := range UserAgents {
				if ua == subscriptionHeader(preset) {
					redirectAgent = ua
					break
				}
			}
			next.Header.Set("User-Agent", redirectAgent)
		}
		next.Header.Del("Referer")
		return nil
	}
	if ua == "" {
		ua = "Hyper"
	}
	send := func(c *http.Client) (response, error) {
		r, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
		if e != nil {
			return response{}, errors.New("invalid_request")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("User-Agent", ua)
		maps.Copy(r.Header, f.header)
		if payload != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		res, e := c.Do(r)
		if e != nil {
			// A refused redirect is the server's answer, which a later attempt gets again.
			general := "subscription_request_failed"
			if errors.Is(e, errRedirectRefused) || errors.Is(e, errAuthenticatedRedirect) {
				general = "subscription_redirect_refused"
			}
			return response{}, classifyRequestFailure(e, general)
		}
		defer res.Body.Close()
		limit := limit
		if res.StatusCode != 200 {
			limit = 32 << 10
		}
		body, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if e != nil {
			// The answer broke off or timed out before its end, unlike one over the size limit.
			return response{}, classifyRequestFailure(e, "subscription_incomplete")
		}
		if int64(len(body)) > limit {
			return response{}, errors.New("invalid_response_size")
		}
		return response{body, res.Header, res.StatusCode}, nil
	}
	if !f.Fallback {
		return send(&c)
	}
	// Redirects never leave https, so the scheme covers every hop.
	return f.egress.send(ctx, &c, f.embedded, u.Scheme == "https", send)
}
func statusErr(r response) error {
	switch r.status {
	case 200:
		return nil
	case http.StatusUnauthorized:
		return ErrLoginRequired
	}
	var v struct{ Code string }
	_ = json.Unmarshal(r.body, &v)
	if r.status == 403 || r.status == 404 {
		switch v.Code {
		case "ACCOUNT_REVOKED":
			return errAccountRevoked
		case "SUBSCRIPTION_REVOKED":
			return ErrRevoked
		case "SUBSCRIPTION_APP_DISABLED":
			if r.status == http.StatusForbidden {
				return errAppDisabled
			}
		}
	}
	return fmt.Errorf("subscription_http_%d", r.status)
}

var errAccountRevoked = errors.New("platform account revoked")
var errAppDisabled = errors.New("subscription_app_disabled")

// A plan's issued link the provider withholds, that is malformed or that its download host
// refuses, redirects or answers with too much: retrying does not help.
var errLinkUnavailable = errors.New("subscription_link_unavailable")

// A plan's issued link whose download failed on the way or that its host answered 408, 429 or
// 5xx: a later attempt can succeed.
var errLinkUnreachable = errors.New("subscription_link_unreachable")

// The redirects request refuses (CheckRedirect).
var (
	errRedirectRefused       = errors.New("redirect_refused")
	errAuthenticatedRedirect = errors.New("authenticated_redirect_refused")
)

type AccountWithdrawal struct{ Sources []string }

func (*AccountWithdrawal) Error() string { return "platform account revoked" }
func (*AccountWithdrawal) Unwrap() error { return ErrRevoked }

type Site struct {
	Name string `json:"name"`
	Logo bool   `json:"logo"`
}

func (f Fetcher) Site(ctx context.Context, origin string) (Site, error) {
	if !validOrigin(origin) {
		return Site{}, errors.New("invalid_origin")
	}
	r, e := f.request(ctx, "GET", strings.TrimRight(origin, "/")+"/api/v1/app/site", nil, "", "", true, 128<<10)
	if e != nil {
		return Site{}, e
	}
	if e = statusErr(r); e != nil {
		return Site{}, e
	}
	var v struct {
		Name   string
		Images struct{ Logo bool }
	}
	if json.Unmarshal(r.body, &v) != nil || !safeLabel(v.Name) {
		return Site{}, errors.New("invalid_platform_site")
	}
	return Site{v.Name, v.Images.Logo}, nil
}

type Plan struct {
	ID       int      `json:"id"`
	Name     string   `json:"planName"`
	Status   string   `json:"status"`
	Usable   bool     `json:"usable"`
	Expires  *string  `json:"expireAt,omitempty"`
	Total    *float64 `json:"total,omitempty"`
	Upload   float64  `json:"upload"`
	Download float64  `json:"download"`
}

// Billing includes an owner's secret subscription URL. Only Plan is stored or
// returned to LuCI. A missing usable flag is different from a suspended plan.
type billingPlan struct {
	Plan
	UserID             string
	URL                *string
	SubscriptionAccess json.RawMessage `json:"subscriptionAccess"`
	IsUsable           *bool           `json:"usable"`
}

func (p billingPlan) valid(owner string) bool {
	return p.UserID == owner && p.ID > 0 && p.IsUsable != nil &&
		(p.Status == "active" || p.Status == "expired" || p.Status == "disabled") &&
		(p.Name == "" || safeLabel(p.Name))
}

type Account struct {
	Site  Site   `json:"site"`
	Plans []Plan `json:"plans"`
	// Credentials are intentionally inaccessible outside this package.
	origin, userID, token string
}

func (f Fetcher) Login(ctx context.Context, origin, email, password string) (Account, error) {
	f = f.withEgress()
	site, e := f.Site(ctx, origin)
	if e != nil {
		return Account{}, e
	}
	if !safeLabel(email) || password == "" || len(password) > 8192 {
		return Account{}, errors.New("invalid_login")
	}
	origin = strings.TrimRight(origin, "/")
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	r, e := f.request(ctx, "POST", origin+"/api/auth/sign-in/email", payload, "", "", true, 128<<10)
	if e != nil {
		return Account{}, e
	}
	if e = statusErr(r); e != nil {
		return Account{}, e
	}
	var auth struct {
		Token string
		User  struct{ ID string }
	}
	if json.Unmarshal(r.body, &auth) != nil || !safeLabel(auth.User.ID) {
		return Account{}, errors.New("invalid_login_response")
	}
	token := r.header.Get("Set-Auth-Token")
	if token == "" {
		token = auth.Token
	}
	if token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
		return Account{}, errors.New("invalid_login_token")
	}
	plans, e := f.ownedPlans(ctx, origin, token, auth.User.ID)
	if e != nil {
		return Account{}, e
	}
	return Account{Site: site, origin: origin, userID: auth.User.ID, token: token, Plans: plans}, nil
}

func (f Fetcher) ownedPlans(ctx context.Context, origin, token, owner string) ([]Plan, error) {
	endpoint := strings.TrimRight(origin, "/") + "/api/v1/app/billing/subscriptions"
	r, e := f.request(ctx, http.MethodGet, endpoint, nil, token, subscriptionHeader(platformUserAgent), true, 1<<20)
	if e != nil {
		return nil, e
	}
	if e = statusErr(r); e != nil {
		return nil, e
	}
	var plans []billingPlan
	if json.Unmarshal(r.body, &plans) != nil || plans == nil || len(plans) > 128 {
		return nil, errors.New("invalid_plan_list")
	}
	out := make([]Plan, 0, len(plans))
	seen := make(map[int]bool, len(plans))
	for _, p := range plans {
		if !p.valid(owner) || seen[p.ID] {
			return nil, errors.New("invalid_plan_list")
		}
		seen[p.ID] = true
		p.Plan.Usable = *p.IsUsable
		out = append(out, p.Plan)
	}
	return out, nil
}
func (a Account) Source(p Plan) Source {
	name := a.Site.Name
	if p.Name != "" {
		name += " · " + p.Name
	}
	return Source{Plan: &p, ID: ref("plan", []any{a.origin, a.userID, p.ID}), Name: name, Kind: "platform", Origin: a.origin, UserID: a.userID, Token: a.token, PlanID: p.ID, UserAgent: platformUserAgent}
}
func (s Source) platformFilters() url.Values {
	q := url.Values{}
	for k, v := range s.Filters {
		if k != "flag" { // Older sources may have stored a template-selection hint.
			q.Set(k, v)
		}
	}
	return q
}

func (s Source) platformURL(resource string) string {
	endpoint := fmt.Sprintf("%s/api/v1/app/proxy/subscriptions/%d/%s", strings.TrimRight(s.Origin, "/"), s.PlanID, resource)
	q := s.platformFilters()
	if len(q) == 0 {
		return endpoint
	}
	return endpoint + "?" + q.Encode()
}

// Billing is authoritative on every refresh: access mode and links can change
// independently of the saved account. The issued URL is never persisted.
func (f Fetcher) ownedPlanTemplate(ctx context.Context, s Source) (response, error) {
	endpoint := fmt.Sprintf("%s/api/v1/app/billing/subscriptions/%d", strings.TrimRight(s.Origin, "/"), s.PlanID)
	ua := subscriptionHeader(platformUserAgent)
	r, err := f.request(ctx, http.MethodGet, endpoint, nil, s.Token, ua, true, 128<<10)
	if err != nil {
		return response{}, err
	}
	if err = statusErr(r); err != nil {
		if r.status == http.StatusNotFound && !errors.Is(err, ErrRevoked) && !errors.Is(err, errAccountRevoked) {
			err = f.entitlement(ctx, s, false)
		}
		return response{}, err
	}
	var plan billingPlan
	if json.Unmarshal(r.body, &plan) != nil || !plan.valid(s.UserID) || plan.ID != s.PlanID {
		return response{}, errors.New("invalid_plan_detail")
	}
	if plan.Status == "disabled" {
		return response{}, ErrRevoked
	}
	if plan.Status == "expired" || !*plan.IsUsable {
		return response{}, ErrSuspended
	}
	// Older servers omit the access mode and only expose a URL when links
	// are enabled. An explicit malformed mode is a contract error.
	var mode string
	if plan.SubscriptionAccess == nil {
		mode = "app"
		if plan.URL != nil {
			mode = "link"
		}
	} else if err := json.Unmarshal(plan.SubscriptionAccess, &mode); err != nil {
		return response{}, errors.New("invalid_plan_detail")
	}
	switch mode {
	case "app", "both":
		r, err := f.request(ctx, http.MethodGet, s.platformURL("config"), nil, s.Token, ua, true, MaxBody)
		if err != nil {
			return response{}, err
		}
		return r, statusErr(r)
	case "link":
		if plan.URL == nil {
			return response{}, errLinkUnavailable
		}
	default:
		return response{}, errors.New("invalid_plan_detail")
	}
	u, err := webURL(*plan.URL, true)
	if err != nil {
		return response{}, errLinkUnavailable
	}
	filters := s.platformFilters()
	if len(filters) > 0 {
		existing, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return response{}, errLinkUnavailable
		}
		for k, v := range filters {
			if previous, ok := existing[k]; ok {
				if !slices.Equal(previous, v) {
					return response{}, errLinkUnavailable
				}
				delete(filters, k)
			}
		}
		if extra := filters.Encode(); extra != "" {
			if u.RawQuery != "" {
				u.RawQuery += "&"
			}
			u.RawQuery += extra
		}
	}
	// Even a same-origin link authenticates by its URL. Forbid redirects and
	// never interpret a download host's response as a change to account access.
	r, err = f.request(ctx, http.MethodGet, u.String(), nil, "", ua, true, MaxBody)
	switch {
	case tlsdiag.From(err) != nil:
		return response{}, err
	case transient(ctx, err) || err == nil && (r.status == http.StatusRequestTimeout || r.status == http.StatusTooManyRequests || r.status >= 500 && r.status <= 599):
		return response{}, errLinkUnreachable
	case err != nil || r.status != http.StatusOK:
		return response{}, errLinkUnavailable
	}
	return r, nil
}

func (s Source) subscriptionAgent() string {
	if s.Kind == "platform" {
		return platformUserAgent
	}
	return first(s.UserAgent, defaultSubscriptionUserAgent)
}

type nodeInventory struct {
	Usable *bool
	Nodes  []struct {
		Protocol string
		Status   string
	}
}

// An editable template may omit protocols or give nodes new display names.
// The inventory describes entitlement, not a second source of node credentials.
func (inventory nodeInventory) reportOmissions(p *Profile) {
	missing := make(map[string]int)
	for _, n := range inventory.Nodes {
		if n.Status == "maintenance" || n.Status == "over_quota" {
			continue
		}
		proto := strings.ToLower(n.Protocol)
		switch proto {
		case "mtproxy":
			// Hyper's Mihomo template intentionally omits the Telegram-only protocol.
			continue
		case "ss":
			proto = "shadowsocks"
		case "ssr":
			proto = "shadowsocksr"
		case "socks":
			proto = "socks5"
		case "hy2":
			proto = "hysteria2"
		}
		missing[proto]++
	}
	for _, n := range p.Nodes {
		missing[n.Protocol]--
	}
	for _, proto := range slices.Sorted(maps.Keys(missing)) {
		if missing[proto] > 0 {
			p.notice("unavailable_inventory", proto, false)
		}
	}
}

func (f Fetcher) readProfile(ctx context.Context, s Source, file []byte, usage **Usage) (Profile, error) {
	get := func(raw string) (response, error) {
		authenticated := s.Kind == "platform"
		token := ""
		if authenticated {
			token = s.Token
		}
		r, err := f.request(ctx, "GET", raw, nil, token, subscriptionHeader(s.subscriptionAgent()), authenticated, MaxBody)
		if err != nil {
			return response{}, err
		}
		return r, statusErr(r)
	}

	var inventory nodeInventory
	origin := ""
	if s.Kind == "platform" {
		r, err := get(s.platformURL("nodes"))
		if err != nil {
			if err.Error() == "subscription_http_404" {
				return Profile{}, f.entitlement(ctx, s, false)
			}
			return Profile{}, err
		}
		if json.Unmarshal(r.body, &inventory) != nil || inventory.Usable == nil || inventory.Nodes == nil || len(inventory.Nodes) > maxNodes {
			return Profile{}, errors.New("invalid_node_inventory")
		}
		if !*inventory.Usable {
			return Profile{}, f.entitlement(ctx, s, true)
		}
		origin = strings.TrimRight(s.Origin, "/")
	} else if s.Kind == "external" {
		// The admin supplied this origin. Providers never receive platform credentials.
		if u, err := webURL(s.URL, true); err == nil {
			origin = "https://" + u.Host
		}
	}

	body := file
	var headers http.Header
	if s.Kind != "file" {
		var r response
		var err error
		if s.Kind == "platform" {
			r, err = f.ownedPlanTemplate(ctx, s)
		} else {
			r, err = get(s.URL)
		}
		if err != nil {
			return Profile{}, err
		}
		body, headers = r.body, r.header
		*usage = parseUsage(headers.Get("Subscription-Userinfo"))
	}
	if s.Kind == "external" && f.providerReceipt != nil {
		key := providerSnapshotKey(s, body)
		now := time.Now().UTC()
		prev := f.previous
		if f.reuseProviders && prev != nil && prev.Current != nil && !prev.Suspended && !prev.LoginRequired && !prev.Revoked && prev.Providers.fresh(key, now) {
			*f.providerReceipt = *prev.Providers
			return *prev.Current, nil
		}
		*f.providerReceipt = providerReceipt{Key: key, Checked: now}
	}
	branch := f.branch(ctx)
	defer branch.close()
	return f.readConverted(ctx, s, body, headers, origin, inventory, branch)
}

// readConverted converts a subscription's body with its routing choice (convert) and reads it
// as the router routes it: with the template's rule sets or its own providers, through the
// router's checks (parseVerified). Only a template version the router read whole becomes its
// copy (promote).
func (f Fetcher) readConverted(ctx context.Context, s Source, body []byte, headers http.Header, origin string, inventory nodeInventory, branch *templateBranch) (Profile, error) {
	// switched is why the router sent a subscription subconv left on its own rules to the
	// template; unusable, that the router cannot use the template's rule sets, nor its copy's.
	var switched *TemplateUse
	unusable := false
	// The subscription's proxy providers download once: the readings after the first, which the
	// template stage leaves within the refresh's reserve, take what the first got.
	downloads := map[string][]byte{}
	for {
		c, err := convert(ctx, body, s.Rules, branch.Client)
		if err != nil {
			// The notices tell a subscription of Snell nodes without a psk apart (credentialIssue).
			p := Profile{SchemaVersion: SchemaVersion, Source: s.ID}
			p.converted(c)
			return p, err
		}
		var template map[string][]byte
		if c.template != nil && !unusable {
			template, err = branch.sets(ctx, c.template.Version, object(c.doc["rule-providers"]))
			if err := ctx.Err(); err != nil {
				return Profile{}, err
			}
			// The version the hosts serve did not arrive whole within the template stage: route
			// by the router's copy of the template, as when no host answers.
			if err != nil && !c.template.Cached && branch.offline() {
				continue
			}
		}
		if c.template != nil && template == nil {
			// Rule sets no host serves and the router never got, or none it can use: route by
			// default this time, as without the template.
			c.defaultRouting()
			c.notices = append(c.notices, Notice{Code: "subconv_template_unavailable"})
		}
		f.sourceID = s.ID
		sets, expansion, err := f.expandSourceProviders(ctx, c.doc, origin, template, downloads)
		if errors.As(err, new(templateSetError)) {
			// The router cannot use the rule sets of the version the hosts serve, which subconv
			// and the manifest accepted: as when they did not arrive, the router's copy, and
			// failing that, default routing.
			if online := branch.offline(); !online || c.template.Cached {
				unusable = true
			}
			continue
		}
		if err != nil {
			if provider := FailureProvider(err); provider != nil {
				provider.Warnings = templateWarningSummary(headers)
			}
			return Profile{}, err
		}
		if err := ctx.Err(); err != nil {
			return Profile{}, err
		}
		p, err := parseVerified(s.ID, c, sets, expansion...)
		if t := c.routerTemplate(p); err == nil && t != nil {
			// subconv kept the subscription's own rules, but the router cannot route by enough of
			// them: the template, as subconv would have taken it, with the reason and counts that
			// include the rules the router dropped.
			s.Rules, switched = RoutingTemplate, t
			continue
		}
		if err == nil && template != nil && branch.promote() != nil {
			// The refresh routes by the version all the same; a later one saves it again.
			p.notice("template_cache_write_failed", "", false)
		}
		if switched != nil && p.Template != nil {
			p.Template.Reason, p.Template.Dropped, p.Template.Total = switched.Reason, switched.Dropped, switched.Total
		}
		p.Title = subscriptionTitle(headers)
		if s.Kind == "platform" {
			_, params, _ := mime.ParseMediaType(headers.Get("Content-Disposition"))
			if p.Format == "clash" && (strings.EqualFold(params["filename"], "hyper.yaml") || strings.EqualFold(params["filename"], "hyper.yml")) {
				p.Format = "hyper"
			}
			if err == nil {
				inventory.reportOmissions(&p)
			}
		}
		if warnings := templateWarningSummary(headers); warnings != "" {
			p.notice("platform_routing_warnings", warnings, false)
		}
		return p, err
	}
}

func templateWarningSummary(headers http.Header) string {
	total, err := strconv.ParseUint(headers.Get("X-Routing-Warnings"), 10, 32)
	if err != nil || total == 0 {
		return ""
	}
	fallback := strconv.FormatUint(total, 10)
	raw := headers.Get("X-Routing-Warning-Codes")
	if len(raw) == 0 || len(raw) > 384 {
		return fallback
	}
	known := set("NO_GROUP_NODES UNSUPPORTED_SOURCE_RULES GLOBAL_TEST_URL UNSUPPORTED_RULE NODE_SUBSCRIPTION_REQUIRED UNSUPPORTED_GROUP REJECT_CANDIDATE STATIC_REJECT NODE_LIST_ONLY CUSTOM_TEMPLATE NATIVE_LINK_UNAVAILABLE UNSUPPORTED_NODE_OPTIONS NAIVE_CERTIFICATE_TRUST")
	var parts []string
	seen := map[string]bool{}
	var sum, unspecified uint64
	for _, entry := range strings.Split(raw, ",") {
		code, num, _ := strings.Cut(strings.TrimSpace(entry), "=")
		n, err := strconv.ParseUint(num, 10, 32)
		if code == "" || len(code) > 64 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_0123456789") != "" ||
			seen[code] || err != nil || n == 0 {
			return fallback
		}
		seen[code] = true
		sum += n
		if known[code] {
			parts = append(parts, code+"="+strconv.FormatUint(n, 10))
		} else {
			unspecified += n
		}
	}
	if sum != total || len(parts) == 0 {
		return fallback
	}
	if unspecified > 0 {
		parts = append(parts, "OTHER="+strconv.FormatUint(unspecified, 10))
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

func (f Fetcher) Refresh(ctx context.Context, store *Store, id string, file []byte, policy Policy) (candidate Candidate, failure error) {
	f = f.withEgress()
	if f.Routing != nil && *f.Routing != RoutingOwn && *f.Routing != RoutingTemplate {
		return Candidate{}, errors.New("invalid_source")
	}
	t, r, e := store.begin(ctx, id, policy.ClockRetryGeneration, policy.Scheduled)
	if e != nil {
		return Candidate{}, e
	}
	// Keep only a stable diagnostic code. A newer refresh, logout or account
	// change always wins and a transport error cannot overwrite its history.
	defer func() {
		var reason string
		// The subscription was read but its update not applied, which ends an earlier failure.
		// A scheduled refresh leaves the update on the source for the administrator; one they
		// ran themselves has shown it to them. The update of a replacement link or of another
		// routing choice is not the source's.
		reviewed := failure == nil && candidate.Decision != "ready" && f.ReplacementURL == "" && (f.Routing == nil || *f.Routing == r.Config.Rules)
		switch {
		case errors.Is(ctx.Err(), context.Canceled), errors.Is(failure, context.Canceled), errors.Is(failure, ErrSuperseded), errors.Is(failure, ErrRevoked):
			return
		case failure != nil:
			reason = failure.Error()
		case !reviewed:
			return
		case candidate.Decision == "blocked":
			reason = candidate.Profile.credentialIssue()
		}
		if len(reason) > 60 || (reason == "" && failure != nil) || strings.Trim(reason, "abcdefghijklmnopqrstuvwxyz_0123456789") != "" {
			reason = "update_failed"
		}
		diagnosticCtx := ctx
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			diagnosticCtx = context.WithoutCancel(ctx)
		}
		_ = store.updateContext(diagnosticCtx, id, func(r *record) error {
			if e := check(r, t); e != nil {
				return e
			}
			before := failureDigest(r)
			r.LastFailure = reason
			r.LastTLS = tlsdiag.From(failure)
			r.ClockRetryGeneration = 0
			if r.LastTLS != nil && r.Config != nil && r.Config.Kind != "file" && len(file) == 0 && f.ReplacementURL == "" && policy.Confirm == "" {
				r.ClockRetryGeneration = t.Generation
			}
			r.LastProvider = FailureProvider(failure)
			r.LastProviderScope = ""
			if reviewed {
				r.PendingUpdate = nil
				if policy.Scheduled {
					r.PendingUpdate = candidate.Pending()
				}
			}
			// A retry that ends the same way leaves the record as it was.
			if failureDigest(r) == before {
				return errUnchanged
			}
			return nil
		})
	}()
	if r.Config == nil {
		return Candidate{}, ErrNotReady
	}
	src := *r.Config
	if f.ReplacementURL != "" {
		if src.Kind != "external" {
			return Candidate{}, errors.New("external_source_required")
		}
		if _, err := webURL(f.ReplacementURL, false); err != nil {
			return Candidate{}, err
		}
		src.URL = f.ReplacementURL
	}
	if f.Routing != nil {
		src.Rules = *f.Routing
	}
	var usage *Usage
	var receipt providerReceipt
	f.previous = r

	f.providerReceipt = &receipt
	f.reuseProviders = policy.Scheduled && f.ReplacementURL == "" && r.LastFailure == ""
	p, e := f.readProfile(ctx, src, file, &usage)
	if err := ctx.Err(); err != nil && (e == nil || errors.Is(err, context.Canceled)) {
		return Candidate{}, err
	}
	if e != nil && !(errors.Is(e, errNoUsableNodes) && p.credentialIssue() != "") {
		if src.Kind == "platform" && errors.Is(e, errAccountRevoked) {
			ids, err := store.withdraw(t.Source, &t)
			if err != nil {
				return Candidate{}, err
			}
			return Candidate{}, &AccountWithdrawal{Sources: ids}
		}
		if src.Kind == "platform" && errors.Is(e, ErrSuspended) {
			if err := store.Suspend(t); err != nil {
				return Candidate{}, err
			}
		}
		if src.Kind == "platform" && errors.Is(e, ErrRevoked) {
			if err := store.RevokeIfCurrent(t); err != nil {
				return Candidate{}, err
			}
		}
		if src.Kind == "platform" && errors.Is(e, ErrLoginRequired) {
			if err := store.RequireLogin(t); err != nil {
				return Candidate{}, err
			}
		}
		return Candidate{}, e
	}
	policy.AllowLocalFallback = src.AllowLocalFallback
	c := Assess(p, r.Current, policy)
	if c.Decision == "confirmation_required" && r.Current != nil {
		// An update that needs confirmation yet routes as the current profile (Profile.routing)
		// asks nothing new: its notices repeat those of the routing the router already runs,
		// which the administrator confirmed, or which needed no confirmation then. It goes on as
		// confirmed, so that a subscription whose every download drops a rule the router cannot
		// carry out, a REJECT one say, is found unchanged by its scheduled refreshes instead of
		// held for the administrator again each time. The profile compared is the one the
		// confirmation would commit: one falling back to local routing is committed without its
		// rules. A blocked update stays blocked even then, as nobody confirms it: credential
		// trouble blocks only an update that loses nodes, which cannot route the same, and an
		// active target it lacks (Policy) is a reference of the router's that the current
		// profile lacks too, which the administrator has to change.
		confirmed := policy
		confirmed.Confirm = c.ConfirmationID
		// Assess appends to the notices: keep its second reading's apart from the first's.
		p.Notices = slices.Clip(p.Notices)
		if same := Assess(p, r.Current, confirmed); same.Decision == "ready" && digest(same.Profile.routing()) == digest(r.Current.routing()) {
			c = same
		}
	}
	c.Usage = usage
	if !receipt.Expires.IsZero() && !c.Profile.Local && len(c.Notices) == 0 {
		c.receipt = &receipt
	}
	if c.Decision != "ready" {
		if !store.Current(t) {
			return Candidate{}, ErrSuperseded
		}
		return c, nil
	}
	// A profile that routes as the usable one (Profile.routing) leaves the router as it is: it
	// needs no check, keeps the rollback snapshot and tells nobody (Unchanged), and the commit
	// writes only the source's status. This includes a new template version with the same rules
	// and rule sets: the profile keeps the version it was committed with, by which the router
	// decides whether open connections stay (router.profileRouting), so they stay; the status
	// shows the version found. Hash the serialized form because stored rule values use
	// json.Number.
	c.unchanged = r.Current != nil && !r.Suspended &&
		(f.ReplacementURL == "" || f.ReplacementURL == r.Config.URL) &&
		digest(c.Profile.routing()) == digest(r.Current.routing())
	var confirm func() error
	if !c.unchanged && f.BeforeCommit != nil {
		if !store.Current(t) {
			return Candidate{}, ErrSuperseded
		}
		confirm, e = f.BeforeCommit(ctx, c.Profile)
		if e != nil {
			return Candidate{}, e
		}
		if confirm == nil {
			return Candidate{}, errors.New("preflight_revision_missing")
		}
	}
	e = store.commit(ctx, t, c, confirm, f.ReplacementURL, f.Routing)
	if errors.Is(e, ErrRevoked) {
		// Another operation withdrew or replaced the source during this download.
		// It already owns the notification for that change.
		return Candidate{}, ErrSuperseded
	}
	if e != nil {
		return Candidate{}, e
	}
	return c, nil
}

// An unavailable plan can be temporarily over its traffic limit. Confirm a
// permanent withdrawal with the signed-in account's billing inventory.
func (f Fetcher) entitlement(ctx context.Context, s Source, knownUnavailable bool) error {
	ambiguous := func() error {
		if knownUnavailable {
			return ErrSuspended
		}
		return errors.New("entitlement_check_failed")
	}

	plans, err := f.ownedPlans(ctx, s.Origin, s.Token, s.UserID)
	if err != nil {
		if tlsdiag.From(err) != nil || errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrRevoked) || errors.Is(err, errAccountRevoked) {
			return err
		}
		return ambiguous()
	}
	for _, plan := range plans {
		if plan.ID == s.PlanID {
			switch plan.Status {
			case "disabled":
				return ErrRevoked
			case "expired":
				return ErrSuspended
			case "active":
				if !plan.Usable {
					return ErrSuspended
				}
			}
			return ambiguous()
		}
	}
	return ErrRevoked
}

// subscriptionTitle reads the name a provider gives its subscription: Profile-Title (plain
// or "base64:"), else the Content-Disposition file name without its extension.
func subscriptionTitle(headers http.Header) string {
	title := strings.TrimSpace(headers.Get("Profile-Title"))
	if rest, ok := strings.CutPrefix(title, "base64:"); ok {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		title = ""
		if err == nil {
			title = strings.TrimSpace(string(raw))
		}
	}
	if title == "" {
		if _, params, err := mime.ParseMediaType(headers.Get("Content-Disposition")); err == nil {
			name := filepath.Base(params["filename"])
			title = strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
			if title == "." || title == "/" {
				title = ""
			}
		}
	}
	if len(title) > 64 || !safeLabel(title) || publicLabel(title, "") == "" {
		return ""
	}
	return title
}
