package subscription

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var errReset = &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}

// failingPath is server's client whose usual path breaks in transit, as when the core
// cannot reach a node. Its TLS settings still trust the fixture server.
func failingPath(server *httptest.Server, calls *atomic.Int32) *http.Client {
	tr := server.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errReset
	}
	return &http.Client{Transport: tr}
}

// useRoute stands in for the router's other way out: every name resolves to resolved
// and every connection reaches server. It records the addresses dialed.
func useRoute(t *testing.T, server, resolved string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var dialed []string
	previous := fallbackRoute
	t.Cleanup(func() { fallbackRoute = previous })
	fallbackRoute = func() *route {
		return &route{
			lookup: func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(resolved)}}, nil
			},
			dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				mu.Lock()
				dialed = append(dialed, address)
				mu.Unlock()
				return (&net.Dialer{}).DialContext(ctx, network, server)
			},
		}
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), dialed...)
	}
}

func TestTransitFailureRetriesOverOtherWayOut(t *testing.T) {
	var rules atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			w.Write(ruleFixture("https://example.com", 3))
			return
		}
		rules.Add(1)
		fmt.Fprint(w, "payload:\n  - media.example.test\n")
	}))
	defer server.Close()
	dialed := useRoute(t, server.Listener.Addr().String(), "203.0.113.7")
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}); err != nil {
		t.Fatal(err)
	}
	var usual atomic.Int32
	c, err := (Fetcher{Fallback: true, Client: failingPath(server, &usual)}).Refresh(t.Context(), store, "sub", nil, Policy{})
	if err != nil || c.Decision != "ready" || len(c.Profile.Rules) != 3 || rules.Load() != 3 {
		t.Fatal(err, c.Decision, rules.Load())
	}
	// Once the other path worked, the rule sets did not wait for the failing one.
	if got := dialed(); usual.Load() != 1 || len(got) != 4 || strings.Count(strings.Join(got, " "), "203.0.113.7:443") != 4 {
		t.Fatal(usual.Load(), got)
	}

	// Without the option nothing is retried.
	usual.Store(0)
	if _, err := (Fetcher{Client: failingPath(server, &usual)}).Refresh(t.Context(), store, "sub", nil, Policy{}); err == nil || usual.Load() != 1 || len(dialed()) != 4 {
		t.Fatal("retried without the fallback", err)
	}
}

func TestOtherWayOutKeepsProviderAddressCheck(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "payload:\n  - media.example.test\n")
	}))
	defer server.Close()
	var usual atomic.Int32
	f := Fetcher{Fallback: true, embedded: true, Client: failingPath(server, &usual)}
	dialed := useRoute(t, server.Listener.Addr().String(), "10.1.2.3")
	_, err := f.request(t.Context(), http.MethodGet, "https://example.com/media.yaml", nil, "", "Hyper", false, MaxBody)
	if providerRequestCause(err).Error() != "private_provider_address" || len(dialed()) != 0 {
		t.Fatal("document-chosen private address dialed over the other path", err, dialed())
	}
	dialed = useRoute(t, server.Listener.Addr().String(), "203.0.113.7")
	r, err := f.request(t.Context(), http.MethodGet, "https://example.com/media.yaml", nil, "", "Hyper", false, MaxBody)
	if err != nil || r.status != http.StatusOK || strings.Join(dialed(), " ") != "203.0.113.7:443" {
		t.Fatal(err, dialed())
	}
}

func TestAnswersAndRefusalsAreNotRetried(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/feed", http.StatusFound)
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	previous := fallbackRoute
	defer func() { fallbackRoute = previous }()
	fallbackRoute = func() *route { t.Error("retried"); return nil }
	f := Fetcher{Fallback: true, Client: server.Client()}
	if r, err := f.request(t.Context(), http.MethodGet, server.URL+"/feed", nil, "", "", false, MaxBody); err != nil || r.status != http.StatusServiceUnavailable {
		t.Fatal(err, r.status)
	}
	if _, err := f.request(t.Context(), http.MethodGet, server.URL+"/moved", nil, "token", "", true, MaxBody); providerRequestCause(err).Error() != "provider_redirect_refused" {
		t.Fatal(err)
	}
	f.Client = &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) { return nil, errReset })}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.request(ctx, http.MethodGet, server.URL+"/feed", nil, "", "", false, MaxBody); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

