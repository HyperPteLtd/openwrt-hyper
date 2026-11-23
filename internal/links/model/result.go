package model

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Format names a subscription's format.
type Format string

const (
	FormatSingBox Format = "sing-box"
	FormatClash   Format = "clash"
	// Stash's dialect of Clash.
	FormatStash Format = "stash"
	// Share links, one per line, often Base64 as a whole.
	FormatLinks        Format = "links"
	FormatSurge        Format = "surge"
	FormatSurfboard    Format = "surfboard"
	FormatLoon         Format = "loon"
	FormatShadowrocket Format = "shadowrocket"
	FormatQuantumultX  Format = "quantumultx"
	// Shadowsocks's JSON server list (SIP008).
	FormatSIP008 Format = "sip008"
	// ShadowsocksD's ssd:// subscription.
	FormatSSD Format = "ssd"
	// Xray and V2Ray JSON configs.
	FormatXray Format = "xray"
)

// Warning reports something the conversion left out or changed. Code is stable; Detail names
// the item.
type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Warning codes. A client should flag unsupported_rule and unsupported_rule_set: the routing
// differs from what the subscription meant.
const (
	// An outbound or proxy of a type the mihomo core has no counterpart for; it is left out.
	WarnUnsupportedProxy = "unsupported_proxy"
	// An option of a supported proxy that cannot be represented; the proxy is left out.
	WarnUnsupportedOption = "unsupported_option"
	// A proxy that is broken: no server or port, or a value mihomo refuses. It is left out.
	WarnInvalidProxy = "invalid_proxy"
	// A proxy whose credentials are missing or not of its protocol (a password where Snell
	// takes a psk); it is left out.
	WarnInvalidCredential = "invalid_credential"
	// An option that was removed while the item it belongs to was kept: an option the client
	// decides (interface-name, routing-mark), a local file, or a value mihomo refuses.
	WarnIgnoredOption = "ignored_option"
	// A rule left out: a condition or action with no Clash counterpart, a malformed rule, or a
	// rule whose policy or rule set does not exist.
	WarnUnsupportedRule = "unsupported_rule"
	// A rule set mihomo cannot read or reach; the rules that use it are left out.
	WarnUnsupportedRuleSet = "unsupported_rule_set"
	// A proxy provider that cannot be used, such as one without a web URL; groups stop using it.
	WarnUnsupportedProvider = "unsupported_provider"
	// A proxy or rule provider that reads a file on the client's disk. It is kept; the client
	// decides whether to allow it.
	WarnLocalProvider = "local_provider"
	// A group of a type mihomo lacks, or a relay that cannot become a dialer-proxy chain; it is
	// left out and references to it are removed.
	WarnUnsupportedGroup = "unsupported_group"
	// A group member or dialer-proxy that names something left out or never defined; the
	// reference is removed.
	WarnUnknownReference = "unknown_reference"
	// Groups or dialer-proxy chains that refer to each other in a loop; one reference is
	// removed to break it.
	WarnCyclicReference = "cyclic_reference"
	// A group left with no members; it is left out and references to it are removed.
	WarnEmptyGroup = "empty_group"
	// An empty, duplicate or reserved name that was given a unique one.
	WarnRenamed = "renamed"
	// Build replaced the subscription's groups and rules with the routing template; the detail
	// says why (a Template* reason).
	WarnTemplateApplied = "template_applied"
	// Build would have applied the routing template but has none: no host served it and the
	// cache holds none, or it was invalid. The subscription's own groups and rules stay.
	WarnTemplateUnavailable = "template_unavailable"
)

// Why Build applied the routing template: TemplateUse.Reason and the detail of
// WarnTemplateApplied.
const (
	TemplateRequested = "requested"     // Options.Rules asked for it
	TemplateNoRules   = "no_rules"      // the subscription has no rules
	TemplateNoMatch   = "no_match"      // its final rule did not survive
	TemplateDropped   = "dropped_rules" // more than a tenth of its rules were left out
)

// TemplateUse reports the routing template Build applied.
type TemplateUse struct {
	// Version is the rules branch's manifest version the template comes from.
	Version int64 `json:"version"`
	// Reason is one of the Template* reasons.
	Reason string `json:"reason"`
	// Cached says no host served the template, and the client's cached copy was used.
	Cached bool `json:"cached,omitempty"`
}

// Result holds the converted parts, ready to place in a mihomo config.
type Result struct {
	Format         Format    `json:"format"`
	Proxies        []Object  `json:"proxies"`
	ProxyProviders Object    `json:"proxy-providers,omitempty"`
	Groups         []Object  `json:"proxy-groups"`
	RuleProviders  Object    `json:"rule-providers"`
	Rules          []string  `json:"rules"`
	Warnings       []Warning `json:"warnings"`
	// How many of the subscription's rules the conversion kept.
	RuleStats RuleStats `json:"rule_stats"`
	// The routing template Build applied, if any.
	Template *TemplateUse `json:"template,omitempty"`
	// Head, from Build with Options.Head, is the settings YAML renders before the parts, making
	// a complete config.
	Head string `json:"head,omitempty"`
}

// RuleStats count a subscription's rules, for a client to judge whether to use them or a
// routing template of its own. Rules count as Clash rules: a sing-box or Xray rule that tests
// several values counts once per value, and a rule left out before it became one counts once.
// Rules a client handles itself (sniffing, DNS hijacking) and rules after the final MATCH,
// which never matched, do not count.
type RuleStats struct {
	// Rules in the subscription, the final one included.
	Total int `json:"total"`
	// Rules left out: a type mihomo lacks, a policy or rule set that is gone, a malformed rule.
	Dropped int `json:"dropped"`
	// Whether the subscription's final rule (MATCH, FINAL, route.final) survived with its policy.
	Match bool `json:"match"`
}

// Warn records a warning.
func (r *Result) Warn(code, format string, args ...any) {
	r.Warnings = append(r.Warnings, Warning{code, fmt.Sprintf(format, args...)})
}

// DropRule counts a rule a format left out before it became a Clash rule.
func (r *Result) DropRule() {
	r.RuleStats.Total++
	r.RuleStats.Dropped++
}

// YAML renders the parts as a mihomo config fragment, after the head if there is one.
func (r *Result) YAML() ([]byte, error) {
	document := New(
		"proxies", toAny(r.Proxies),
		"proxy-providers", r.ProxyProviders,
		"proxy-groups", toAny(r.Groups),
		"rule-providers", r.RuleProviders,
		"rules", r.Rules,
	)
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	parts := Readable(buffer.Bytes())
	switch {
	case r.Head == "":
		return parts, nil
	case len(document) == 0:
		parts = nil // not "{}" after the head
	}
	head := r.Head
	if head[len(head)-1] != '\n' {
		head += "\n"
	}
	return append([]byte(head), parts...), nil
}

func toAny(objects []Object) []any {
	out := make([]any, len(objects))
	for index, o := range objects {
		out[index] = o
	}
	return out
}

// Options adjust a conversion; the zero value is the default.
type Options struct {
	// ConvertsRuleSets says that the caller downloads rule providers itself and passes their
	// bodies through ConvertRuleSet. Rule sets whose content mihomo cannot read (sing-box source
	// JSON, Quantumult X filter lists) are then kept as http providers instead of left out.
	ConvertsRuleSets bool
}
