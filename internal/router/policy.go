package router

import (
	"errors"

	"strings"
)

func ValidatePolicy(p Policy) error {
	if e := validateRefs(p); e != nil {
		return e
	}
	if len(p.Devices) > 2048 || len(p.Rules) > 2048 {
		return errors.New("policy exceeds router limits")
	}
	validTarget := func(s string) bool {
		return s == "@direct" || s == "@reject" || s == Local || localID.MatchString(s) || strings.HasPrefix(s, "group:") && len(s) < 100
	}
	for _, d := range p.Devices {
		if _, e := deviceMatch(d.Address); e != nil || (d.Target != "@proxy" && !validTarget(d.Target)) {
			return errors.New("invalid device route")
		}
	}
	ids := map[string]bool{}
	for _, r := range p.Rules {
		if len([]rune(r.Note)) > 256 {
			return errors.New("custom rule note exceeds 256 characters")
		}
		if r.ID == "" || len(r.ID) > 128 || ids[r.ID] {
			return errors.New("invalid or duplicate custom rule identifier")
		}
		ids[r.ID] = true
		if len(r.Match) == 0 || len(r.Match) > 32 || !validTarget(r.Target) {
			return errors.New("invalid custom rule")
		}
		for k := range r.Match {
			switch k {
			case "domain", "domain_suffix", "domain_keyword", "ip_cidr", "source_ip_cidr", "network", "port", "port_range", "ip_version":
			default:
				return errors.New("unsupported custom rule condition")
			}
		}
		if _, err := nativeRule(r.Match, coreTarget(r.Target)); err != nil {
			return err
		}
	}
	return nil
}
