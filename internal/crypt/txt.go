// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"unicode/utf8"
)

// Same bootstrap key and wire format as hyper-tunnel's domainResolver.ts:
// base64(12-byte nonce || AES-GCM ciphertext || tag), plaintext v=1|host|host.
const txtKeyBase64 = "Ip/dK3Y7Z7nJw01INIGVXw=="

// OpenTXT decrypts an API discovery TXT record to its "v=1|host|..." plaintext.
func OpenTXT(record string) ([]byte, error) {
	key, err := DecodeB64(txtKeyBase64)
	if err != nil {
		return nil, ErrDecode
	}
	data, err := DecodeB64(record)
	if err != nil || len(data) <= 12+tagLen {
		return nil, ErrDecode
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
		return nil, ErrDecode
	}
	text := strings.TrimSpace(string(plain))
	if !strings.HasPrefix(text, "v=1|") {
		return nil, ErrDecode
	}
	return []byte(text), nil
}
