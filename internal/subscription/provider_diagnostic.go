package subscription

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

// ProviderDiagnostic contains only safe fields for task results and the source's
// last unsuccessful refresh. Error never includes the original transport error.
type ProviderDiagnostic struct {
	tlsDetail     *tlsdiag.Diagnostic
	DownloadGroup *DownloadGroup `json:"download_group,omitempty"`
	Kind          string         `json:"kind"`
	Code          string         `json:"code"`
	File          string         `json:"file,omitempty"`
	Warnings      string         `json:"template_warnings,omitempty"`
}

func (p *ProviderDiagnostic) Unwrap() error {
	if p.tlsDetail == nil {
		return nil
	}
	return p.tlsDetail
}

func (p *ProviderDiagnostic) Error() string { return p.Code }

var providerCodes = set("invalid_provider_url provider_timeout provider_dns_failed provider_tls_untrusted provider_tls_expired provider_tls_not_yet_valid provider_tls_hostname provider_tls_verification_failed provider_unreachable provider_connection_refused private_provider_address provider_redirect_refused provider_download_failed provider_limit provider_size_limit invalid_proxy_provider invalid_rule_provider node_limit rule_limit provider_incomplete provider_cancelled")

func providerFailure(kind, raw string, cause error) error {
	if kind != "rule" && kind != "node" {
		return errors.New("provider_download_failed")
	}
	code := "provider_download_failed"
	if cause != nil {
		value := cause.Error()
		n, err := strconv.Atoi(strings.TrimPrefix(value, "provider_http_"))
		if providerCodes[value] || err == nil && n >= 100 && n <= 599 && value == "provider_http_"+strconv.Itoa(n) {
			code = value
		}
	}
	return &ProviderDiagnostic{tlsDetail: tlsdiag.From(cause), Kind: kind, Code: code, File: publicProviderFile(raw)}
}

// Paths can themselves be credentials, even when a URL has no query string.
// Show a basename only when all of its parts are known rule-list vocabulary.
// For an unfamiliar/private name the UI can still identify the provider kind.
var fileWords = set("geosite geoip category rule rules ruleset domain domains ip cidr ipcidr ipv4 ipv6 cn hk tw jp us uk sg global private public direct reject block proxy default main domestic china mainland sites list ads ad adblock tracker tracking apple google microsoft netflix youtube disney spotify telegram github streaming media ai gfw lan local providers nodes fallback download games gaming chinaip classic classical services exclude non no lite full dns openai hyper social music paypal location tld geolocation sub clash sing box")

func publicProviderFile(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	name := path.Base(u.EscapedPath())
	if len(name) > 64 || strings.Contains(name, "%") {
		return ""
	}
	stem, ext, ok := strings.Cut(name, ".")
	switch strings.ToLower(ext) {
	case "srs", "mrs", "yaml", "yml", "json", "txt", "list":
	default:
		return ""
	}
	if !ok || stem == "" {
		return ""
	}
	for _, word := range strings.Split(strings.ReplaceAll(strings.ToLower(stem), "_", "-"), "-") {
		if !fileWords[word] {
			return ""
		}
	}
	return name
}

func FailureProvider(err error) *ProviderDiagnostic {
	var p *ProviderDiagnostic
	if errors.As(err, &p) {
		return p
	}
	return nil
}

type requestFailure struct {
	code, providerCode string
	detail             *tlsdiag.Diagnostic
}

func (e *requestFailure) Unwrap() error {
	if e.detail == nil {
		return nil
	}
	return e.detail
}

func (e *requestFailure) Error() string { return e.code }

func providerRequestCause(err error) error {
	var failed *requestFailure
	if errors.As(err, &failed) {
		return &requestFailure{code: failed.providerCode, detail: failed.detail}
	}
	switch err.Error() {
	case "invalid_response_size":
		return errors.New("provider_size_limit")
	case "invalid_source_url", "invalid_provider_url":
		return errors.New("invalid_provider_url")
	}
	return errors.New("provider_download_failed")
}

func classifyRequestFailure(err error, general string) error {
	var dns *net.DNSError
	var timed net.Error
	var remote *url.Error
	// Never retain the wrapped URL or certificate subject.
	if errors.As(err, &remote) {
		err = remote.Err
	}
	detail := tlsdiag.Classify(err, time.Now())
	var provider string
	switch {
	case detail != nil:
		general, provider = "subscription_"+detail.Error(), "provider_"+detail.Error()
	case errors.Is(err, context.Canceled):
		provider = "provider_cancelled"
	case errors.As(err, &timed) && timed.Timeout():
		provider = "provider_timeout"
	case errors.As(err, &dns):
		provider = "provider_dns_failed"
	case errors.Is(err, syscall.ECONNREFUSED):
		provider = "provider_connection_refused"
	case errors.Is(err, io.ErrUnexpectedEOF):
		provider = "provider_incomplete"
	default:
		switch err.Error() {
		case "private_provider_address":
			provider = "private_provider_address"
		case "redirect_refused", "authenticated_redirect_refused":
			provider = "provider_redirect_refused"
		default:
			provider = "provider_unreachable"
		}
	}
	return &requestFailure{general, provider, detail}
}
