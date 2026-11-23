package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// coreRules reads the rules the core is running, to see that a payload reload took effect.
func coreRules(ctx context.Context, c Client) ([]struct{ Proxy string }, error) {
	var r struct{ Rules []struct{ Proxy string } }
	e := c.call(ctx, "GET", "/rules", nil, &r)
	return r.Rules, e
}

// Runs the actual fork with temporary state. The test transport redirects only
// the controller socket, so it can coexist with a user's running proxy.
func TestMihomoController(t *testing.T) {
	core := os.Getenv("HYPER_MIHOMO_TEST_CORE")
	if core == "" {
		t.Skip("set HYPER_MIHOMO_TEST_CORE to run the controller contract against the fork")
	}
	listen, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listen.Addr().String()
	listen.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf("external-controller: %s\nsecret: test-local-controller\nmode: rule\nlog-level: warning\nprofile: {store-selected: false}\nproxy-groups:\n  - {name: 'Test/A', type: select, proxies: [REJECT, DIRECT]}\nrules: ['MATCH,Test/A']\n", addr)
	if err = os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, end := context.WithTimeout(context.Background(), 12*time.Second)
	defer end()
	proc := exec.CommandContext(ctx, core, "-d", dir, "-f", path)
	if err = proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { end(); _ = proc.Wait() }()
	old := transport
	tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != Address {
			return nil, errors.New("non-loopback controller")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	transport = &http.Client{Transport: tr}
	defer func() { transport = old; tr.CloseIdleConnections() }()
	c := Client{"test-local-controller"}
	for {
		if _, err = c.Version(ctx); err == nil {
			break
		}
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("controller failed to start", err)
		}
	}
	if _, err = (Client{"invalid-credential"}).Version(ctx); !errors.Is(err, statusError(401)) {
		t.Fatal("missing auth enforcement", err)
	}
	// The controller can answer before the configuration's groups are loaded.
	var groups []Group
	for {
		groups, err = c.Groups(ctx)
		if err != nil || len(groups) != 0 || ctx.Err() != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil || len(groups) != 1 || groups[0].Tag != "Test/A" || groups[0].Selected != "@reject" {
		t.Fatal(groups, err)
	}
	if err = c.Select(ctx, "Test/A", "@direct"); err != nil {
		t.Fatal(err)
	}
	rules, err := coreRules(ctx, c)
	if err != nil || len(rules) != 1 || rules[0].Proxy != "Test/A" {
		t.Fatal(rules, err)
	}
	// A payload reload swaps rules in place without a file the core can read.
	next := strings.Replace(config, "rules: ['MATCH,Test/A']", "rules: ['DOMAIN,example.com,DIRECT', 'MATCH,Test/A']", 1)
	if err = c.ReloadPayload(ctx, []byte(next)); err != nil {
		t.Fatal(err)
	}
	if rules, err = coreRules(ctx, c); err != nil || len(rules) != 2 {
		t.Fatal(rules, err)
	}
	if err = c.ReloadPayload(ctx, []byte("proxy-groups: [")); err == nil {
		t.Fatal("invalid payload accepted")
	}
	if err = c.CloseConnection(ctx, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatal(err)
	}
	if err = c.CloseConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.CloseThrough(ctx, "Test/A"); err != nil {
		t.Fatal(err)
	}
	trf, err := c.Traffic(ctx)
	if err != nil || !trf.Available {
		t.Fatal(trf, err)
	}
}
