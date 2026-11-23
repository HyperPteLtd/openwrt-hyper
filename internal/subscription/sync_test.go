// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package subscription

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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/crypt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mihomo"
	"github.com/HyperPteLtd/openwrt-hyper/internal/nodes"
	"github.com/HyperPteLtd/openwrt-hyper/internal/rulekey"
)

func seal(t *testing.T, clientPub string, plain string) map[string]string {
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

type fakeAPI struct {
	*httptest.Server
	countries map[string]string // code -> body; "" = 500
	onCountry func()
	ruleHits  atomic.Int32
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{countries: map[string]string{
		"JP": "REMARKS=Hyper\nSTATUS=Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09\nanytls://pw@jp.example.com:443?sni=jp.example.com#Tokyo\nssr://bad",
		"SG": "anytls://pw@sg.example.com:443?sni=sg.example.com#Singapore",
	}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/v1/public/user/subscribe":
			fmt.Fprint(w, `{"code":200,"msg":"","data":{"list":[{"id":1,"token":"tok","status":1}]}}`)
		case "/api/subscribe/location":
			if q.Get("token") != "tok" {
				w.WriteHeader(403)
				return
			}
			fmt.Fprint(w, `{"locations":{"SG":{"sort":5},"JP":{"sort":"9"},"bad code":{}}}`)
		case "/api/subscribe":
			if f.onCountry != nil {
				f.onCountry()
			}
			body, ok := f.countries[q.Get("country")]
			if !ok || body == "" {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(seal(t, q.Get("client_public_key"), body))
		case "/api/subscribe/rules":
			if r.Header.Get("Authorization") != "Bearer jwt" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprintf(w, `{"code":200,"msg":"","data":[
				{"key":"streaming","value":"%s/lists/streaming.yaml","policy":"proxy","sort":20},
				{"key":"cn-direct","value":"DOMAIN-SUFFIX,baidu.com\nGEOIP,CN\nPROCESS-NAME,x","policy":"DIRECT","sort":10},
				{"key":"bad\nkey","value":"x","policy":"DIRECT"},
				{"key":"微信常用域名和 IP","value":"DOMAIN-SUFFIX,weixin.qq.com","policy":"DIRECT","sort":15},
				{"key":"off","value":"+.off.example","policy":"PROXY","enabled":false,"sort":30}]}`, f.URL)
		case "/lists/streaming.yaml":
			f.ruleHits.Add(1)
			fmt.Fprint(w, "payload:\n  - 'DOMAIN-SUFFIX,netflix.com'\n  - DOMAIN-KEYWORD,nflx\n  - IP-CIDR,1.2.3.0/24\n  - IP-CIDR,bad\n")
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func newSyncer(t *testing.T, f *fakeAPI) *Syncer {
	dir := t.TempDir()
	c := &account.Client{
		Sessions: &account.Sessions{Path: filepath.Join(dir, "session")},
		Resolver: &account.Resolver{Builtin: []string{f.URL}, DoH: []string{"http://127.0.0.1:1/x"}},
	}
	if err := c.Sessions.Save(&account.Session{JWT: "jwt", Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	return &Syncer{Client: c, NodesPath: filepath.Join(dir, "nodes.json"), RulesDir: filepath.Join(dir, "rules"), InsecureDefault: true}
}

func TestSync(t *testing.T) {
	f := newFake(t)
	s := newSyncer(t, f)
	out, err := s.Run(context.Background())
	if err != nil || !out.State.OK {
		t.Fatalf("%v %+v", err, out.State)
	}
	st := out.State
	if st.Count != 2 || st.Added != 2 || st.Invalid+len(st.Unsupported) != 1 || !slices.Equal(st.Countries, []string{"JP", "SG"}) ||
		st.Remarks != "Hyper" || st.TrafficUsed != "1.5 GB" || st.Expires != "2027-11-09" || st.Rules == nil || *st.Rules != 4 {
		t.Fatalf("%+v", st)
	}
	if !out.NodesChanged || !out.RulesChanged {
		t.Fatal("changes not reported")
	}
	store, err := nodes.Load(s.NodesPath)
	if err != nil || len(store.Nodes) != 2 || store.Nodes[0].Label != "Tokyo" || store.Nodes[0].Sort != 9 {
		t.Fatalf("%v %+v", err, store)
	}
	sess := s.Client.Sessions.Load()
	if sess.SubToken != "tok" || sess.JWT != "jwt" {
		t.Fatal(sess)
	}

	// The renderer reads what the sync wrote.
	rules, err := mihomo.LoadServerRules(s.RulesDir, nil)
	if err != nil || len(rules) != 3 {
		t.Fatalf("%v %+v", err, rules)
	}
	// The server names lists in Chinese too: kept under that name, stored under an ASCII ID.
	if wx := rules[1]; wx.Key != "微信常用域名和 IP" || !wx.Direct || !slices.Equal(wx.Suffix, []string{"weixin.qq.com"}) {
		t.Fatalf("%+v", wx)
	}
	if _, err := os.Stat(filepath.Join(s.RulesDir, rulekey.ID("微信常用域名和 IP")+".domain.json")); err != nil {
		t.Fatal(err)
	}
	rules = slices.Delete(rules, 1, 2)
	if rules[0].Key != "cn-direct" || !rules[0].Direct || !rules[0].GeoIPCN || !slices.Equal(rules[0].Suffix, []string{"baidu.com"}) {
		t.Fatalf("%+v", rules[0])
	}
	if rules[1].Key != "streaming" || rules[1].Direct || !slices.Equal(rules[1].Keyword, []string{"nflx"}) || !slices.Equal(rules[1].CIDR, []string{"1.2.3.0/24"}) {
		t.Fatalf("%+v", rules[1])
	}

	// Unchanged second run.
	out, _ = s.Run(context.Background())
	if out.NodesChanged || out.RulesChanged || out.State.Added != 0 {
		t.Fatalf("%+v", out)
	}

	// SG fails: its nodes stay; a list URL fails: the previous copy stays (stale).
	f.countries["SG"] = ""
	f.countries["JP"] = "anytls://pw@jp2.example.com:443?sni=jp2.example.com#Tokyo 2"
	f.Config.Handler = wrap(f.Config.Handler, "/lists/streaming.yaml")
	out, _ = s.Run(context.Background())
	store, _ = nodes.Load(s.NodesPath)
	var labels []string
	for _, n := range store.Nodes {
		labels = append(labels, n.Label)
	}
	if !slices.Equal(labels, []string{"Tokyo 2", "Singapore"}) || out.State.Removed != 1 || !slices.Equal(out.State.Failed, []string{"SG"}) {
		t.Fatalf("%v %+v", labels, out.State)
	}
	rules, _ = mihomo.LoadServerRules(s.RulesDir, nil)
	if len(rules) != 3 || rules[2].Key != "streaming" {
		t.Fatalf("stale list dropped: %+v", rules)
	}

	// Every country failing keeps the nodes and reports the failure.
	f.countries["JP"] = ""
	out, _ = s.Run(context.Background())
	if out.State.OK || out.State.Error != "nodes" {
		t.Fatalf("%+v", out.State)
	}
	if store2, _ := nodes.Load(s.NodesPath); len(store2.Nodes) != 2 {
		t.Fatal("nodes replaced after total failure")
	}
}

func wrap(h http.Handler, failPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == failPath {
			w.WriteHeader(404)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func TestSyncSessionChange(t *testing.T) {
	f := newFake(t)
	s := newSyncer(t, f)
	f.onCountry = func() { s.Client.Sessions.Save(&account.Session{JWT: "other"}) }
	_, err := s.Run(context.Background())
	if !errors.Is(err, ErrSessionChanged) {
		t.Fatal(err)
	}
	if store, _ := nodes.Load(s.NodesPath); len(store.Nodes) != 0 {
		t.Fatal("wrote nodes for a stale session")
	}
	s.Client.Sessions.Clear()
	out, err := s.Run(context.Background())
	if err != nil || out.State.Error != "not_signed_in" {
		t.Fatal(err, out.State)
	}
}

func TestParseRuleList(t *testing.T) {
	l := ParseRuleList("# c\n+.a.com\n.b.com\nc.com\n10.0.0.0/8\n- \"DOMAIN,X.com\"\nnot a domain\nDOMAIN,\n")
	if !slices.Equal(l.Suffix, []string{"a.com", "b.com"}) || !slices.Equal(l.Domain, []string{"c.com", "x.com"}) || l.Count != 5 || l.Skipped != 2 {
		t.Fatalf("%+v", l)
	}
	if d, ip := (RuleList{}).Files(); d != nil || ip != nil {
		t.Fatal("empty list wrote files")
	}
}

func TestParseLocations(t *testing.T) {
	got := ParseLocations(json.RawMessage(`{"data":{"locations":{"US":{"sort":1},"HK":{},"JP":{"sort":1}}}}`))
	if len(got) != 3 || got[0].Code != "JP" || got[1].Code != "US" || got[2].Code != "HK" {
		t.Fatal(got)
	}
}

// A rule list cut off on the way is tried over the other route; one over the limit is
// refused instead of kept cut short.
func TestDownloadCutOff(t *testing.T) {
	var direct, viaCore atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cut":
			direct.Add(1)
			w.Header().Set("Content-Length", "100")
			fmt.Fprint(w, "DOMAIN-SUFFIX,a.example\n") // then the connection ends
		case "/big":
			w.Write(make([]byte, maxRuleList+1))
		}
	}))
	defer srv.Close()
	r := &run{Syncer: &Syncer{HTTP: func(proxy string) *http.Client {
		if proxy != "" {
			return &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				viaCore.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("DOMAIN-SUFFIX,a.example\nDOMAIN-SUFFIX,b.example\n")), Request: req}, nil
			})}
		}
		return srv.Client()
	}}, ctx: context.Background(), alt: "socks5://127.0.0.1:7893"}
	body, _ := r.download(srv.URL + "/cut")
	if direct.Load() != 1 || viaCore.Load() != 1 || !strings.Contains(string(body), "b.example") || r.proxy == "" {
		t.Fatalf("direct %d, via core %d: %q", direct.Load(), viaCore.Load(), body)
	}
	r.proxy, r.alt = "", ""
	if body, status := r.download(srv.URL + "/big"); body != nil || status != 200 {
		t.Fatalf("kept %d bytes of a list over the limit (HTTP %d)", len(body), status)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
