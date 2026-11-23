package router

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"go.yaml.in/yaml/v3"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWireSharedOutbound(t *testing.T) {
	bin := os.Getenv("HYPER_DIAGNOSTIC_CORE")
	if bin == "" {
		t.Skip("requires isolated upstream CLI fixture")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				_, _ = io.Copy(c, r)
			}()
		}
	}()
	p, e := subscription.Parse("primary", []byte("http://"+listener.Addr().String()+"#shared"))
	if e != nil {
		t.Fatal(e)
	}
	node := p.Nodes[0].ID
	first, second := "group:media", "group:research"
	p.Groups = []subscription.Group{{ID: first, Kind: "selector", Members: []string{node}}, {ID: second, Kind: "selector", Members: []string{node}}}
	p.Rules = []subscription.Rule{{Match: obj{"ip_cidr": "192.0.2.0/25"}, Target: first}, {Match: obj{"ip_cidr": "192.0.2.128/25"}, Target: second}}
	p.Local = false
	p.Final = subscription.Direct
	i := initial()
	i.Source = p.Source
	caps := kernel.TargetCapabilities{Control: "mihomo-rest-v1", TProxy: true}
	api := control.Client{Secret: strings.Repeat("f", 32)}
	data, e := Compile(i, p, caps, Network{Method: "tproxy", Resolver: "192.0.2.53"}, api.Secret)
	if e != nil {
		t.Fatal(e)
	}
	var cfg map[string]any
	_ = yaml.Unmarshal(data, &cfg)
	delete(cfg, "listeners")
	mixed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := mixed.Addr().(*net.TCPAddr)
	cfg["mixed-port"] = addr.Port
	mixed.Close()
	data, _ = yaml.Marshal(cfg)
	name := filepath.Join(t.TempDir(), "wire.yaml")
	if e = os.WriteFile(name, data, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, end := context.WithTimeout(context.Background(), 18*time.Second)
	defer end()
	cmd := exec.CommandContext(ctx, bin, "-d", filepath.Dir(name), "-f", name)
	var log bytes.Buffer
	cmd.Stderr = &log
	cmd.Stdout = &log
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		end()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(log.String())
		}
	}()
	for {
		if _, e := api.Version(ctx); e == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("core startup timed out")
		}
		time.Sleep(30 * time.Millisecond)
	}
	// The controller can answer before the mixed listener is up: wait until it accepts.
	for {
		conn, e := net.DialTimeout("tcp4", addr.String(), time.Second)
		if e == nil {
			conn.Close()
			break
		}
		if ctx.Err() != nil {
			t.Fatal(e)
		}
		time.Sleep(30 * time.Millisecond)
	}
	records := NewRecords()
	records.Viewed()
	go func() { _ = api.Connections(ctx, records.pollWait, records.Observe) }()
	for _, oct := range []byte{20, 200} {
		conn, e := net.DialTimeout("tcp4", addr.String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		if _, e = conn.Write([]byte{5, 1, 0}); e != nil {
			t.Fatal(e)
		}
		buf := make([]byte, 2)
		if _, e = io.ReadFull(conn, buf); e != nil || buf[1] != 0 {
			t.Fatal("SOCKS negotiation")
		}
		if _, e = conn.Write([]byte{5, 1, 0, 1, 192, 0, 2, oct, 0x23, 0x28}); e != nil {
			t.Fatal(e)
		}
		reply := make([]byte, 10)
		if _, e = io.ReadFull(conn, reply); e != nil || reply[1] != 0 {
			t.Fatalf("SOCKS connect %v %v", reply, e)
		}
		msg := fmt.Sprintf("verified-echo-%d", oct)
		if _, e = io.WriteString(conn, msg); e != nil {
			t.Fatal(e)
		}
		back := make([]byte, len(msg))
		if _, e = io.ReadFull(conn, back); e != nil || string(back) != msg {
			t.Fatalf("wire witness %q %v", back, e)
		}
	}
	for {
		observed := map[string]bool{}
		for _, r := range records.Routes(8) {
			if r.Outbound == node {
				observed[r.Group] = true
			}
		}
		if observed[first] && observed[second] {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("missing actual chain evidence: %+v", records.Routes(8))
		case <-time.After(60 * time.Millisecond):
		}
	}
	records.Log("service", "warn", "previous_startup_error")
	records.Clear("connections")
	if len(records.Routes(8)) != 0 || len(records.Logs(0, 8, "service", "warn", "startup")) != 1 {
		t.Fatal("page clear crossed records")
	}
}
