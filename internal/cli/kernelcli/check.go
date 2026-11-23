package kernelcli

import (
	"context"
	"errors"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// The core can quote credentials or URLs in its diagnostics. Derive the public
// error only from a recognized position, a generated ID and known schema keys.
type configRejection struct {
	Kind   string `json:"kind"`
	Object string `json:"object"`
	Reason string `json:"reason"`
}

func (r *configRejection) RejectedConfiguration() bool { return true }

func (r *configRejection) Error() string { return r.Object + ": " + r.Reason }

// Report only normalized failure classes. Raw paths, URLs and core stderr may
// contain credentials and must never be copied to the RPC response.
func launchIssue(err error) *configRejection {
	var rejected *configRejection
	if errors.As(err, &rejected) {
		return rejected
	}
	reason := "kernel_preparation_failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "kernel_validation_timeout"
	case errors.Is(err, syscall.ENOSPC):
		reason = "kernel_storage_full"
	case errors.Is(err, os.ErrPermission):
		reason = "kernel_permission_denied"
	case errors.Is(err, os.ErrNotExist):
		reason = "kernel_file_missing"
	default:
		switch err.Error() {
		case "insufficient storage for the config snapshot and reserve", "insufficient space for configuration check":
			reason = "kernel_storage_reserve"
		case "kernel transaction in progress", "selected kernel changed during compilation":
			reason = "kernel_selection_changed"
		case "release file size or SHA-256 mismatch":
			reason = "kernel_integrity_check_failed"
		case "invalid candidate configuration", "cannot read candidate configuration", "cannot read valid candidate configuration":
			reason = "kernel_config_unreadable"
		case "no selected core", "requested core is not installed":
			reason = "kernel_not_installed"
		case "release source unreachable":
			reason = "kernel_restore_unavailable"
		}
	}
	return &configRejection{"kernel", "", reason}
}

var checkPosition = regexp.MustCompile(`(?i)\b(proxy group|proxy|rules?)(?: |\[)([0-9]{1,5})(?:\]|:)`)
var nodeID = regexp.MustCompile(`^(node|group):[0-9a-f]{32}$`)

func rejection(config, diagnostic string) *configRejection {
	r := &configRejection{"configuration", "configuration", "candidate core rejected the configuration"}
	loc := checkPosition.FindStringSubmatch(diagnostic)
	if loc == nil {
		return r
	}
	idx, _ := strconv.Atoi(loc[2])
	section := strings.ToLower(loc[1])
	r.Object = section + "[" + strconv.Itoa(idx) + "]"
	if section == "rule" || section == "rules" {
		return r
	}
	f, e := os.Open(config)
	if e != nil {
		return r
	}
	defer f.Close()
	type entry struct{ Name, Type string }
	var doc struct {
		Proxies []entry `yaml:"proxies"`
		Groups  []entry `yaml:"proxy-groups"`
	}
	if yaml.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&doc) != nil {
		return r
	}
	entries := doc.Proxies
	if section == "proxy group" {
		entries = doc.Groups
	}
	if idx >= len(entries) {
		return r
	}
	ent := entries[idx]
	if nodeID.MatchString(ent.Name) {
		r.Object = ent.Name
		r.Kind, _, _ = strings.Cut(ent.Name, ":")
	}
	if slices.Contains(strings.Fields("http socks5 ss ssr vmess vless trojan anytls hysteria2 tuic naive snell mieru nowhere select url-test fallback load-balance"), ent.Type) {
		r.Reason = "candidate core rejected " + ent.Type + " configuration"
	}
	return r
}
