// SPDX-License-Identifier: GPL-2.0-only
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package main is hyper-crypt, the subscription decryption helper of luci-app-hyper.
//
// PPanel delivers the subscription (GET <api>/api/subscribe?token&country&client_public_key)
// as an envelope encrypted with X25519 + HKDF-SHA256 + AES-256-GCM, the same scheme the
// Hyper app uses (hyper-tunnel packages/app-client/src/subscribe/client.ts):
//
//   - shared secret = X25519(client_private, spk)
//   - key = HKDF-SHA256(ikm = shared, salt = envelope.salt, info = "subscribe-v1", L = 32)
//   - AES-256-GCM with the envelope nonce, no AAD, tag appended to the ciphertext
//
// It replaces the earlier C/mbedtls helper with a static Go binary, so one build
// works on every OpenWrt release (23.05 ships mbedtls 2.28, 24.10 mbedtls 3.6).
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

const (
	keyLen = 32
	tagLen = 16
)

var errDecode = errors.New("invalid key, salt, nonce or data")

// decodeB64 accepts standard or url-safe base64, padded or not, with whitespace.
func decodeB64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t', '=':
			return -1
		case '-':
			return '+'
		case '_':
			return '/'
		}
		return r
	}, s)
	return base64.RawStdEncoding.DecodeString(s)
}

func encodeB64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// keygen returns an ephemeral X25519 key pair (private, public), 32 bytes each.
func keygen(random io.Reader) ([]byte, []byte, error) {
	key, err := ecdh.X25519().GenerateKey(random)
	if err != nil {
		return nil, nil, err
	}
	return key.Bytes(), key.PublicKey().Bytes(), nil
}

// open decrypts data (ciphertext || 16-byte tag) sent to the client key priv.
func open(priv, spk, salt, nonce []byte, info string, data []byte) ([]byte, error) {
	if len(priv) != keyLen || len(spk) != keyLen || len(nonce) == 0 || len(data) <= tagLen {
		return nil, errDecode
	}
	clientKey, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, errDecode
	}
	serverKey, err := ecdh.X25519().NewPublicKey(spk)
	if err != nil {
		return nil, errDecode
	}
	shared, err := clientKey.ECDH(serverKey)
	if err != nil {
		return nil, errors.New("x25519 failed")
	}
	key, err := hkdf.Key(sha256.New, shared, salt, info, keyLen)
	if err != nil {
		return nil, errors.New("hkdf failed")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithNonceSize(block, len(nonce))
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	return plain, nil
}
