package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"io"
	"os"
	"syscall"
	"time"
)

// Called over the root-only socket by the subscription worker. Confirmation
// uses a short request and never tries to acquire a source write lock.
func (s *app) reviewSource(ctx context.Context, r request) (string, error) {
	token, i, needed, e := s.m.ReviewToken(r.Source)
	if e != nil {
		return "", e
	}
	if r.Token != "" {
		if r.Token != token {
			return "", errors.New("configuration intent has changed")
		}
		return token, nil
	}
	var p subscription.Profile
	if json.Unmarshal(r.Args, &p) != nil || p.Source != r.Source || p.SchemaVersion != subscription.SchemaVersion {
		return "", errors.New("invalid candidate")
	}
	if needed {
		if e = s.sys.Preflight(ctx, i, &p, ""); e != nil {
			return "", e
		}
	}
	current, _, _, e := s.m.ReviewToken(r.Source)
	if e != nil || current != token {
		return "", errors.New("configuration changed during preflight")
	}
	return token, nil
}
func offlineToken() (string, error) {
	for _, marker := range []string{"owned", "held"} {
		if _, e := os.Stat(router.RunDir + "/network/" + marker); !os.IsNotExist(e) {
			return "", errors.New("network ownership must first be released")
		}
	}
	if e := os.MkdirAll(router.RunDir, 0711); e != nil {
		return "", e
	}
	f, e := os.OpenFile(router.RunDir+"/daemon.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return "", e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return "", errors.New("manager is starting or unavailable")
	}
	b, e := os.ReadFile(router.Root + "/private/intent.json")
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	h := sha256.Sum256(b)
	return "offline-" + hex.EncodeToString(h[:]), nil
}
func validateSource(args []string) error {
	var p json.RawMessage
	var expected string
	if len(args) == 2 {
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 24<<20+1))
		if e != nil || len(b) > 24<<20 || !json.Valid(b) {
			return errors.New("invalid candidate snapshot")
		}
		p = b
	} else {
		expected = args[2]
	}
	token := ""
	var e error
	if _, err := os.Stat(socket); os.IsNotExist(err) {
		token, e = offlineToken()
	} else {
		req, err := json.Marshal(request{Source: args[1], Args: p, Token: expected})
		if err != nil {
			return err
		}
		c, end := context.WithTimeout(context.Background(), time.Minute)
		defer end()
		b, err := call(c, "validate_source", req)
		if err != nil {
			return errors.New("source preflight failed")
		}
		e = json.NewDecoder(bytes.NewReader(b)).Decode(&token)
	}
	if e != nil {
		return e
	}
	if token == "" || expected != "" && expected != token {
		return errors.New("source preflight expired")
	}
	_, e = fmt.Fprintln(os.Stdout, token)
	return e
}
