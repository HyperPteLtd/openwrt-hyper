package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// Select a usable source on first import or when opening an older installation
// with an empty selection. Selecting a source never changes the power setting.
func selectInitialSource(m *router.Manager, store *subscription.Store, updated string) (bool, error) {
	intent := m.Status().Intent
	if intent.Source != "" {
		return false, nil
	}
	eligible := func(st subscription.Status) bool { return st.Ready && !st.LoginRequired && st.NodeCount > 0 }
	id := updated
	if id != "" {
		st, err := store.Status(id)
		if err != nil || !eligible(st) {
			return false, err
		}
	} else {
		sources, err := store.List()
		if err != nil {
			return false, err
		}
		for _, source := range sources {
			if eligible(source) {
				id = source.Source
				break
			}
		}
	}
	if id == "" {
		return false, nil
	}
	_, err := m.Change(&intent.Revision, func(i *router.Intent) error { i.Source = id; return nil })
	if err != nil && m.Status().Intent.Source != "" {
		return false, nil // a concurrent import or manual selection won
	}
	return err == nil, err
}

// Validate a replacement while the previous working config still serves LAN.
// Only the newest request may commit the revision it inspected.
func (s *app) change(ctx context.Context, method string, r request) (any, error) {
	status := s.m.Status()
	next := status.Intent
	if r.Revision != nil && *r.Revision != next.Revision {
		return nil, router.ErrRevisionChanged
	}
	switch method {
	case "mode":
		if r.Mode != "smart" && r.Mode != "global" && r.Mode != "direct" {
			return nil, errors.New("unknown routing mode")
		}
		next.Mode = r.Mode
	case "source":
		if _, e := subscription.OpenStore(router.Root + "/sources").Load(r.Source); e != nil {
			return nil, errors.New("source unavailable")
		}
		if next.Source == r.Source {
			return revisionTask(next.Revision), nil
		}
		next.Source = r.Source
	case "policy":
		next.Policy = r.Policy
	case "save_rules":
		var rules []router.CustomRule
		if json.Unmarshal(r.Args, &rules) != nil {
			return nil, errors.New("invalid custom rules")
		}
		next.Policy.Rules = rules
	default:
		return nil, errors.New("unknown configuration operation")
	}
	if e := router.ValidatePolicy(next.Policy); e != nil {
		return nil, e
	}
	s.edits.Lock()
	s.sequence++
	seq := s.sequence
	s.edits.Unlock()
	commit := func() (uint64, error) {
		s.edits.Lock()
		defer s.edits.Unlock()
		if s.sequence != seq {
			return 0, errors.New("newer configuration accepted")
		}
		revision, e := s.m.Change(&status.Intent.Revision, func(i *router.Intent) error {
			i.Source = next.Source
			i.Mode = next.Mode
			i.Policy = next.Policy
			return nil
		})
		if e == nil {
			s.tests.reset()
			s.sys.Records().Log("service", "info", method)
		}
		return revision, e
	}
	if status.Phase == "running" && next.Mode != "direct" {
		return s.runJob("", "configure/"+method, func(ctx context.Context) (json.RawMessage, error) {
			if e := s.sys.Preflight(ctx, next, nil, ""); e != nil {
				return nil, e
			}
			revision, e := commit()
			if e != nil {
				return nil, e
			}
			out, _ := json.Marshal(revisionTask(revision))
			return out, nil
		})
	}
	id, e := commit()
	return revisionTask(id), e
}
