package subscription

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

func TestProviderDiagnosticsDoNotRetainURLOrCredentials(t *testing.T) {
	const secret = "fixture-secret"
	for _, tc := range []struct{ path, want string }{
		{"/" + secret + "/geosite-cn.srs?token=" + secret, "geosite-cn.srs"},
		{"/" + secret + ".srs?token=" + secret, ""},
		{"/%2F" + secret + ".yaml", ""},
		{"/" + secret + "/providers.yaml", "providers.yaml"},
		{"/.env", ""},
		{"/proxy.yaml.fragment", ""},
		{"/" + strings.Repeat("a", 32) + ".json", ""},
	} {
		raw := "https://" + secret + ":password@service.example" + tc.path
		cause := providerFailure("rule", raw, errors.New("provider_http_404"))
		info := FailureProvider(cause)
		if info == nil || info.File != tc.want || info.Code != "provider_http_404" || info.Kind != "rule" {
			t.Fatal(info)
		}
		b, err := json.Marshal(info)
		if err != nil || strings.Contains(string(b), secret) || strings.Contains(string(b), "service.example") || strings.Contains(string(b), "password") {
			t.Fatal(string(b), err)
		}
	}
	for _, cause := range []string{"Bearer " + secret, "provider_http_600", "provider_http_0404"} {
		err := providerFailure("node", "", errors.New(cause))
		if err.Error() != "provider_download_failed" {
			t.Fatal(err)
		}
	}
}

func TestProviderTransportCategoriesSkipRawDetails(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		cause                  error
		provider, subscription string
	}{
		{"DNS", &net.DNSError{Name: "private.example", Err: "no such host"}, "provider_dns_failed", "subscription_request_failed"},
		{"timeout", context.DeadlineExceeded, "provider_timeout", "subscription_request_failed"},
		{"connection refused", syscall.ECONNREFUSED, "provider_connection_refused", "subscription_request_failed"},
		{"cancel", context.Canceled, "provider_cancelled", "subscription_request_failed"},
		{"certificate", x509.UnknownAuthorityError{}, "provider_tls_untrusted", "subscription_tls_untrusted"},
		{"private", errors.New("private_provider_address"), "private_provider_address", "subscription_request_failed"},
		{"redirect refused", errors.New("authenticated_redirect_refused"), "provider_redirect_refused", "subscription_request_failed"},
		{"truncated", io.ErrUnexpectedEOF, "provider_incomplete", "subscription_request_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := classifyRequestFailure(&url.Error{Op: "GET", URL: "https://example.test/token/private", Err: tc.cause}, "subscription_request_failed")
			if e.Error() != tc.subscription || providerRequestCause(e).Error() != tc.provider {
				t.Fatal(e, providerRequestCause(e))
			}
		})
	}
}

func TestFailedProviderPreservesProfileSharesSafeReasonAndClearsOnRetry(t *testing.T) {
	var sendMissing atomic.Bool
	var base string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			if sendMissing.Load() {
				w.Header().Set("X-Routing-Warnings", "3")
				w.Header().Set("X-Routing-Warning-Codes", "STATIC_REJECT=2,CUSTOM_TEMPLATE=1")
				w.Write(ruleFixture(base, 1))
			} else {
				fmt.Fprint(w, "socks5://example.test:1080#Fixture")
			}
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("provider received credentials")
		}
		http.Error(w, "token=PRIVATETOKEN", http.StatusNotFound)
	}))
	defer server.Close()
	base = server.URL
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "test", Name: "fixture", Kind: "external", URL: base + "/feed?token=PRIVATETOKEN"}); err != nil {
		t.Fatal(err)
	}
	f := Fetcher{Client: server.Client()}
	ctx := t.Context()
	if _, err := f.Refresh(ctx, store, "test", nil, Policy{}); err != nil {
		t.Fatal(err)
	}
	sendMissing.Store(true)
	_, err := f.Refresh(ctx, store, "test", nil, Policy{})
	p := FailureProvider(err)
	if p == nil || p.Code != "provider_http_404" || p.Kind != "rule" || p.Warnings != "CUSTOM_TEMPLATE=1,STATIC_REJECT=2" {
		t.Fatal(err, p)
	}
	status, err := store.Status("test")
	if err != nil || !status.Ready || status.NodeCount != 1 || status.LastFailure != "provider_http_404" || status.LastProvider == nil || *p != *status.LastProvider {
		t.Fatal(status, err)
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), "PRIVATETOKEN") || strings.Contains(string(data), base) {
		t.Fatal(string(data))
	}
	sendMissing.Store(false)
	if _, err := f.Refresh(ctx, store, "test", nil, Policy{}); err != nil {
		t.Fatal(err)
	}
	status, err = store.Status("test")
	if err != nil || status.LastFailure != "" || status.LastProvider != nil {
		t.Fatal(status, err)
	}
}

