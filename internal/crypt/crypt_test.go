// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// Envelope sealed by the original C/mbedtls helper for the client key below.
var mbedtlsVector = struct{ priv, spk, salt, nonce, data, plain string }{
	priv:  "oJTLudnOm5XI42kjiHM5p6rI-xUHCsV8ysaq7FODH14",
	spk:   "OtKRgrKbooeuFksqC1nemE2/Acg4zZRyGhAOuZuHomM=",
	salt:  "rsAFkBn5fps80f/UGxObHaSIuiQwavqP/qJJah9+ogg=",
	nonce: "LNHgksN6pffcSqcu",
	data:  "tP3+PPsA+IEmIp1gjz3PPV7qX4gAg7+wrAeyBo3zS6jGgxoIlxfgVtPwt05qWiJb2eigk0HK83PoP7vbdmtoLpsKWsZiL/QoSTEG0BPFYio=",
	plain: "REMARKS=Hyper\nanytls://p@jp1.example.com:443#日本 · 东京 01",
}

func dec(t *testing.T, s string) []byte {
	t.Helper()
	b, err := DecodeB64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenVector(t *testing.T) {
	v := mbedtlsVector
	plain, err := Open(dec(t, v.priv), dec(t, v.spk), dec(t, v.salt), dec(t, v.nonce), "subscribe-v1", dec(t, v.data))
	if err != nil || string(plain) != v.plain {
		t.Fatalf("%q %v", plain, err)
	}
	if _, err = Open(dec(t, v.priv), dec(t, v.spk), dec(t, v.salt), dec(t, v.nonce), "other", dec(t, v.data)); err == nil {
		t.Fatal("wrong info accepted")
	}
}

func TestRoundTrip(t *testing.T) {
	priv, pub, err := Keygen(rand.Reader)
	if err != nil || len(priv) != 32 || len(pub) != 32 {
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
	data := aead.Seal(nil, nonce, []byte("hello"), nil)
	plain, err := Open(priv, server.PublicKey().Bytes(), salt, nonce, "subscribe-v1", data)
	if err != nil || string(plain) != "hello" {
		t.Fatal(plain, err)
	}
	if got := dec(t, EncodeB64URL(pub)); string(got) != string(pub) {
		t.Fatal("b64")
	}
	if _, err = Open(priv[:31], pub, salt, nonce, "x", data); err == nil {
		t.Fatal("short key accepted")
	}
}

const txtVector = "AAECAwQFBgcICQoLgBKV3OnfGGdC9EUAaTiJA78Ndy9yi/kkkDNX5WeBLhTrJfrvZDDA1W/JzEpU48YGnh6n"

func TestOpenTXT(t *testing.T) {
	plain, err := OpenTXT(txtVector)
	if err != nil || string(plain) != "v=1|api-one.example|api-two.example" {
		t.Fatalf("%q %v", plain, err)
	}
	tampered := dec(t, txtVector)
	tampered[len(tampered)-1] ^= 1
	for _, record := range []string{"", "v=1|x", "%%%", base64.StdEncoding.EncodeToString(tampered),
		"AAECAwQFBgcICQoLgBKW3OnfGGdC9EUAaTiJA78Nd4PWSzO46p5WUC4uuyoyEa4="} {
		if _, err := OpenTXT(record); err == nil {
			t.Errorf("accepted %q", record)
		}
	}
}
