// Package subscription normalizes private subscription inputs without performing network I/O.
package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HyperPteLtd/openwrt-hyper/internal/proxyspec"
)

const MaxBody = 8 << 20
const maxNodes = 4096
const maxRules = 50000
const SchemaVersion = 2

var errNoUsableNodes = errors.New("no_usable_nodes")

// Outbound and native share credentials are private. Only Summary is suitable for RPC.
type Node struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Protocol string         `json:"protocol"`
	Server   string         `json:"server"`
	Port     int            `json:"port"`
	Outbound map[string]any `json:"outbound"`
	Special  bool           `json:"special,omitempty"`
}
type Member struct {
	Capability   string `json:"capability,omitempty"`
	Availability string `json:"availability,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	Special      bool   `json:"special,omitempty"`
	TCPProbe     bool   `json:"tcp_probe"`
}

// Dialer is the ID of the node or group a chained node connects through, or empty.
func (n Node) Dialer() string { return field(n.Outbound, "dialer-proxy") }

func (n Node) Summary() Member {
	_, _, tcp := n.TCPProbeEndpoint()
	return Member{Capability: n.CapabilityKey(), ID: n.ID, Name: publicLabel(n.Name, "Node "+n.Protocol), Protocol: n.Protocol, Special: n.Special, TCPProbe: tcp}
}

type Group struct {
	Hidden  bool           `json:"hidden,omitempty"`
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Members []string       `json:"members"`
	Options map[string]any `json:"options,omitempty"`
}
type Rule struct {
	Match   map[string]any `json:"match"`
	Target  string         `json:"target"`
	RuleSet string         `json:"rule_set,omitempty"`
}

// SetTags includes both expanded inline entries and binary multi-set matches.
func (r Rule) SetTags() []string {
	seen := map[string]bool{}
	var result []string
	tags, _ := textlist(r.Match["rule_set"])
	for _, s := range append(tags, r.RuleSet) {
		if s != "" && !seen[s] {
			result = append(result, s)
			seen[s] = true
		}
	}
	return result
}

type Notice struct {
	Resource  string `json:"resource,omitempty"`
	Code      string `json:"code"`
	At        string `json:"at,omitempty"`
	Dangerous bool   `json:"dangerous,omitempty"`
}
type Profile struct {
	SchemaVersion int         `json:"schema_version"`
	BinarySets    []BinarySet `json:"binary_sets,omitempty"`
	Source        string      `json:"source"`
	Format        string      `json:"format"`
	Nodes         []Node      `json:"nodes"`
	Groups        []Group     `json:"groups"`
	Rules         []Rule      `json:"rules"`
	Final         string      `json:"final,omitempty"`
	Notices       []Notice    `json:"notices,omitempty"`
	Local         bool        `json:"local"`
	Title         string      `json:"title,omitempty"`
	// Template is set when the profile routes by subconv's template instead of its own rules.
	Template *TemplateUse `json:"template,omitempty"`
	// RoutingChoice is the source's routing choice (Source.Rules) the profile was read with,
	// which the commit records and a rollback restores with the profile. Profiles stored
	// before it was recorded have none: a rollback to one leaves the source's choice.
	RoutingChoice *string `json:"routing_choice,omitempty"`
	// Countries are a Hyper profile's node IDs by country, so that a read where a country
	// fails keeps that country's nodes.
	Countries map[string][]string `json:"countries,omitempty"`
	// Lists summarise a Hyper profile's rule catalog for the rules page.
	Lists []HyperList `json:"lists,omitempty"`
}

type Policy struct {
	ClockRetryGeneration uint64
	Scheduled            bool
	AllowLocalFallback   bool
	Confirm              string
	ActiveIDs            []string
	// Choices are the saved selections, group to member. One in a group the update removes
	// resets; one in a group that stays keeps its member.
	Choices map[string]string
}
type Candidate struct {
	receipt        *providerReceipt
	RetainsRouting bool
	unchanged      bool
	Usage          *Usage
	Changes        Changes
	Profile        Profile
	Decision       string
	Notices        []Notice
	ConfirmationID string
}

// Unchanged reports a successful check of an already usable profile that found it routing as
// before (Profile.routing): the router's configuration stays as it is.
func (c Candidate) Unchanged() bool { return c.unchanged }

// routing is what of a profile makes the router's configuration: all of it but what it only
// shows (notices, title, format), the routing choice it was read with and the bookkeeping of
// the template it routes by: the template's version, why it was chosen and whether the
// router's copy served. A new template version whose rules and rule sets are the same leaves
// the router's configuration as it is.
func (p Profile) routing() Profile {
	p.Notices, p.Title, p.Format, p.RoutingChoice, p.Countries, p.Lists = nil, "", "", nil, nil, nil
	if p.Template != nil {
		p.Template = &TemplateUse{}
	}
	return p
}

const Direct = "@direct"
const Reject = "@reject"

func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func ref(prefix string, v any) string            { return prefix + ":" + digest(v)[:32] }
func field(m map[string]any, name string) string { s, _ := m[name].(string); return s }
func integer(v any) (int, bool) {
	s := fmt.Sprint(v)
	n, err := strconv.Atoi(s)
	return n, err == nil
}
func hostValid(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if strings.HasSuffix(s, ".") {
		s = strings.TrimSuffix(s, ".")
	}
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Provider-controlled display names occasionally contain a full subscription
// URL or authorization header. Preserve internal identities; public names and
// notices must not reproduce those values in rpcd or exported task results.
func publicLabel(value, fallback string) string {
	lower := strings.ToLower(value)
	for _, key := range []string{"://", "bearer ", "authorization:", "token=", "password=", "psk=", "userkey=", "secret=", "api_key="} {
		if strings.Contains(lower, key) {
			return fallback
		}
	}
	return value
}
func safeLabel(s string) bool {
	return s != "" && len(s) <= 384 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
func (p *Profile) notice(code, at string, danger bool) {
	p.Notices = append(p.Notices, Notice{Code: code, At: publicLabel(at, "configuration entry"), Dangerous: danger})
}

func (p Profile) credentialIssue() string {
	for _, n := range p.Notices {
		if n.Code == errSnellCredential.Error() || n.Code == errInvalidCredential.Error() || len(p.Nodes) == 0 && n.Code == "mihomo_protocol_unsupported" {
			return n.Code
		}
	}
	return ""
}
func (p *Profile) addProxy(name, kind string, out map[string]any) error {
	host := field(out, "server")
	port, ok := proxyspec.Port(out["port"])
	if !ok || port < 1 || port > 65535 || !hostValid(host) || !safeLabel(name) {
		return errors.New("invalid_endpoint")
	}
	if len(p.Nodes) >= maxNodes {
		return errors.New("node_limit")
	}
	out["port"] = port
	out["server"] = strings.ToLower(host)
	// A Hyper node keeps its ID while the server rotates its credentials, so the router's
	// selection and failover history stay with it.
	key := []any{p.Source, kind, out}
	if p.Format == HyperSource {
		key = []any{p.Source, kind, out["server"], port, name}
	}
	id := ref("node", key)
	p.Nodes = append(p.Nodes, Node{id, name, kind, host, port, out, kind == "mtproxy"})
	return nil
}

// Parse converts a subscription body with its own routing, without downloading anything.
func Parse(sourceID string, body []byte) (Profile, error) {
	c, err := convert(context.Background(), body, RoutingOwn, nil)
	if err != nil {
		p := Profile{SchemaVersion: SchemaVersion, Source: sourceID}
		p.converted(c)
		return p, err
	}
	return parseVerified(sourceID, c, nil)
}

// converted records the original format and the converter's and providers' notices.
func (p *Profile) converted(c conversion, notices ...Notice) {
	if c.format != "" {
		p.Format = c.format
	}
	p.Template = c.template
	p.Notices = slices.Concat(c.notices, notices, p.Notices)
}

// parseVerified reads a converted subscription whose providers were expanded into it (sets).
func parseVerified(sourceID string, c conversion, sets []BinarySet, notices ...Notice) (p Profile, err error) {
	p = Profile{SchemaVersion: SchemaVersion, Source: sourceID, BinarySets: sets}
	defer func() { p.converted(c, notices...) }()
	if !safeLabel(sourceID) {
		return p, errors.New("invalid_subscription")
	}
	if err := parseClash(&p, c.doc); err != nil {
		return p, err
	}
	if len(p.Nodes) == 0 {
		return p, errNoUsableNodes
	}
	resolve(&p)
	if len(p.Nodes) == 0 {
		// Every node was a broken chain.
		return p, errNoUsableNodes
	}
	return p, nil
}

// Notices of rules a profile goes without while keeping the rest of its routing.
var partialNotices = set("mihomo_unsupported_rule mihomo_unsupported_rule_set subconv_unsupported_rule subconv_unsupported_rule_set rule_provider_reference_unavailable rule_provider_custom_geo rejection_removed")

func Assess(p Profile, previous *Profile, policy Policy) Candidate {
	if previous != nil && previous.Source != p.Source {
		return Candidate{Decision: "blocked", Notices: []Notice{{Code: "source_mismatch"}}}
	}
	if previous != nil && !previous.Local && len(previous.Rules)+len(previous.Groups) > 0 && len(p.Rules)+len(p.Groups) == 0 {
		p.notice("routing_disappeared", "", false)
	}
	active := map[string]bool{}
	for _, n := range p.Nodes {
		active[n.ID] = true
	}
	if p.credentialIssue() != "" {
		if len(p.Nodes) == 0 {
			return Candidate{Profile: p, Decision: "blocked", Notices: p.Notices}
		}
		if previous != nil {
			for _, n := range previous.Nodes {
				if !active[n.ID] {
					return Candidate{Profile: p, Decision: "blocked", Notices: p.Notices}
				}
			}
		}
	}
	for _, g := range p.Groups {
		active[g.ID] = true
	}
	refs := slices.Clone(policy.ActiveIDs)
	for group, member := range policy.Choices {
		if (!strings.HasPrefix(group, "group:") || active[group]) && (strings.HasPrefix(member, "node:") || strings.HasPrefix(member, "group:")) {
			refs = append(refs, member)
		}
	}
	for _, id := range refs {
		if !active[id] && id != Direct && id != Reject {
			p.notice("active_target_missing", "", false)
			return Candidate{Profile: p, Decision: "blocked", Notices: p.Notices, Changes: changes(previous, p, refs)}
		}
	}
	omittedReference := slices.ContainsFunc(p.Notices, func(n Notice) bool { return n.Code == "rule_provider_reference_unavailable" })
	lostRoute := false
	if previous != nil {
		have := map[string]bool{}
		for _, r := range p.Rules {
			if r.Target == Reject || omittedReference {
				have[digest(r)] = true
			}
		}
		for _, r := range previous.Rules {
			if omittedReference && !have[digest(r)] {
				lostRoute = true
			}
			if r.Target == Reject && !have[digest(r)] {
				p.notice("rejection_removed", "", true)
				break
			}
		}
		if previous.Final == Reject && p.Final != Reject {
			p.notice("rejection_removed", "final", true)
		}
	}
	fallback, partial, danger := false, false, false
	for _, n := range p.Notices {
		switch n.Code {
		case "nonstandard_snell_credential", "invalid_credential":
			continue
		case "mihomo_router_field_ignored", "mihomo_protocol_unsupported", "mihomo_unavailable_member", "unsupported_node", "invalid_endpoint", "node_missing_credential", "platform_routing_warnings", "unavailable_inventory",
			"dialer_proxy_unavailable", "dialer_proxy_cycle", "provider_option_ignored", "provider_option_unsafe", "template_cache_write_failed":
			continue
		}
		switch {
		case partialNotices[n.Code]:
			partial = true
		case strings.HasPrefix(n.Code, "subconv_"):
			// The converter's other warnings cost a node, group or option, not the routing.
			continue
		default:
			fallback = true
		}
		danger = danger || n.Dangerous
	}
	if fallback && !danger && (confirmedLoss(p) || previous != nil && confirmedLoss(*previous)) {
		p.notice("local_fallback_removes_rejection", "", true)
		danger = true
	}
	c := Candidate{Profile: p, Decision: "ready", Notices: p.Notices, Changes: changes(previous, p, refs)}
	// Custom router rules can refer to any existing rule-set tag. A partial
	// refresh must not silently discard a previously usable set, even when its
	// subscription rules route through an ordinary (non-reject) outbound.
	if partial && previous != nil {
		available := map[string]bool{}
		for _, rs := range p.BinarySets {
			available[rs.Tag] = true
		}
		for _, rs := range previous.BinarySets {
			if !available[rs.Tag] {
				danger = true
				break
			}
		}
	}
	if danger || lostRoute || fallback && !policy.AllowLocalFallback {
		previousDigest := ""
		if previous != nil {
			previousDigest = digest(*previous)
		}
		id := digest([]string{digest(p), previousDigest})
		if policy.Confirm != id {
			c.RetainsRouting = partial && !fallback && (len(p.Rules)+len(p.Groups) > 0 || p.Final != "")
			c.Decision = "confirmation_required"
			c.ConfirmationID = id
			return c
		}
	}
	if fallback || len(p.Groups) == 0 && len(p.Rules) == 0 && p.Final == "" {
		c.Profile.Local = true
		c.Profile.Final = ""
		c.Profile.Rules = nil
		c.Profile.Groups = nil
		c.Profile.BinarySets = nil
		c.Profile.Template = nil
		c.Changes = changes(previous, c.Profile, refs)
	}
	return c
}