func TestCertificateFailurePersistsClockRecoveryOnlyForOriginalSource(t *testing.T) {
	store := OpenStore(t.TempDir())
	src := Source{ID: "test", Name: "fixture", Kind: "external", URL: "https://source.test/private-token"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	cert := &x509.Certificate{NotBefore: start, NotAfter: start.Add(time.Hour)}
	f := Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}
	})}}
	ctx := t.Context()
	if _, err := f.Refresh(ctx, store, src.ID, nil, Policy{}); err == nil || err.Error() != "subscription_tls_not_yet_valid" {
		t.Fatal(err)
	}
	status, err := store.Status(src.ID)
	if err != nil || !status.RetryAfterClock || status.Ready || !status.LastTLS.Recheck(start) || status.LastTLS.Recheck(time.Now()) {
		t.Fatal(status, err)
	}
	if _, err := f.Refresh(ctx, store, src.ID, nil, Policy{ClockRetryGeneration: status.Generation}); !errors.Is(err, ErrSuperseded) {
		t.Fatal("retried before clock recovered", err)
	}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	current, err := store.Status(src.ID)
	if err != nil || current.RetryAfterClock {
		t.Fatal(current, err)
	}

	// Recreate the observation of a router whose clock was too early, without
	// changing the host clock. Only one worker may consume the same ticket.
	first, _ := store.Begin(src.ID)
	if err := store.update(src.ID, func(r *record) error {
		r.ClockRetryGeneration = first.Generation
		r.LastTLS = &tlsdiag.Diagnostic{Reason: "not_yet_valid", CheckedAt: start.Add(-5 * time.Hour), NotBefore: start.Add(-2 * time.Hour), NotAfter: start.Add(time.Hour)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.begin(ctx, src.ID, first.Generation, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.begin(ctx, src.ID, first.Generation, true); !errors.Is(err, ErrSuperseded) {
		t.Fatal("duplicate clock retry", err)
	}
	f.ReplacementURL = "https://replacement.test/private"
	if _, err := f.Refresh(ctx, store, src.ID, nil, Policy{}); err == nil {
		t.Fatal("expected failed replacement")
	}
	status, _ = store.Status(src.ID)
	if status.RetryAfterClock {
		t.Fatal("would replay a replacement as an ordinary refresh")
	}

	requestErr := classifyRequestFailure(x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}, "subscription_request_failed")
	provider := providerFailure("rule", "https://rules.test/private", providerRequestCause(requestErr))
	if provider.Error() != "provider_tls_not_yet_valid" || !tlsdiag.From(provider).Recheck(start) {
		t.Fatal(provider)
	}
}

// Run the actual Go TLS handshake against a certificate with a known validity
// window. Trust the fixture certificate explicitly so the verifier reaches its
// date checks, as it would for an HTTPS subscription on a cold router.
func TestTLSHandshakeOnSubscriptionDownloadRecordsCertificateDates(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: start, NotAfter: start.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid certificate was accepted") }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{raw}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	trust := x509.NewCertPool()
	trust.AddCert(cert)
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust}}
	defer tr.CloseIdleConnections()
	store := OpenStore(t.TempDir())
	src := Source{ID: "test", Name: "fixture", Kind: "external", URL: server.URL + "/private"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	_, err = (Fetcher{Client: &http.Client{Transport: tr}}).Refresh(t.Context(), store, src.ID, nil, Policy{})
	d := tlsdiag.From(err)
	if d == nil || d.Reason != "not_yet_valid" || !d.NotBefore.Equal(cert.NotBefore) || !d.NotAfter.Equal(cert.NotAfter) {
		t.Fatal(err, d)
	}
	st, err := store.Status(src.ID)
	if err != nil || !st.RetryAfterClock || st.LastFailure != "subscription_tls_not_yet_valid" || !st.LastTLS.Recheck(start) {
		t.Fatal(err, st)
	}
}
