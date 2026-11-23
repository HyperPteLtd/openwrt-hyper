package subscription

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/openweir/subconv"
	"github.com/openweir/subconv/rules"
	"go.yaml.in/yaml/v3"
)

// conversion is a subscription in the Hyper format: the Clash document subconv makes of a
// body in any format. The router checks and compiles nothing else.
type conversion struct {
	doc      map[string]any
	format   string
	notices  []Notice
	template *TemplateUse
	// auto is subconv's count of the subscription's rules when it chose them over the template
	// itself (RoutingOwn), for the router to weigh what it drops on top (routerTemplate).
	auto *subconv.RuleStats
}

var (
	errEmptySubscription = errors.New("subscription_empty")
	errWebPage           = errors.New("subscription_web_page")
	errMessage           = errors.New("subscription_message")
	errUnknownFormat     = errors.New("unsupported_format")
)

// convert runs a subscription body through subconv. Rule sets whose content only the router
// can convert stay, since it downloads them itself (ruleSet). With the rules branch (branch),
// subconv puts its routing template in place of the subscription's groups and rules when the
// source asks for it (routing) or the subscription's own routing is missing or too lossy.
// The router writes the rest of the config itself.
func convert(ctx context.Context, body []byte, routing string, branch *rules.Client) (conversion, error) {
	if len(body) > MaxBody {
		return conversion{}, errors.New("subscription_size_limit")
	}
	options := subconv.Options{ConvertsRuleSets: true, Rules: subconv.RulesAuto, Client: branch}
	switch {
	case branch == nil:
		options.Rules = subconv.RulesSubscription
	case routing == RoutingTemplate:
		options.Rules = subconv.RulesTemplate
	}
	result, err := subconv.Build(ctx, body, options)
	switch {
	case ctx.Err() != nil:
		return conversion{}, ctx.Err()
	case errors.Is(err, subconv.ErrEmpty):
		return conversion{}, errEmptySubscription
	case errors.Is(err, subconv.ErrWebPage):
		return conversion{}, errWebPage
	case errors.Is(err, subconv.ErrMessage):
		return conversion{}, errMessage
	case errors.Is(err, subconv.ErrUnknownFormat):
		return conversion{}, errUnknownFormat
	case err != nil:
		return conversion{}, errors.New("invalid_subscription")
	}
	c := conversion{format: formatName(result.Format)}
	unavailable := slices.ContainsFunc(result.Warnings, func(w subconv.Warning) bool { return w.Code == subconv.WarnTemplateUnavailable })
	// Whether the subscription routes by its own groups and rules: the template and the
	// router's default replace them, and what was lost of them no longer matters.
	own := result.Template == nil && !unavailable
	if own && options.Rules == subconv.RulesAuto {
		stats := result.RuleStats
		c.auto = &stats
	}
	for _, w := range result.Warnings {
		switch w.Code {
		case subconv.WarnTemplateApplied:
			// The profile records it (Profile.Template).
			continue
		case subconv.WarnUnsupportedRule, subconv.WarnUnsupportedRuleSet, subconv.WarnUnsupportedGroup, subconv.WarnEmptyGroup:
			if !own {
				continue
			}
		}
		c.notices = append(c.notices, warningNotice(w))
	}
	if t := result.Template; t != nil {
		c.template = &TemplateUse{Version: t.Version, Reason: t.Reason, Cached: t.Cached}
		if t.Reason == subconv.TemplateDropped {
			c.template.Dropped, c.template.Total = result.RuleStats.Dropped, result.RuleStats.Total
		}
	}
	if len(result.Proxies) == 0 && len(result.ProxyProviders) == 0 {
		return c, errNoUsableNodes
	}
	if c.doc, err = document(result); err != nil {
		return c, err
	}
	if unavailable {
		c.defaultRouting()
	}
	if result.Format == subconv.FormatClash || result.Format == subconv.FormatStash {
		c.notices = append(c.notices, clashFields(body, c.doc, own)...)
	}
	return c, nil
}

// defaultRouting drops routing the subscription's own rules could not be trusted with, or that
// the source declined, for the router's default: mainland direct, the rest proxied. A later
// refresh tries the template again.
func (c *conversion) defaultRouting() {
	for _, k := range []string{"proxy-groups", "rule-providers", "rules"} {
		delete(c.doc, k)
	}
	c.template = nil
}

// routerTemplate says why a subscription that subconv left on its own rules (auto) needs the
// template after all, once the router has read it (p), or nil while its routing holds. The
// router leaves out rules of its own (custom geodata, a rule set it has not got, a rule or
// target it cannot route), and they count with subconv's by subconv's measure: the final rule
// lost, or more than a tenth of the rules.
func (c conversion) routerTemplate(p Profile) *TemplateUse {
	if c.auto == nil {
		return nil
	}
	routed := 0
	for _, r := range p.Rules {
		if r.Target != "" {
			routed++
		}
	}
	stats := *c.auto
	// subconv kept the final rule last, and the router reads it as the profile's.
	stats.Dropped += max(len(list(c.doc["rules"]))-1-routed, 0)
	if p.Final == "" {
		stats.Dropped++
		stats.Match = false
	}
	switch {
	case !stats.Match:
		return &TemplateUse{Reason: subconv.TemplateNoMatch}
	case stats.Dropped*10 > stats.Total:
		return &TemplateUse{Reason: subconv.TemplateDropped, Dropped: stats.Dropped, Total: stats.Total}
	}
	return nil
}

