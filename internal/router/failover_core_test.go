package router

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"go.yaml.in/yaml/v3"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

// Only the child core trusts this ephemeral certificate. The HTTP proxy
// terminates its test traffic locally and has no forwarding implementation.
func latencyEndpoint(t *testing.T) (port int, root string) {
	t.Helper()
	const host = "www.gstatic.com"
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	cert := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Hyper isolated core test"}, DNSNames: []string{host}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, &cert, &cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != host+":443" {
			http.Error(w, "invalid test target", 400)
			return
		}
		conn, buf, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
		fmt.Fprint(buf, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if buf.Flush() != nil {
			return
		}
		stream := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
		req, e := http.ReadRequest(bufio.NewReader(stream))
		if e != nil || req.Method != "HEAD" || req.URL.Path != "/generate_204" {
			return
		}
		time.Sleep(8 * time.Millisecond)
		fmt.Fprint(stream, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	}))
	t.Cleanup(peer.Close)
	_, raw, e := net.SplitHostPort(peer.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	port, e = strconv.Atoi(raw)
	if e != nil {
		t.Fatal(e)
	}
	root = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return
}

func TestFailoverAgainstRelease(t *testing.T) {
	bin := os.Getenv("HYPER_TEST_CORE")
	if bin == "" {
		t.Skip("requires the pinned Mihomo binary in an isolated network")
	}
	bin, e := filepath.Abs(bin)
	if e != nil {
		t.Fatal(e)
	}
	port, ca := latencyEndpoint(t)
	dead, live := testNodeID("d"), testNodeID("c")
	c := control.Client{Secret: "isolated-router-control"}
	cfg := obj{"mode": "rule", "log-level": "warning", "external-controller": "127.0.0.1:19761", "secret": c.Secret, "tls": obj{"custom-certifactes": []string{ca}},
		"proxies":      []obj{{"type": "http", "name": dead, "server": "127.0.0.1", "port": 1}, {"type": "http", "name": live, "server": "127.0.0.1", "port": port}},
		"proxy-groups": []obj{{"name": Global, "type": "select", "proxies": []string{dead, live}}}, "rules": []string{"MATCH," + Global}}
	b, e := yaml.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "core.yaml")
	if e = os.WriteFile(file, b, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, end := context.WithTimeout(context.Background(), 50*time.Second)
	defer end()
	p := exec.CommandContext(ctx, bin, "-d", filepath.Dir(file), "-f", file)
	p.Stderr = os.Stderr
	if e = p.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { end(); _ = p.Wait() }()
	var group control.Group
	for i := 0; i < 30; i++ {
		groups, e := failoverGroups(ctx, c)
		if e == nil && len(groups) > 0 {
			group = groups[0]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !eligibleGroup(group, "global") || group.Selected != dead {
		t.Fatalf("unexpected core selector %+v", group)
	}
	// This is the same core URL measurement used by the LuCI test_nodes RPC.
	// The test endpoint requires a TLS HEAD request and waits before its 204.
	start := time.Now().Unix()
	sample, err := c.Measure(ctx, live)
	if err != nil || sample.Measured < start || sample.Delay == 0 {
		t.Fatalf("missing URL test measurement from the release: %+v, %v", sample, err)
	}
	t.Logf("release URL test measured %d ms through the isolated HTTPS 204 endpoint", sample.Delay)
	state, target := probeFailover(ctx, c, group, nodeFailure{})
	if state.misses != 1 || target != "" {
		t.Fatal("single failure triggered change", state, target)
	}
	_, target = probeFailover(ctx, c, group, state)
	if target != live {
		t.Fatal("failed to select reachable peer", target)
	}
	if e = c.Select(ctx, Global, target); e != nil {
		t.Fatal(e)
	}
	snapshot, e := c.Groups(ctx)
	if e != nil || len(snapshot) != 1 || snapshot[0].Selected != live {
		t.Fatal("core did not report switched node", snapshot, e)
	}
}
