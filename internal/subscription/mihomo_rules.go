package subscription

import (
	"fmt"
	"strings"
)

// The router's mainland lists, which GEOSITE,CN and GEOIP,CN rules use.
const (
	DomesticDomainSet = "@hyper-geosite-cn"
	DomesticIPSet     = "@hyper-geoip-cn"
)

// Only the final policy changes during compilation. Preserve compound matchers
// intact, including commas within AND, OR and NOT expressions.

func parseMihomoRule(p *Profile, raw string, index int) {
	line := strings.TrimSpace(raw)
	at := fmt.Sprintf("rule %d", index)
	danger := rejects(line)
	reject := func() { p.notice("mihomo_unsupported_rule", at, danger) }
	if len(line) > maxClashRuleLine || strings.ContainsAny(line, "\r\n\x00") {
		reject()
		return
	}
	fields := strings.Split(line, ",")
	if len(fields) < 2 {
		reject()
		return
	}
	last := len(fields) - 1
	opts, src := false, false
	for last > 1 {
		v := strings.ToLower(strings.TrimSpace(fields[last]))
		if v == "no-resolve" && !opts {
			opts = true
		} else if v == "src" && !src {
			src = true
		} else {
			break
		}
		last--
	}
	if last < 1 {
		reject()
		return
	}
	kind := strings.ToUpper(strings.TrimSpace(fields[0]))
	tgt := clashTarget(strings.TrimSpace(fields[last]))
	if !safeLabel(tgt) || p.Final != "" {
		reject()
		return
	}
	if kind == "MATCH" && last == 1 && !opts && !src {
		p.Final = tgt
		return
	}
	if last < 2 || !nativeRuleTypes[kind] {
		reject()
		return
	}
	prefix := strings.TrimSpace(strings.Join(fields[:last], ","))
	rule := Rule{Match: map[string]any{"clash": prefix}, Target: tgt}
	if src {
		rule.Match["src"] = true
	}
	if opts {
		rule.Match["no_resolve"] = true
	}
	if last == 2 && strings.EqualFold(strings.TrimSpace(fields[1]), "CN") && (kind == "GEOIP" || kind == "GEOSITE") {
		rule.RuleSet = DomesticIPSet
		if kind == "GEOSITE" {
			rule.RuleSet = DomesticDomainSet
		}
		rule.Match["clash"] = "RULE-SET," + rule.RuleSet
	}
	if kind == "RULE-SET" {
		if last != 2 {
			reject()
			return
		}
		rule.RuleSet = strings.TrimSpace(fields[1])
		found := false
		for _, s := range p.BinarySets {
			if s.Tag == rule.RuleSet {
				found = true
				break
			}
		}
		if !found {
			p.notice("rule_provider_reference_unavailable", at+" · "+publicLabel(rule.RuleSet, "rule set"), danger)
			return
		}
	}
	p.Rules = append(p.Rules, rule)
}

var nativeRuleTypes = set("DOMAIN DOMAIN-SUFFIX DOMAIN-KEYWORD DOMAIN-REGEX DOMAIN-WILDCARD GEOSITE GEOIP SRC-GEOIP IP-ASN SRC-IP-ASN IP-CIDR IP-CIDR6 SRC-IP-CIDR IP-SUFFIX SRC-IP-SUFFIX DST-PORT SRC-PORT IN-PORT IN-TYPE NETWORK IP-VERSION DSCP RULE-SET AND OR NOT")

// rejects reports whether a rule as written rejects traffic: whether its target, the last field
// before any trailing options, is REJECT or a variant.
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
