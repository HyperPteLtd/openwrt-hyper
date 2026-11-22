// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// TestMain doubles as a fake mihomo when HYPERD_FAKE_CORE is set: `-v`, `-t` and a run
// mode serving the controller endpoints hyperd uses. It records selections and provider
// refreshes in $HYPERD_FAKE_LOG.
func TestMain(m *testing.M) {
	if os.Getenv("HYPERD_FAKE_CORE") == "1" {
		os.Exit(fakeCore(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeLog(format string, args ...any) {
	f, err := os.OpenFile(os.Getenv("HYPERD_FAKE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintf(f, format+"\n", args...)
		f.Close()
	}
}

func fakeCore(args []string) int {
	if len(args) == 1 && args[0] == "-v" {
		fmt.Println("Mihomo Meta fake linux arm64")
		return 0
	}
	var cfgPath string
	test := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			test = true
		case "-f":
			i++
			cfgPath = args[i]
		}
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	var cfg struct {
		Controller string           `yaml:"external-controller"`
		Secret     string           `yaml:"secret"`
		Proxies    []map[string]any `yaml:"proxies"`
	}
	if err = yaml.Unmarshal(b, &cfg); err != nil || cfg.Controller == "" {
		fmt.Println("bad config", err)
		return 1
	}
	if test {
		if strings.Contains(string(b), "FAIL-CHECK") {
			fmt.Println("rules[0] error: FAIL-CHECK")
			return 1
		}
		return 0
	}
	fakeLog("run %s %d", cfgPath, len(cfg.Proxies))
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+cfg.Secret {
				w.WriteHeader(401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /version", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"fake"}`))
	}))
	selected := ""
	mux.HandleFunc("PUT /proxies/{group}", auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		json.NewDecoder(r.Body).Decode(&body)
		selected = body.Name
		fakeLog("select %s %s", r.PathValue("group"), body.Name)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /proxies/{name}", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"name": r.PathValue("name"), "type": "Selector", "now": selected})
	}))
	mux.HandleFunc("GET /proxies/{name}/delay", auth(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.PathValue("name"), "dead") {
			w.WriteHeader(504)
			w.Write([]byte(`{"message":"timeout"}`))
			return
		}
		w.Write([]byte(`{"delay":42}`))
	}))
	mux.HandleFunc("GET /connections", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"downloadTotal":300,"uploadTotal":100,"memory":1024,"connections":[
			{"id":"1","upload":10,"download":20,"metadata":{"sourceIP":"192.168.1.20"}},
			{"id":"2","upload":5,"download":5,"metadata":{"sourceIP":"192.168.1.20"}},
			{"id":"3","upload":1,"download":1,"metadata":{"sourceIP":""}}]}`))
	}))
	mux.HandleFunc("PUT /providers/rules/{name}", auth(func(w http.ResponseWriter, r *http.Request) {
		fakeLog("refresh %s", r.PathValue("name"))
		w.WriteHeader(204)
	}))
	srv := &http.Server{Addr: cfg.Controller, Handler: mux}
	// core-exit beside the core directory ends the core with its contents as a fatal line.
	go func() {
		for {
			if b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(cfgPath)), "core-exit")); err == nil {
				fmt.Printf("time=%q level=fatal msg=%q\n", time.Now().Format(time.RFC3339Nano), string(b))
				os.Exit(1)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
		<-ch
		srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Println(err)
		return 1
	}
	return 0
}
