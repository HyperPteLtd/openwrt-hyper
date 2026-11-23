package subscription

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// CapabilityKey describes the specific transport variant requiring a core probe.
func (n Node) CapabilityKey() string {
	switch n.Protocol {
	case "naive", "nowhere":
		return n.Protocol
	case "snell":
		if version, ok := integer(n.Outbound["version"]); ok && version == 6 {
			return "snell-v6"
		}
	case "anytls":
		if n.Outbound["reality-opts"] != nil {
			return "anytls-reality"
		}
	}
	return ""
}

func CapabilityAvailability(key string, naive *bool, support map[string]bool) string {
	if key == "" {
		return ""
	}
	if enabled, found := support[key]; found {
		if !enabled {
			return "unsupported"
		}
		return ""
	}
	if key == "naive" {
		if naive == nil {
			return "unverified"
		}
		if !*naive {
			return "unsupported"
		}
		return ""
	}
	if naive == nil && support == nil {
		return "unverified"
	}
	return "" // Older receipts can still preflight the actual configuration.
}

// CoreFeatures is read once per operation, even if hundreds of relevant
// proxies are present. A missing core leaves offline selections available.
type CoreFeatures struct {
	Control   string          `json:"control_api"`
	Naive     *bool           `json:"naive"`
	Protocols map[string]bool `json:"protocol_support"`
}

func CurrentCoreFeatures(ctx context.Context) CoreFeatures {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, e := exec.CommandContext(probe, "/usr/sbin/hyper-kernel", "target-capabilities").Output()
	var f CoreFeatures
	if e != nil || json.Unmarshal(raw, &f) != nil || f.Control != "mihomo-rest-v1" {
		return CoreFeatures{}
	}
	return f
}
func (f CoreFeatures) Availability(key string) string {
	return CapabilityAvailability(key, f.Naive, f.Protocols)
}
