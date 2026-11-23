package subscription

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

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

func TestMixedPortTunnelsByName(t *testing.T) {
	r := &route{lookup: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.5")}}, nil
	}, dial: dialMixed, byName: true}
	targets := fakeMixedPort(t, "200 Connection established")
	res, err := r.client(http.Client{Timeout: 5 * time.Second}).Get("http://sub.test/feed")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	// The core resolves the host itself.
	if got := <-targets; got != "CONNECT sub.test:80" || string(body) != "tunnel!" {
		t.Fatal(got, string(body))
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
