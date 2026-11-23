// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package hypersync

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
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/account"
	"github.com/HyperPteLtd/openwrt-hyper/internal/crypt"
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
	countries    map[string]string // code -> body; "" = 500
	onCountry    func()
	unauthorized bool
	noRules      bool
	ruleHits     atomic.Int32
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{countries: map[string]string{
		"JP": "REMARKS=Hyper\nSTATUS=Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09\nanytls://pw@jp.example.com:443?sni=jp.example.com#Tokyo\nssr://bad",
		// base64 as a whole, as some panels send it
		"SG": base64.StdEncoding.EncodeToString([]byte("trojan://pw@sg.example.com:443?sni=sg.example.com#Singapore\n")),
	}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/v1/public/user/subscribe":
			if f.unauthorized {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"code":401,"msg":"expired","data":null}`)
				return
			}
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
			if r.Header.Get("Authorization") != "Bearer jwt" || f.noRules {
				w.WriteHeader(503)
				return
			}
			fmt.Fprintf(w, `{"code":200,"msg":"","data":[
				{"key":"streaming","value":"%s/lists/streaming.yaml","policy":"proxy","sort":20},
				{"key":"cn-direct","value":"DOMAIN-SUFFIX,baidu.com\nGEOIP,CN\nPROCESS-NAME,x","policy":"DIRECT","sort":10},
				{"key":"bad\nkey","value":"x","policy":"DIRECT"},
				{"key":"微信常用域名和 IP","value":"DOMAIN-SUFFIX,weixin.qq.com","policy":"DIRECT","sort":15},
				{"key":"gone","value":"%s/lists/gone","policy":"PROXY","sort":25},
				{"key":"off","value":"+.off.example","policy":"PROXY","enabled":false,"sort":30}]}`, f.URL, f.URL)
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

func newReader(t *testing.T, f *fakeAPI) *Reader {
	dir := t.TempDir()
	c := &account.Client{
		Sessions: &account.Sessions{Path: filepath.Join(dir, "session")},
		Resolver: &account.Resolver{Builtin: []string{f.URL}, DoH: []string{"http://127.0.0.1:1/x"}},
	}
	if err := c.Sessions.Save(&account.Session{JWT: "jwt", Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	return &Reader{Client: c}
}

func TestRead(t *testing.T) {
	f := newFake(t)
	feed, err := newReader(t, f).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Countries) != 2 || feed.Countries[0].Code != "JP" || feed.Countries[0].Sort != 9 || feed.Countries[1].Code != "SG" || len(feed.Failed) != 0 {
		t.Fatalf("%+v", feed)
	}
	// REMARKS= / STATUS= are taken out; the share links stay, base64 decoded.
	if feed.Remarks != "Hyper" || feed.Status != "Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09" ||
		strings.Contains(string(feed.Countries[0].Body), "STATUS=") || !strings.HasPrefix(string(feed.Countries[1].Body), "trojan://") {
		t.Fatalf("%q %q %q %q", feed.Remarks, feed.Status, feed.Countries[0].Body, feed.Countries[1].Body)
	}
	c := feed.Catalog
	if c == nil || len(c.Errors) != 1 {
		t.Fatalf("%+v", c)
	}
	var keys []string
	for _, l := range c.Lists {
		keys = append(keys, l.Key)
	}
	if !slices.Equal(keys, []string{"cn-direct", "微信常用域名和 IP", "streaming", "gone", "off"}) {
		t.Fatal(keys)
	}
	if l := c.Lists[2]; l.Parsed == nil || !slices.Equal(l.Parsed.Suffix, []string{"netflix.com"}) || !slices.Equal(l.Parsed.CIDR, []string{"1.2.3.0/24"}) {
		t.Fatalf("%+v", l)
	}
	if l := c.Lists[3]; l.Parsed != nil || l.Status != 404 {
		t.Fatalf("a list that could not be read: %+v", l)
	}
	if l := c.Lists[4]; l.Enabled || l.Parsed == nil {
		t.Fatalf("a list the server turned off is still read: %+v", l)
	}
	if u, total, exp := ParseStatus(feed.Status); u != "1.5 GB" || total != "100 GB" || exp != "2027-11-09" {
		t.Fatal(u, total, exp)
	}
}

func TestReadFailures(t *testing.T) {
	f := newFake(t)
	r := newReader(t, f)
	f.countries["SG"] = ""
	f.noRules = true
	feed, err := r.Read(context.Background())
	if err != nil || !slices.Equal(feed.Failed, []string{"SG"}) || len(feed.Countries) != 1 || feed.Catalog != nil {
		t.Fatalf("%v %+v", err, feed)
	}
	f.countries["JP"] = ""
	if _, err := r.Read(context.Background()); err == nil || err.Error() != "nodes_unreachable" {
		t.Fatal(err)
	}
	f.unauthorized = true
	if _, err := r.Read(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatal(err)
	}
}

func TestReadSessionChange(t *testing.T) {
	f := newFake(t)
	r := newReader(t, f)
	f.onCountry = func() { r.Client.Sessions.Save(&account.Session{JWT: "other"}) }
	if _, err := r.Read(context.Background()); !errors.Is(err, ErrSessionChanged) {
		t.Fatal(err)
	}
	r.Client.Sessions.Clear()
	if _, err := r.Read(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal(err)
	}
}

func TestParseRuleList(t *testing.T) {
	l := ParseRuleList("# c\n+.a.com\n.b.com\nc.com\n10.0.0.0/8\n- \"DOMAIN,X.com\"\nnot a domain\nDOMAIN,\n")
	if !slices.Equal(l.Suffix, []string{"a.com", "b.com"}) || !slices.Equal(l.Domain, []string{"c.com", "x.com"}) || l.Count != 5 || l.Skipped != 2 {
		t.Fatalf("%+v", l)
	}
}

func TestParseLocations(t *testing.T) {
	got := ParseLocations(json.RawMessage(`{"data":{"locations":{"US":{"sort":1},"HK":{},"JP":{"sort":1}}}}`))
	if len(got) != 3 || got[0].Code != "JP" || got[1].Code != "US" || got[2].Code != "HK" {
		t.Fatal(got)
	}
}

// A rule list cut off on the way is tried the other way; one over the limit is refused
// instead of kept cut short.
func TestDownloadCutOff(t *testing.T) {
	var direct, other atomic.Int32
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
	r := &run{Reader: &Reader{Alt: "fallback", HTTP: func(way string) *http.Client {
		if way != "" {
			return &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				other.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("DOMAIN-SUFFIX,a.example\nDOMAIN-SUFFIX,b.example\n")), Request: req}, nil
			})}
		}
		return srv.Client()
	}}, ctx: context.Background()}
	body, _ := r.download(srv.URL + "/cut")
	if direct.Load() != 1 || other.Load() != 1 || !strings.Contains(string(body), "b.example") || r.way == "" {
		t.Fatalf("direct %d, other way %d: %q", direct.Load(), other.Load(), body)
	}
	r.way, r.Alt = "", ""
	if body, status := r.download(srv.URL + "/big"); body != nil || status != 200 {
		t.Fatalf("kept %d bytes of a list over the limit (HTTP %d)", len(body), status)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
