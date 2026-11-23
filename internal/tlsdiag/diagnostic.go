// Package tlsdiag exposes only the type of a failed certificate check and its
// validity dates. Raw verifier errors may contain private hosts and URLs.
package tlsdiag

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Diagnostic struct {
	Reason    string    `json:"reason"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	NotBefore time.Time `json:"not_before,omitzero"`
	NotAfter  time.Time `json:"not_after,omitzero"`
}

func (d *Diagnostic) Error() string { return "tls_" + d.Reason }

// Recheck returns true only after the router clock has moved from outside to
// inside the actual certificate validity interval. A truly expired certificate
// does not trigger a retry on every NTP event.
func (d *Diagnostic) Recheck(now time.Time) bool {
	return d != nil && (d.Reason == "not_yet_valid" || d.Reason == "expired") &&
		!now.Before(d.NotBefore) && !now.After(d.NotAfter)
}

func From(err error) *Diagnostic {
	var d *Diagnostic
	if errors.As(err, &d) {
		return d
	}
	return nil
}

func Classify(err error, now time.Time) *Diagnostic {
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var verify *tls.CertificateVerificationError
	switch {
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired && invalid.Cert != nil:
		cert := invalid.Cert
		reason := "expired"
		if now.Before(cert.NotBefore) {
			reason = "not_yet_valid"
		} else if !now.After(cert.NotAfter) {
			return &Diagnostic{Reason: "verification_failed"}
		}
		return &Diagnostic{Reason: reason, CheckedAt: now.UTC(), NotBefore: cert.NotBefore.UTC(), NotAfter: cert.NotAfter.UTC()}
	case errors.As(err, &hostname):
		return &Diagnostic{Reason: "hostname"}
	case errors.As(err, &authority):
		return &Diagnostic{Reason: "untrusted"}
	case errors.As(err, &verify), errors.As(err, &invalid):
		return &Diagnostic{Reason: "verification_failed"}
	}
	return nil
}

// Wire and Parse exchange a sanitized diagnostic across the kernel helper's
// stderr boundary. Only our canonical encoding is accepted into the journal.
func (d *Diagnostic) Wire() string {
	b, _ := json.Marshal(d)
	return "release_tls " + string(b)
}
func Parse(message string) *Diagnostic {
	raw, ok := strings.CutPrefix(message, "release_tls ")
	if !ok || len(raw) > 250 {
		return nil
	}
	var d Diagnostic
	if json.Unmarshal([]byte(raw), &d) != nil || d.Wire() != message {
		return nil
	}
	switch d.Reason {
	case "expired", "not_yet_valid":
		if d.NotAfter.Before(d.NotBefore) || d.CheckedAt.IsZero() ||
			(d.Reason == "expired" && !d.CheckedAt.After(d.NotAfter)) ||
			(d.Reason == "not_yet_valid" && !d.CheckedAt.Before(d.NotBefore)) {
			return nil
		}
	case "hostname", "untrusted", "verification_failed":
		if !d.CheckedAt.IsZero() || !d.NotAfter.IsZero() || !d.NotBefore.IsZero() {
			return nil
		}
	default:
		return nil
	}
	return &d
}