// document decodes subconv's output as the router's YAML reads any Clash document.
func document(r *subconv.Result) (map[string]any, error) {
	b, err := r.YAML()
	if err != nil {
		return nil, errors.New("invalid_subscription")
	}
	var doc map[string]any
	if yaml.Unmarshal(b, &doc) != nil {
		return nil, errors.New("invalid_subscription")
	}
	return doc, nil
}

// formatName is the LuCI name of a subconv format.
func formatName(f subconv.Format) string {
	if f == subconv.FormatLinks {
		return "uri"
	}
	return string(f)
}

// warningNotice turns a subconv warning into a notice. A dropped rule or rule set changes where
// traffic goes, so the user confirms it. A Snell node without its psk blocks an update that
// would lose it (credentialIssue), as other missing credentials do not. The rest inform.
func warningNotice(w subconv.Warning) Notice {
	n := Notice{Code: "subconv_" + w.Code, At: publicLabel(w.Detail, "")}
	switch w.Code {
	case subconv.WarnUnsupportedRule:
		// As before the switch, only a dropped rejection needs confirming: a subscription that
		// always loses some other rule would otherwise stop at every scheduled refresh.
		n.Dangerous = dropsRejection(w.Detail)
	case "invalid_credential":
		name, problem := w.Detail, ""
		if i := strings.LastIndex(w.Detail, ": "); i >= 0 {
			name, problem = w.Detail[:i], w.Detail[i+2:]
		}
		n.At = publicLabel(name, "")
		switch problem {
		case "a password but no psk":
			n.Code = errSnellCredential.Error()
		case "no psk":
			n.Code = errInvalidCredential.Error()
		default:
			n.Code = "node_missing_credential"
		}
	}
	return n
}

// dropsRejection reports whether a dropped rule, given as "<rule as written>: <problem>" or
// "rule <n> to <target>: <problem>", rejected traffic (rejects).
func dropsRejection(detail string) bool {
	line, _, _ := strings.Cut(detail, ": ")
	// A rule dropped with its rule set reads "rule <n> to <target>".
	if i := strings.LastIndex(line, " to "); i >= 0 && !strings.Contains(line, ",") {
		line = line[i+4:]
	}
	return rejects(line)
}

// rejects reports whether a rule as written rejects traffic: whether its target, the last field
// before any trailing options, is REJECT or a variant. A rule the router drops itself is weighed
// the same way.
func rejects(rule string) bool {
	fields := strings.Split(rule, ",")
	target := ""
	for i := len(fields) - 1; i >= 0; i-- {
		if target = strings.TrimSpace(fields[i]); !strings.EqualFold(target, "no-resolve") && !strings.EqualFold(target, "src") {
			break
		}
	}
	// REJECT and its variants: REJECT-DROP, Surge's REJECT-TINYGIF, Quantumult X's reject-200...
	return strings.HasPrefix(strings.ToUpper(target), "REJECT")
}

// Top-level Clash fields the router decides itself.
var routerFields = set("external-controller external-controller-tls external-controller-unix external-controller-pipe external-ui external-ui-url secret tun listeners iptables ebpf tunnels")

// clashFields reports top-level fields of a Clash body that subconv leaves out without a word:
// the router's own settings and, while the subscription routes by its own rules (own), a Clash
// Premium script, which routed by code, and custom geodata, whose GEOIP and GEOSITE names the
// router cannot honor (clashDefaultGeo).
func clashFields(body []byte, doc map[string]any, own bool) []Notice {
	var notices []Notice
	lines := bufio.NewScanner(bytes.NewReader(body))
	lines.Buffer(nil, MaxBody)
	for lines.Scan() {
		key, _, ok := strings.Cut(lines.Text(), ":")
		switch {
		case !ok || key != strings.TrimSpace(key):
		case routerFields[key]:
			notices = append(notices, Notice{Code: "mihomo_router_field_ignored", At: key})
		case !own:
		case key == "script":
			notices = append(notices, Notice{Code: "script", At: key, Dangerous: true})
		case key == "geox-url":
			var raw struct {
				Geo map[string]any `yaml:"geox-url"`
			}
			if yaml.Unmarshal(body, &raw) != nil || raw.Geo == nil {
				// Unreadable: no GEO rule may assume the default data.
				raw.Geo = map[string]any{"geoip": "", "geosite": "", "mmdb": "", "asn": ""}
			}
			doc["geox-url"] = raw.Geo
		}
	}
	return notices
}
