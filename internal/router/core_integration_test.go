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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The release in this test runs inside an isolated network namespace. It never
// enters the installer's trust store or uses a home network.
func TestUpstreamCoreControl(t *testing.T) {
	bin := os.Getenv("HYPER_DIAGNOSTIC_CORE")
	if bin == "" {
		t.Skip("requires isolated upstream CLI fixture")
	}
	p, e := subscription.Parse("primary", []byte("http://127.0.0.1:3128#one\nhttp://127.0.0.1:3129#two"))
	if e != nil {
		t.Fatal(e)
	}
	i := initial()
	i.Source = "primary"
	i.Mode = "global"
	i.Choices = map[string]map[string]string{p.Source: {Global: p.Nodes[1].ID}}
	cap := kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true}
	client := control.Client{Secret: strings.Repeat("x", 32)}
	b, e := Compile(i, p, cap, Network{Method: "tproxy", Resolver: "192.0.2.53", DNS: true}, client.Secret)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if e = os.WriteFile(cfg, b, 0600); e != nil {
		t.Fatal(e)
	}
	if out, e := exec.Command(bin, "-t", "-d", dir, "-f", cfg).CombinedOutput(); e != nil {
		t.Fatalf("config check: %s", out)
	}
	// Exercise the real control and DNS listeners even on VM kernels missing
	// IP_TRANSPARENT. The production TProxy config was checked above.
	var v map[string]any
	_ = yaml.Unmarshal(b, &v)
	delete(v, "listeners")
	b, _ = yaml.Marshal(v)
	_ = os.WriteFile(cfg, b, 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	proc := exec.CommandContext(ctx, bin, "-d", dir, "-f", cfg)
	var stderr bytes.Buffer
	proc.Stderr = &stderr
	proc.Stdout = &stderr
	if e = proc.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		cancel()
		_ = proc.Wait()
		if t.Failed() {
			t.Log(stderr.String())
		}
	}()
	for {
		if _, e = client.Version(ctx); e == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(e)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The controller can answer before the DNS listener is up: wait for it, not just the API.
	for {
		if e = probeDNS(ctx, DNSPort); e == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(e)
		}
		time.Sleep(25 * time.Millisecond)
	}
	wrong := control.Client{Secret: strings.Repeat("wrong", 8)}
	if _, e := wrong.Version(ctx); e == nil {
		t.Fatal("accepted wrong secret")
	}
	gs, e := client.Groups(ctx)
	if e != nil || len(gs) == 0 || gs[0].Selected != p.Nodes[1].ID {
		t.Fatalf("saved choice did not survive startup: %v %v", gs, e)
	}
	if e = client.Select(ctx, Global, p.Nodes[0].ID); e != nil {
		t.Fatal(e)
	}
	gs, e = client.Groups(ctx)
	if e != nil || len(gs) == 0 {
		t.Fatalf("readback %v %v", gs, e)
	}
	if gs[0].Selected != p.Nodes[0].ID {
		t.Fatalf("wrong readback %#v", gs)
	}
	// Keep an actual TCP tunnel through the serving core open while a
	// separate candidate process checks the complete mixed subscription.
	ping := mixedEcho(t)
	ping()
	unsupported, e := subscription.Parse(p.Source, []byte("nowhere://0102030405060708090a0b0c0d0e0f10@198.18.0.33:443?net=udp&morph=1#NW3"))
	if e != nil || len(unsupported.Nodes) != 1 {
		t.Fatalf("NW3 fixture: %v", e)
	}
	p.Nodes = append(p.Nodes, unsupported.Nodes[0])
	mixed, e := Compile(i, p, cap, Network{Method: "tproxy", Resolver: "192.0.2.53", DNS: true}, client.Secret)
	if e != nil {
		t.Fatal(e)
	}
	cfg = filepath.Join(dir, "candidate.yaml")
	if e = os.WriteFile(cfg, mixed, 0600); e != nil {
		t.Fatal(e)
	}
	if out, e := exec.Command(bin, "-t", "-d", dir, "-f", cfg).CombinedOutput(); e != nil {
		t.Fatalf("fork rejected its native NW3 candidate: %s", out)
	}
	ping()
	gs, e = client.Groups(ctx)
	if e != nil || len(gs) == 0 || gs[0].Selected != p.Nodes[0].ID {
		t.Fatal("candidate check disturbed the confirmed selector", e)
	}
}

func mixedEcho(t *testing.T) func() {
	t.Helper()
	peer, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { peer.Close() })
	go func() {
		if c, e := peer.Accept(); e == nil {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}
	}()
	conn, e := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", MixedPort))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	addr := peer.Addr().String()
	if _, e = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr); e != nil {
		t.Fatal(e)
	}
	reader := bufio.NewReader(conn)
	reply, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if e != nil || reply.StatusCode != 200 {
		t.Fatalf("proxy CONNECT: %v %v", reply, e)
	}
	return func() {
		var got [4]byte
		if _, e := conn.Write([]byte("ping")); e != nil {
			t.Fatal(e)
		}
		if _, e := io.ReadFull(reader, got[:]); e != nil || string(got[:]) != "ping" {
			t.Fatalf("confirmed connection interrupted: %q %v", got, e)
		}
	}
}

func TestMihomoChecksManagedModes(t *testing.T) {
	bin := os.Getenv("HYPER_DIAGNOSTIC_CORE")
	if bin == "" {
		t.Skip("requires pinned Mihomo")
	}
	dir := t.TempDir()
	builtin, err := filepath.Abs("../../files/usr/share/hyper/rules")
	if err != nil {
		t.Fatal(err)
	}
	if e := os.Symlink(builtin, filepath.Join(dir, "builtin")); e != nil {
		t.Fatal(e)
	}
	devdir := filepath.Join(dir, "devices")
	if e := os.Mkdir(devdir, 0700); e != nil {
		t.Fatal(e)
	}
	p := baseProfile(t, "wire")
	i := initial()
	i.Source = p.Source
	i.Policy.DefaultDirect = true
	i.Policy.Devices = []DeviceRule{{"02:00:00:00:00:02", "@proxy"}, {"192.168.41.2", subscription.Direct}}
	name := macProviderName(i.Policy.Devices[0].Address)
	// A configured device can be offline on first boot.
	if e := os.WriteFile(filepath.Join(devdir, name+".txt"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{"tproxy", "tun"} {
		b, e := Compile(i, p, kernel.TargetCapabilities{Control: kernel.Controller, TProxy: true, TUN: true}, Network{DNS: true, Family6: true, FakeIP: method == "tun", Method: method, Resolver: "2001:db8::53"}, strings.Repeat("C", 32))
		if e != nil {
			t.Fatal(e)
		}
		file := filepath.Join(dir, method+".yaml")
		if e = os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		if out, e := exec.Command(bin, "-t", "-d", dir, "-f", file).CombinedOutput(); e != nil {
			t.Fatalf("%s: %s", method, out)
		}
	}
}
