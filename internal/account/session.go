// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package account is the Hyper service client: device login, the session file, API
// discovery over encrypted DNS TXT records, and the subscription token.
//
// The router signs in as a device (Login-Type: device + a stable identifier), so it takes
// one device slot and may receive the subscription token. The JWT and token live only in
// /etc/hyper/session (0600); the browser never sees them.
package account

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

// Session is the signed-in account.
type Session struct {
	JWT             string          `json:"jwt"`
	Email           string          `json:"email,omitempty"`
	LoginAt         int64           `json:"login_at,omitempty"`
	SubToken        string          `json:"sub_token,omitempty"`
	UserSubscribeID json.RawMessage `json:"user_subscribe_id,omitempty"`
	ExpireTime      json.RawMessage `json:"expire_time,omitempty"`
}

// Identity is a stable, non-reversible id of the session, used to discard results of
// background work that finished after a logout or account change.
func (s *Session) Identity() string {
	if s == nil || s.JWT == "" {
		return ""
	}
	sum := md5.Sum([]byte(s.JWT))
	return hex.EncodeToString(sum[:])
}

// Sessions reads and writes the session file.
type Sessions struct {
	Path string
	mu   sync.Mutex
}

// Load returns the session, or nil when signed out (or the file is unreadable).
func (s *Sessions) Load() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, _ := s.load()
	return sess
}

// load reads the session and its file as it is (caller holds mu).
func (s *Sessions) load() (*Session, []byte) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, nil
	}
	var sess Session
	if json.Unmarshal(b, &sess) != nil || sess.JWT == "" {
		return nil, nil
	}
	return &sess, b
}

// Save replaces the session atomically (0600).
func (s *Sessions) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(sess, nil)
}

// save writes sess unless the file already holds exactly that (caller holds mu).
func (s *Sessions) save(sess *Session, current []byte) error {
	if sess == nil || sess.JWT == "" {
		return errors.New("empty session")
	}
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if current != nil && bytes.Equal(b, current) {
		return nil
	}
	return writeFile(s.Path, b, 0o600)
}

// Update changes the session only if it still has identity id. The check and the write
// are one step, so a sign-out or another account's sign-in meanwhile is never overwritten,
// and a session the change leaves as it is stays unwritten (the daily sync refreshes the
// subscription token, which seldom changes, and /etc is flash).
func (s *Sessions) Update(id string, f func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, current := s.load()
	if sess == nil || sess.Identity() != id {
		return ErrSessionChanged
	}
	f(sess)
	return s.save(sess, current)
}

// Clear signs out.
func (s *Sessions) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ErrSessionChanged means the account changed while a request was in flight.
var ErrSessionChanged = errors.New("session_changed")

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, mode)
}
