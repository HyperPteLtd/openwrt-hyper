package tlsdiag

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCertificateFailuresExposeOnlyPublicValidityAndCategory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cert := &x509.Certificate{NotBefore: now, NotAfter: now.Add(time.Hour), DNSNames: []string{"private.secret"}}
	for _, tc := range []struct {
		err     error
		clock   time.Time
		want    string
		recover bool
	}{
		{x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}, now.Add(-time.Hour), "not_yet_valid", true},
		{x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}, now.Add(2 * time.Hour), "expired", true},
		{x509.HostnameError{Certificate: cert, Host: "private.secret"}, now, "hostname", false},
		{x509.UnknownAuthorityError{Cert: cert}, now, "untrusted", false},
		{x509.CertificateInvalidError{Cert: cert, Reason: x509.IncompatibleUsage}, now, "verification_failed", false},
	} {
		err := &url.Error{Op: "GET", URL: "https://private.secret/?token=private", Err: &tls.CertificateVerificationError{Err: tc.err}}
		d := Classify(err, tc.clock)
		if d == nil || strings.Contains(d.Wire(), "private") || d.Reason != tc.want {
			t.Fatal(d)
		}
		got := Parse(d.Wire())
		if got == nil || got.Recheck(tc.clock) || got.Recheck(cert.NotBefore) != tc.recover || got.Recheck(cert.NotAfter) != tc.recover {
			t.Fatalf("invalid clock recovery for %s: %#v", tc.want, got)
		}
	}
	for _, raw := range []string{`release_tls {"reason":"hostname","secret":"hidden"}`, `release_tls {"reason":"expired"}`, `release_tls {"reason":"password"}`} {
		if Parse(raw) != nil {
			t.Fatalf("retained unknown helper diagnostic %s", raw)
		}
	}
	if Classify(errors.New("unrelated"), now) != nil {
		t.Fatal("classified unrelated error")
	}
}
