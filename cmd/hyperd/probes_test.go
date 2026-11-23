package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

func TestTCPProbeOnlyDialsAnImportedNodeOnce(t *testing.T) {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.SetDeadline(time.Now().Add(3 * time.Second))
	id := "node:00000000000000000000000000000001"
	if _, err := probeIDs(json.RawMessage(`{"nodes":["` + id + `"],"port":1234}`)); err == nil {
		t.Fatal("accepted untrusted endpoint")
	}
	ids, err := probeIDs(json.RawMessage(`{"nodes":["` + id + `"]}`))
	if err != nil {
		t.Fatal(err)
	}
	n := subscription.Node{ID: id, Protocol: "snell", Server: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port}
	profile := subscription.Profile{Nodes: []subscription.Node{n}}
	if _, err := probeTargets(profile, []string{"node:00000000000000000000000000000002"}); err == nil {
		t.Fatal("accepted unknown node")
	}
	nodes, err := probeTargets(profile, ids)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r := measureTCP(ctx, nodes[id]); r.Error != "" || r.Latency == nil || r.Latency.Measured == 0 {
		t.Fatalf("no measurement: %+v", r)
	}
	c, err := l.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := c.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("probe sent application traffic: %d %v", n, err)
	}
	n.Protocol = "tuic"
	if r := measureTCP(ctx, n); r.Error != "not_applicable" || r.Latency != nil {
		t.Fatal("UDP-only node measured")
	}
	l.SetDeadline(time.Now())
	if extra, err := l.Accept(); err == nil {
		extra.Close()
		t.Fatal("unexpected extra dial")
	}
}

func TestProbeMethodsStaySeparateAndLateWorkersCannotPublish(t *testing.T) {
	set := new(probeSet)
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i, method := range []string{"url", "tcp"} {
		ctx, stop, seq := set.begin(parent, "A", method, []string{"node"})
		err := set.run(ctx, seq, method, []string{"node"}, func(context.Context, string) probeResult {
			return probeResult{Latency: &control.Latency{Delay: uint64(i + 1)}}
		})
		stop()
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, method := range []string{"", "tcp"} {
		results, err := set.snapshot("A", method)
		if err != nil || results["node"].Latency == nil || results["node"].Latency.Delay != uint64(i+1) {
			t.Fatalf("wrong results for %q: %v", method, results)
		}
	}
	if _, err := set.snapshot("A", "icmp"); err == nil {
		t.Fatal("accepted invalid method")
	}
	ids := []string{"1", "2", "3", "4", "5", "6", "7", "8"}
	old, stopOld, seq := set.begin(parent, "A", "url", ids)
	defer stopOld()
	started := make(chan bool, 8)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- set.run(old, seq, "url", ids, func(ctx context.Context, id string) probeResult {
			started <- true
			select {
			case <-release:
			case <-parent.Done():
			}
			return probeResult{Node: id}
		})
	}()
	for range 4 {
		select {
		case <-started:
		case <-parent.Done():
			t.Fatal("workers did not start")
		}
	}
	current, stop, seq := set.begin(parent, "B", "tcp", []string{"new"})
	defer stop()
	newDone := make(chan error, 1)
	go func() {
		newDone <- set.run(current, seq, "tcp", []string{"new"}, func(ctx context.Context, id string) probeResult {
			return probeResult{Node: id}
		})
	}()
	select {
	case <-newDone:
		t.Fatal("concurrent batches exceeded 4 connections")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if !errors.Is(<-done, context.Canceled) || <-newDone != nil {
		t.Fatal("incorrect replacement outcome")
	}
	set.stop()
	results, _ := set.snapshot("B", "tcp")
	if len(results) != 1 || results["new"].Node != "new" {
		t.Fatal(results)
	}
	if results, _ := set.snapshot("A", "url"); len(results) != 0 {
		t.Fatal("old source leaked")
	}
	set.reset()
	if results, _ := set.snapshot("B", "tcp"); len(results) != 0 {
		t.Fatal("stale results survived change")
	}
}
