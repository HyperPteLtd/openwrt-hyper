// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package crypt opens Hyper's encrypted subscription envelopes and API discovery TXT records.
//
// Hyper service delivers the subscription (GET <api>/api/subscribe?token&country&client_public_key)
// as an envelope encrypted with X25519 + HKDF-SHA256 + AES-256-GCM, the same scheme the
// Hyper app uses (hyper-tunnel packages/app-client/src/subscribe/client.ts):
//
//   - shared secret = X25519(client_private, spk)
//   - key = HKDF-SHA256(ikm = shared, salt = envelope.salt, info = "subscribe-v1", L = 32)
//   - AES-256-GCM with the envelope nonce, no AAD, tag appended to the ciphertext
package crypt

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

// ErrDecode is an envelope or record that does not decode.
var ErrDecode = errors.New("invalid key, salt, nonce or data")

// DecodeB64 accepts standard or url-safe base64.
func DecodeB64(s string) ([]byte, error) {
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

// EncodeB64URL is unpadded url-safe base64, the form the API takes.
func EncodeB64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// Keygen returns an ephemeral X25519 key pair (private, public), 32 bytes each.
func Keygen(random io.Reader) ([]byte, []byte, error) {
	key, err := ecdh.X25519().GenerateKey(random)
	if err != nil {
		return nil, nil, err
	}
	return key.Bytes(), key.PublicKey().Bytes(), nil
}

// Open decrypts data (ciphertext || 16-byte tag) sent to the client key priv.
func Open(priv, spk, salt, nonce []byte, info string, data []byte) ([]byte, error) {
	if len(priv) != keyLen || len(spk) != keyLen || len(nonce) == 0 || len(data) <= tagLen {
		return nil, ErrDecode
	}
	clientKey, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, ErrDecode
	}
	serverKey, err := ecdh.X25519().NewPublicKey(spk)
	if err != nil {
		return nil, ErrDecode
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
