package router

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Mihomo combines alternatives inside a field with OR and separate fields with AND.
func logical(kind string, terms []string) string {
	if len(terms) == 1 && kind != "NOT" {
		return terms[0]
	}
	return kind + ",((" + strings.Join(terms, "),(") + "))"
}

func matchValues(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []string:
		return x, nil
	case int:
		return []string{strconv.Itoa(x)}, nil
	case float64:
		if x == float64(int(x)) {
			return []string{strconv.Itoa(int(x))}, nil
		}
	case []any:
		var out []string
		for _, a := range x {
			vs, err := matchValues(a)
			if err != nil || len(vs) != 1 {
				return nil, errors.New("invalid rule value")
			}
			out = append(out, vs[0])
		}
		return out, nil
	}
	return nil, errors.New("invalid rule value")
}
func nativeRule(match obj, target string) (string, error) {
	if prefix, ok := match["clash"].(string); ok {
		if prefix == "" || strings.ContainsAny(prefix, "\n\r\x00") {
			return "", errors.New("invalid native rule")
		}
		line := prefix + "," + target
		if match["no_resolve"] == true {
			line += ",no-resolve"
		}
		if match["src"] == true {
			line += ",src"
		}
		return line, nil
	}
	fields := map[string]string{"domain": "DOMAIN", "domain_suffix": "DOMAIN-SUFFIX", "domain_keyword": "DOMAIN-KEYWORD", "domain_regex": "DOMAIN-REGEX", "ip_cidr": "IP-CIDR", "source_ip_cidr": "SRC-IP-CIDR", "network": "NETWORK", "port": "DST-PORT", "port_range": "DST-PORT", "source_port": "SRC-PORT", "source_port_range": "SRC-PORT", "rule_set": "RULE-SET", "process_name": "PROCESS-NAME"}
	var conditions []string
	for _, key := range slices.Sorted(maps.Keys(match)) {
		if key == "invert" {
			continue
		}
		kind := fields[key]
		if key == "ip_version" {
			kind = "IP-CIDR"
		}
		if kind == "" {
			return "", fmt.Errorf("condition %s cannot be expressed in Mihomo", key)
		}
		values, err := matchValues(match[key])
		if err != nil || len(values) == 0 {
			return "", errors.New("empty or invalid rule condition")
		}
		var options []string
		for _, v := range values {
			if v == "" || strings.ContainsAny(v, ",\r\n\x00") {
				return "", errors.New("invalid rule operand")
			}
			if key == "ip_version" {
				switch v {
				case "4":
					v = "0.0.0.0/0"
				case "6":
					v = "::/0"
				default:
					return "", errors.New("invalid IP version")
				}
			}
			if strings.HasSuffix(key, "port_range") {
				v = strings.ReplaceAll(v, ":", "-")
			}
			options = append(options, kind+","+v)
		}
		conditions = append(conditions, logical("OR", options))
	}
	if len(conditions) == 0 {
		return "", errors.New("empty rule condition")
	}
	prefix := logical("AND", conditions)
	if match["invert"] == true {
		prefix = logical("NOT", []string{prefix})
	}
	return prefix + "," + target, nil
}
