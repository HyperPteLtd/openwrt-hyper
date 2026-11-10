// SPDX-License-Identifier: GPL-2.0-only
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Envelope sealed by the previous C/mbedtls implementation (src/hyper-crypt.c +
// test/seal.c in luci-app-hyper) for the client key below.
var mbedtlsVector = struct{ priv, spk, salt, nonce, data, plain string }{
	priv:  "oJTLudnOm5XI42kjiHM5p6rI-xUHCsV8ysaq7FODH14",
	spk:   "OtKRgrKbooeuFksqC1nemE2/Acg4zZRyGhAOuZuHomM=",
	salt:  "rsAFkBn5fps80f/UGxObHaSIuiQwavqP/qJJah9+ogg=",
	nonce: "LNHgksN6pffcSqcu",
	data:  "tP3+PPsA+IEmIp1gjz3PPV7qX4gAg7+wrAeyBo3zS6jGgxoIlxfgVtPwt05qWiJb2eigk0HK83PoP7vbdmtoLpsKWsZiL/QoSTEG0BPFYio=",
	plain: "REMARKS=Hyper\nanytls://p@jp1.example.com:443#日本 · 东京 01",
}

// seal plays the server side of the envelope (what PPanel does).
func seal(t *testing.T, clientPub []byte, info string, plain []byte) (spk, salt, nonce, data []byte) {
	t.Helper()
	server, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := ecdh.X25519().NewPublicKey(clientPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := server.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	salt = make([]byte, 32)
	nonce = make([]byte, 12)
	rand.Read(salt)
	rand.Read(nonce)
	key, err := hkdf.Key(sha256.New, shared, salt, info, 32)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	return server.PublicKey().Bytes(), salt, nonce, aead.Seal(nil, nonce, plain, nil)
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decodeB64(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func TestOpenMbedtlsVector(t *testing.T) {
	v := mbedtlsVector
	got, err := open(mustDecode(t, v.priv), mustDecode(t, v.spk), mustDecode(t, v.salt), mustDecode(t, v.nonce), "subscribe-v1", mustDecode(t, v.data))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != v.plain {
		t.Fatalf("got %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	priv, pub, err := keygen(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"nodes":[{"name":"日本 · 东京 01","protocol":"vless","port":443}]}`)
	spk, salt, nonce, data := seal(t, pub, "subscribe-v1", plain)

	got, err := open(priv, spk, salt, nonce, "subscribe-v1", data)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	if _, err := open(priv, spk, salt, nonce, "other-info", data); err == nil {
		t.Error("wrong info accepted")
	}
	tampered := append([]byte(nil), data...)
	tampered[0] ^= 1
	if _, err := open(priv, spk, salt, nonce, "subscribe-v1", tampered); err == nil {
		t.Error("tampered ciphertext accepted")
	}
	other, _, _ := keygen(rand.Reader)
	if _, err := open(other, spk, salt, nonce, "subscribe-v1", data); err == nil {
		t.Error("another client's key accepted")
	}
}

func TestDecodeB64Forms(t *testing.T) {
	raw := []byte{0xfb, 0xff, 0xbf, 0x01, 0x02}
	for _, s := range []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		" " + base64.StdEncoding.EncodeToString(raw)[:4] + "\n" + base64.StdEncoding.EncodeToString(raw)[4:] + "\r\n",
	} {
		got, err := decodeB64(s)
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("decodeB64(%q) = %x, %v", s, got, err)
		}
	}
	if _, err := decodeB64("not*base64"); err == nil {
		t.Error("garbage accepted")
	}
}

// The command-line contract is what sync_subscription relies on.
func TestCLI(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"keygen"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("keygen exit %d: %s", code, errOut.String())
	}
	var kp struct{ PrivateKey, PublicKey string }
	if err := json.Unmarshal(out.Bytes(), &struct {
		PrivateKey *string `json:"private_key"`
		PublicKey  *string `json:"public_key"`
	}{&kp.PrivateKey, &kp.PublicKey}); err != nil {
		t.Fatalf("keygen output %q: %v", out.String(), err)
	}
	if len(kp.PublicKey) != 43 || strings.ContainsAny(kp.PublicKey+kp.PrivateKey, "+/=") {
		t.Fatalf("keys must be unpadded url-safe base64 of 32 bytes: %q", out.String())
	}

	plain := []byte("REMARKS=Hyper\nvless://u@h:443#n")
	spk, salt, nonce, data := seal(t, mustDecode(t, kp.PublicKey), "subscribe-v1", plain)
	t.Setenv("HYPER_CRYPT_PRIVATE_KEY", kp.PrivateKey)
	b := base64.StdEncoding.EncodeToString
	out.Reset()
	// url-safe, unpadded data on stdin is accepted too
	stdin := strings.NewReader(base64.RawURLEncoding.EncodeToString(data))
	if code := run([]string{"open", b(spk), b(salt), b(nonce), "subscribe-v1"}, stdin, &out, &errOut); code != 0 || !bytes.Equal(out.Bytes(), plain) {
		t.Fatalf("open exit %d, output %q, stderr %q", code, out.String(), errOut.String())
	}
	if code := run([]string{"open", b(spk), b(salt), b(nonce), "other-info"}, strings.NewReader(b(data)), &out, &errOut); code != exitCrypto {
		t.Errorf("wrong info: exit %d, want %d", code, exitCrypto)
	}

	t.Setenv("HYPER_CRYPT_PRIVATE_KEY", "")
	if code := run([]string{"open", "a", "b", "c", "d"}, strings.NewReader(""), &out, &errOut); code != exitDecode {
		t.Errorf("bad input: exit %d, want %d", code, exitDecode)
	}
	os.Unsetenv("HYPER_CRYPT_PRIVATE_KEY")
	if code := run([]string{"open", "a", "b", "c", "d"}, strings.NewReader(""), &out, &errOut); code != exitUsage {
		t.Errorf("no key: exit %d, want %d", code, exitUsage)
	}
	if code := run(nil, nil, &out, &errOut); code != exitUsage {
		t.Errorf("usage: exit %d, want %d", code, exitUsage)
	}
}