// The clock retry and TLS diagnostics need a certificate verdict even when the other path
// failed differently, whichever path saw it.
func TestRetryKeepsCertificateVerdict(t *testing.T) {
	start := time.Now().Add(time.Hour)
	early := setFixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, x509.CertificateInvalidError{Cert: &x509.Certificate{NotBefore: start, NotAfter: start.Add(time.Hour)}, Reason: x509.Expired}
	})
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closed.Close()
	useRoute(t, closed.Addr().String(), "203.0.113.7")
	_, err := (Fetcher{Fallback: true, Client: &http.Client{Transport: early}}).request(t.Context(), http.MethodGet, "https://example.com/feed", nil, "", "", false, MaxBody)
	if err == nil || err.Error() != "subscription_tls_not_yet_valid" {
		t.Fatal(err)
	}

	// The other path reaches a server whose certificate the router does not trust.
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted server accepted") }))
	defer untrusted.Close()
	useRoute(t, untrusted.Listener.Addr().String(), "203.0.113.7")
	reset := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errReset }}
	_, err = (Fetcher{Fallback: true, Client: &http.Client{Transport: reset}}).request(t.Context(), http.MethodGet, "https://example.com/feed", nil, "", "", false, MaxBody)
	if err == nil || err.Error() != "subscription_tls_untrusted" {
		t.Fatal(err)
	}
}

// fakeMixedPort answers one CONNECT like the core and then serves the tunneled request itself.
func fakeMixedPort(t *testing.T, status string) <-chan string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	// This process owns the listener, standing in for the core's account.
	previous, owner := mixedProxy, coreOwner
	mixedProxy, coreOwner = ln.Addr().String(), u.Uid
	t.Cleanup(func() { mixedProxy, coreOwner = previous, owner; ln.Close() })
	targets := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		targets <- req.Method + " " + req.Host
		fmt.Fprintf(c, "HTTP/1.1 %s\r\n\r\n", status)
		if _, err := http.ReadRequest(br); err == nil {
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 7\r\nConnection: close\r\n\r\ntunnel!")
		}
	}()
	return targets
}

