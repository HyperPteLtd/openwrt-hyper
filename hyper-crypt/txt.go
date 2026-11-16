// SPDX-License-Identifier: GPL-2.0-only
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"unicode/utf8"
)

// Same bootstrap key and wire format as hyper-tunnel's domainResolver.ts:
// base64(12-byte nonce || AES-GCM ciphertext || tag), plaintext v=1|host|host.
const txtKeyBase64 = "Ip/dK3Y7Z7nJw01INIGVXw=="

func openTXT(record string) ([]byte, error) {
	key, err := decodeB64(txtKeyBase64)
	if err != nil {
		return nil, errDecode
	}
	data, err := decodeB64(record)
	if err != nil || len(data) <= 12+tagLen {
		return nil, errDecode
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, data[:12], data[12:], nil)
	if err != nil || !utf8.Valid(plain) {
		return nil, errDecode
	}
	text := strings.TrimSpace(string(plain))
	if !strings.HasPrefix(text, "v=1|") {
		return nil, errDecode
	}
	return []byte(text), nil
}
