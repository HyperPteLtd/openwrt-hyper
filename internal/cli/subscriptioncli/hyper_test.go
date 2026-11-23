// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package subscriptioncli

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/crypt"
	sub "github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

func sealFor(t *testing.T, clientPub, plain string) map[string]string {
	pub, err := crypt.DecodeB64(clientPub)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := ecdh.X25519().GenerateKey(rand.Reader)
	peer, _ := ecdh.X25519().NewPublicKey(pub)
	shared, _ := server.ECDH(peer)
	salt, nonce := make([]byte, 32), make([]byte, 12)
	rand.Read(salt)
	rand.Read(nonce)
	key, _ := hkdf.Key(sha256.New, shared, salt, "subscribe-v1", 32)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	enc := base64.StdEncoding.EncodeToString
	return map[string]string{"spk": enc(server.PublicKey().Bytes()), "salt": enc(salt), "nonce": enc(nonce), "data": enc(aead.Seal(nil, nonce, []byte(plain), nil))}
}

// The whole account path: sign in, read the nodes and rules, turn a list off, sign out.
func TestHyperAccountFlow(t *testing.T) {
	var loggedOut bool
	var loginBody map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		signed := r.Header.Get("Authorization") == "Bearer jwt"
		switch r.URL.Path {
		case "/v1/common/site/config": // the API address is checked before a request it cannot repeat
			fmt.Fprint(w, `{"code":200,"msg":"","data":{}}`)
		case "/v1/common/send_code":
			fmt.Fprint(w, `{"code":200,"msg":"sent","data":null}`)
		case "/v1/auth/login":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &loginBody)
			if loginBody["code"] != "123456" {
				fmt.Fprint(w, `{"code":400,"msg":"验证码错误","data":null}`)
				return
			}
			fmt.Fprint(w, `{"code":200,"msg":"","data":{"token":"jwt"}}`)
		case "/v1/public/user/logout":
			loggedOut = signed
			fmt.Fprint(w, `{"code":200,"msg":"","data":null}`)
		case "/v1/public/user/info":
			fmt.Fprintf(w, `{"code":200,"msg":"","data":{"signed":%v}}`, signed)
		case "/v1/public/user/subscribe":
			if !signed {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"code":200,"msg":"","data":{"list":[{"id":1,"token":"tok","status":1}]}}`)
		case "/api/subscribe/location":
			fmt.Fprint(w, `{"locations":{"JP":{"sort":9}}}`)
		case "/api/subscribe":
			json.NewEncoder(w).Encode(sealFor(t, q.Get("client_public_key"), "STATUS=Traffic: 1 GB / 10 GB | Expires: 2027-01-01\nanytls://pw@jp.example.com:443?sni=jp.example.com#日本 01\n"))
		case "/api/subscribe/rules":
			fmt.Fprint(w, `{"code":200,"msg":"","data":[{"key":"微信","value":"DOMAIN-SUFFIX,weixin.qq.com","policy":"DIRECT","sort":1},{"key":"streaming","value":"DOMAIN-SUFFIX,netflix.com","policy":"PROXY","sort":2}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	dir := t.TempDir()
	old := sub.HyperAccount
	sub.HyperAccount = &account.Client{
		Sessions: &account.Sessions{Path: filepath.Join(dir, "session")},
		Resolver: &account.Resolver{Builtin: []string{api.URL}, DoH: []string{"http://127.0.0.1:1/x"}},
		DeviceID: func() string { return "device-1" },
	}
	defer func() { sub.HyperAccount = old }()
	store := sub.OpenStore(filepath.Join(dir, "sources"))
	checkinPath = filepath.Join(dir, "checkin.json")
	ctx := context.Background()
	call := func(method string, in input) (map[string]any, []string, error) {
		t.Helper()
		data, changed, err := run(ctx, store, method, in, sub.Fetcher{})
		b, _ := json.Marshal(data)
		var m map[string]any
		json.Unmarshal(b, &m)
		return m, changed, err
	}

	if _, _, err := call("refresh", input{Source: sub.HyperSource}); err == nil {
		t.Fatal("refreshed without signing in")
	}
	if d, _, _ := call("send_code", input{Email: "a@b.c"}); d["result"] != true {
		t.Fatal(d)
	}
	if d, changed, _ := call("login", input{Email: "a@b.c", Code: "000000"}); d["result"] != false || d["msg"] != "验证码错误" || changed != nil {
		t.Fatal(d, changed)
	}
	d, changed, err := call("login", input{Email: "a@b.c", Code: "123456", Invite: "INV"})
	if err != nil || d["result"] != true || len(changed) != 1 || changed[0] != sub.HyperSource || loginBody["identifier"] != "device-1" || loginBody["invite"] != "INV" {
		t.Fatal(err, d, changed, loginBody)
	}
	if d, _, _ := call("account", input{}); d["signed_in"] != true || d["email"] != "a@b.c" {
		t.Fatal(d)
	}

	d, _, err = call("refresh", input{Source: sub.HyperSource})
	if err != nil || d["decision"] != "ready" || d["node_count"] != float64(1) {
		t.Fatal(err, d)
	}
	st, _ := store.Status(sub.HyperSource)
	if st.Usage == nil || st.Usage.Total == nil || *st.Usage.Total != 10<<30 {
		t.Fatalf("usage %+v", st.Usage)
	}
	d, _, _ = call("catalog", input{})
	if items, _ := d["items"].([]any); len(items) != 2 || items[0].(map[string]any)["key"] != "微信" || items[0].(map[string]any)["enabled"] != true {
		t.Fatal(d)
	}

	off := false
	if _, changed, err := call("rule_toggle", input{Key: "streaming", Enabled: &off}); err != nil || len(changed) != 1 {
		t.Fatal(err, changed)
	}
	call("refresh", input{Source: sub.HyperSource})
	p, _ := store.Load(sub.HyperSource)
	for _, set := range p.BinarySets {
		if strings.Contains(set.Tag, "streaming") {
			t.Fatal("a list turned off is still routed:", set.Tag)
		}
	}
	d, _, _ = call("catalog", input{})
	if items := d["items"].([]any); items[1].(map[string]any)["enabled"] != false || items[1].(map[string]any)["server_enabled"] != true {
		t.Fatal(d)
	}

	if d, _, _ := call("api", input{Path: "/v1/admin/x"}); d["error"] != "path not allowed" {
		t.Fatal(d)
	}
	if d, _, _ := call("api", input{Path: "/v1/public/user/info"}); d["ok"] != true || d["data"].(map[string]any)["signed"] != true {
		t.Fatal(d)
	}
	if d, _, _ := call("checkin", input{Date: time.Now().Format("2006-01-02")}); d["result"] != true || d["added"] != true {
		t.Fatal(d)
	}

	if _, changed, err := call("logout", input{}); err != nil || len(changed) != 1 || !loggedOut {
		t.Fatal(err, changed, loggedOut)
	}
	if st, _ := store.Status(sub.HyperSource); !st.Revoked {
		t.Fatalf("source not revoked: %+v", st)
	}
	if _, _, err := call("refresh", input{Source: sub.HyperSource}); err == nil || !errors.Is(err, sub.ErrRevoked) && !errors.Is(err, sub.ErrNotReady) && !errors.Is(err, sub.ErrLoginRequired) {
		t.Fatal("refreshed after signing out:", err)
	}
	// Signing in again renews the source the sign-out revoked; its disabled lists stay off.
	if d, _, err := call("login", input{Email: "a@b.c", Code: "123456"}); err != nil || d["result"] != true {
		t.Fatal(err, d)
	}
	if d, _, err := call("refresh", input{Source: sub.HyperSource}); err != nil || d["decision"] != "ready" {
		t.Fatal(err, d)
	}
}