func TestMixedPortTunnelsByNameOrCheckedAddress(t *testing.T) {
	r := &route{lookup: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.5")}}, nil
	}, dial: dialMixed, byName: true}
	for _, embedded := range []bool{false, true} {
		targets := fakeMixedPort(t, "200 Connection established")
		res, err := r.client(http.Client{Timeout: 5 * time.Second}, embedded).Get("http://sub.test/feed")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		// The core resolves a configured host itself; a document's host was checked here.
		want := map[bool]string{false: "CONNECT sub.test:80", true: "CONNECT 203.0.113.5:80"}[embedded]
		if got := <-targets; got != want || string(body) != "tunnel!" {
			t.Fatal(got, string(body))
		}
	}
	fakeMixedPort(t, "502 Bad Gateway")
	if _, err := dialMixed(t.Context(), "tcp", "sub.test:80"); err == nil {
		t.Fatal("refused tunnel accepted")
	}
	// Another account took the port, as after a core restart: nothing is sent to it.
	if other, err := user.Lookup("nobody"); err == nil && other.Uid != strconv.Itoa(os.Getuid()) {
		targets := fakeMixedPort(t, "200 Connection established")
		coreOwner = other.Uid
		if _, err := dialMixed(t.Context(), "tcp", "sub.test:443"); err == nil || err.Error() != "proxy_not_core" {
			t.Fatal(err)
		}
		select {
		case got := <-targets:
			t.Fatal("sent to a foreign listener:", got)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// The local proxy is not trusted with plain http, even when it worked for this refresh.
func TestMixedPortCarriesOnlyTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	dialed := useRoute(t, server.Listener.Addr().String(), "203.0.113.7")
	inner := fallbackRoute
	fallbackRoute = func() *route { r := inner(); r.tlsOnly = true; return r }
	var usual atomic.Int32
	f := Fetcher{Fallback: true, Client: failingPath(server, &usual)}.withEgress()
	get := func(raw string) error {
		_, err := f.request(t.Context(), http.MethodGet, raw, nil, "", "", false, MaxBody)
		return err
	}
	if err := get("http://example.com/feed"); err == nil || len(dialed()) != 0 {
		t.Fatal("plain http retried through the local proxy", err)
	}
	if err := get("https://example.com/feed"); err != nil || len(dialed()) != 1 {
		t.Fatal(err, dialed())
	}
	usual.Store(0)
	if err := get("http://example.com/feed"); err == nil || usual.Load() != 1 || len(dialed()) != 1 {
		t.Fatal("plain http sent through the proxy that went first", err, dialed())
	}
}

func TestRouterFallbackFollowsTakeover(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	previous := []string{coreOwner, takeoverState, procTCP}
	previousGID := coreGID
	defer func() {
		coreOwner, takeoverState, procTCP = previous[0], previous[1], previous[2]
		coreGID = previousGID
	}()
	coreOwner, takeoverState, procTCP = u.Uid, dir, filepath.Join(dir, "tcp")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("backend", "fw4\n")
	write("owned", "")
	coreGID = func() (int, error) { return 0, errors.New("invalid core group") }
	if routerFallback() != nil {
		t.Fatal("captured router traffic retried direct without the core's group")
	}
	coreGID = func() (int, error) { return os.Getgid(), nil }
	if r := routerFallback(); r == nil || r.byName {
		t.Fatal("captured router traffic must retry direct")
	}
	write("backend", "fw3\n")
	if routerFallback() != nil {
		t.Fatal("no core listening, yet a proxy was chosen")
	}
	local := fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32([]byte{127, 0, 0, 1}), 19763)
	line := "   0: " + local + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000 %s        0 4242 1 0000000000000000 100 0 0 10 0\n"
	write("tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+fmt.Sprintf(line, "4242"))
	if routerFallback() != nil {
		t.Fatal("another account's listener taken for the core")
	}
	write("tcp", fmt.Sprintf(line, u.Uid))
	if r := routerFallback(); r == nil || !r.byName || !r.tlsOnly {
		t.Fatal("uncaptured router traffic must retry through the core, over TLS only")
	}
	os.Remove(filepath.Join(dir, "owned"))
	write("backend", "fw4\n")
	if r := routerFallback(); r == nil || !r.byName {
		t.Fatal("a released takeover no longer captures router traffic")
	}
}

func TestDialAsGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("socket groups are set on Linux")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := dialAsGroup(t.Context(), os.Getgid(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := dialAsGroup(t.Context(), os.Getgid(), "tcp", "localhost:80"); err == nil {
		t.Fatal("dialed a host name, which may race on other threads")
	}
	if os.Getuid() != 0 && os.Getgid() != 0 && os.Getegid() != 0 {
		if _, err := dialAsGroup(t.Context(), 0, "tcp", ln.Addr().String()); err == nil || err.Error() != "socket_group_unavailable" {
			t.Fatal("unprivileged group change went unnoticed", err)
		}
	}
}

// The core runs as root: a listener counts as the core's once root holds it with a file, and an
// account other than root cannot list as root.
func TestRootCoreHoldsTheMixedPort(t *testing.T) {
	dir := t.TempDir()
	previous := []string{coreOwner, procTCP}
	defer func() { coreOwner, procTCP = previous[0], previous[1] }()
	coreOwner, procTCP = "0", filepath.Join(dir, "tcp")
	local := fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32([]byte{127, 0, 0, 1}), 19763)
	head := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	listen := func(uid, inode string) {
		line := "   0: " + local + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000 " + uid + "        0 " + inode + " 1 0000000000000000 100 0 0 10 0\n"
		if err := os.WriteFile(procTCP, []byte(head+line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		uid, inode string
		core       bool
	}{{"0", "4242", true}, {"0", "0", false}, {"65534", "4242", false}, {"1000", "4242", false}} {
		listen(c.uid, c.inode)
		if coreListening() != c.core {
			t.Errorf("uid %s inode %s: core=%v", c.uid, c.inode, !c.core)
		}
	}
	os.WriteFile(procTCP, []byte(head), 0o600)
	if coreListening() {
		t.Fatal("no listener taken for the core")
	}
}
