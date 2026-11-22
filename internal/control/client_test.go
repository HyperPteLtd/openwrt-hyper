// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeCore is a minimal stand-in for mihomo's external controller.
func fakeCore(t *testing.T, secret string) (*Client, *map[string]string) {
	t.Helper()
	selected := map[string]string{"main-out": "cfg-a-out"}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.WriteHeader(401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /version", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"meta":true,"version":"v1.0.0"}`))
	}))
	mux.HandleFunc("GET /proxies", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"proxies": map[string]any{
			"main-out":  map[string]any{"name": "main-out", "type": "Selector", "now": selected["main-out"], "all": []string{"cfg-a-out", "cfg-b-out"}},
			"cfg-a-out": map[string]any{"name": "cfg-a-out", "type": "Vless", "history": []any{map[string]any{"time": time.Now(), "delay": 120}}},
		}})
	}))
	mux.HandleFunc("GET /proxies/{name}", auth(func(w http.ResponseWriter, r *http.Request) {
		n := r.PathValue("name")
		json.NewEncoder(w).Encode(map[string]any{"name": n, "type": "Selector", "now": selected[n]})
	}))
	mux.HandleFunc("PUT /proxies/{name}", auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "missing" {
			w.WriteHeader(400)
			w.Write([]byte(`{"message":"Selector update error: proxy not exist"}`))
			return
		}
		if body.Name != "sticky" {
			selected[r.PathValue("name")] = body.Name
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /proxies/{name}/delay", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") == "cfg-dead-out" {
			w.WriteHeader(504)
			w.Write([]byte(`{"message":"Timeout"}`))
			return
		}
		if r.URL.Query().Get("timeout") != "5000" || !strings.HasPrefix(r.URL.Query().Get("url"), "https://") {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"delay":87}`))
	}))
	mux.HandleFunc("GET /connections", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"downloadTotal":10,"uploadTotal":5,"memory":4096,"connections":[{"id":"x","upload":1,"download":2,"chains":["cfg-a-out","main-out"],"rule":"Match","metadata":{"network":"tcp","sourceIP":"192.168.1.2","sourcePort":"5000","host":"example.com","destinationPort":"443"}}]}`))
	}))
	mux.HandleFunc("GET /traffic", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{\"up\":300,\"down\":900}\n{\"up\":1,\"down\":1}\n"))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(strings.TrimPrefix(srv.URL, "http://"), secret), &selected
}

func TestClient(t *testing.T) {
	ctx := context.Background()
	c, selected := fakeCore(t, "s3cret")

	if v, err := c.Version(ctx); err != nil || v != "v1.0.0" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	p, err := c.Proxy(ctx, "main-out")
	if err != nil || p.Now != "cfg-a-out" {
		t.Fatalf("Proxy = %+v, %v", p, err)
	}
	if err = c.Select(ctx, "main-out", "cfg-b-out"); err != nil || (*selected)["main-out"] != "cfg-b-out" {
		t.Fatalf("Select: %v", err)
	}
	var se *StatusError
	if err = c.Select(ctx, "main-out", "missing"); !errors.As(err, &se) || se.Code != 400 || !strings.Contains(err.Error(), "proxy not exist") {
		t.Fatalf("Select missing: %v", err)
	}
	if err = c.Select(ctx, "main-out", "sticky"); err == nil {
		t.Fatal("Select must fail when the core does not apply the change")
	}
	if d, err := c.Delay(ctx, "cfg-a-out", "https://www.gstatic.com/generate_204", 5*time.Second); err != nil || d != 87 {
		t.Fatalf("Delay = %d, %v", d, err)
	}
	if _, err = c.Delay(ctx, "cfg-dead-out", "https://www.gstatic.com/generate_204", 5*time.Second); !errors.Is(err, ErrDelayFailed) {
		t.Fatalf("Delay dead = %v", err)
	}
	cs, err := c.Connections(ctx)
	if err != nil || cs.Memory != 4096 || len(cs.Connections) != 1 || cs.Connections[0].Metadata.SourceIP != "192.168.1.2" {
		t.Fatalf("Connections = %+v, %v", cs, err)
	}
	if tot, err := c.Totals(ctx); err != nil || tot.Memory != 4096 || tot.Connections != nil {
		t.Fatalf("Totals = %+v, %v", tot, err)
	}
	if _, err = c.Proxy(ctx, "bad\nname"); err == nil {
		t.Fatal("names with control characters must be rejected")
	}
}

func TestClientAuthAndUnavailable(t *testing.T) {
	ctx := context.Background()
	c, _ := fakeCore(t, "s3cret")
	c.Secret = "wrong"
	var se *StatusError
	if _, err := c.Version(ctx); !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("wrong secret: %v", err)
	}
	down := New("127.0.0.1:1", "x")
	if _, err := down.Version(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed port: %v", err)
	}
}

// A node switch closes only the connections through the switched group.
func TestCloseThrough(t *testing.T) {
	var closed []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /connections", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"connections":[{"id":"a","chains":["cfg-a-out","main-out"]},{"id":"b","chains":["DIRECT"]},{"id":"gone","chains":["cfg-b-out","main-out"]}]}`))
	})
	mux.HandleFunc("DELETE /connections/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "gone" {
			w.WriteHeader(404)
			return
		}
		closed = append(closed, r.PathValue("id"))
		w.WriteHeader(204)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(strings.TrimPrefix(srv.URL, "http://"), "")
	if err := c.CloseThrough(context.Background(), "main-out"); err != nil || len(closed) != 1 || closed[0] != "a" {
		t.Fatalf("closed %v, %v", closed, err)
	}
}
