package kernel

import (
	"bytes"
	"context"
	"errors"
	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func (m *Manager) Select(tag string, rollback bool, expected ...uint64) error {
	return m.locked(func(s *state) error {
		if len(expected) > 0 && s.Revision != expected[0] {
			return errors.New("kernel selection revision changed")
		}
		if s.Pending != nil || s.Repair {
			return errors.New("kernel transaction in progress")
		}
		var selected *pick
		if rollback {
			selected = s.Previous
		} else {
			for _, p := range []*pick{s.Staged, s.Selected, s.Active, s.Previous} {
				if p != nil && p.Tag == tag {
					selected = p
					break
				}
			}
		}
		if selected == nil {
			return errors.New("requested core is not retained locally")
		}
		old := s.Selected
		s.Selected = selected
		if selected == s.Staged {
			s.Staged = nil
		}
		if e := m.commit(s); e != nil {
			return e
		}
		m.forget(old, s)
		return nil
	})
}

// Reject restores the last confirmed selection after a preflight failure.
// A concurrent install or a newer trial owns its own selection.
func (m *Manager) Reject(tag string) error {
	return m.locked(func(s *state) error {
		if s.Pending != nil || s.Repair || s.Active == nil || s.Selected == nil || s.Selected.Tag != tag {
			return errors.New("candidate is no longer eligible for fallback")
		}
		old := s.Selected
		s.Selected = s.Active
		if e := m.commit(s); e != nil {
			return e
		}
		m.forget(old, s)
		return nil
	})
}

// Check verifies an installed binary and checks a disposable copy of the new
// configuration while the confirmed process continues to serve traffic.
func (m *Manager) Check(ctx context.Context, tag, config string) error {
	return m.locked(func(s *state) error {
		var p *pick
		for _, ref := range []*pick{s.Staged, s.Selected, s.Active, s.Previous} {
			if ref != nil && ref.Tag == tag {
				p = ref
				break
			}
		}
		if p == nil {
			return errors.New("requested core is not installed")
		}
		v, e := m.stored(p)
		if e != nil {
			return e
		}
		if e = m.verifySlot(*p, v); e != nil {
			return e
		}
		buf, e := readSmall(config, 16<<20)
		if e != nil || !validConfig(buf) {
			return errors.New("cannot read valid candidate configuration")
		}
		token, e := uidToken()
		if e != nil {
			return e
		}
		path := filepath.Join(m.Root, "configs", ".preflight-"+token)
		space, e := m.Capacity(m.Root, m.Temp)
		if e != nil {
			return e
		}
		if space.Persistent.Free < int64(len(buf))+16*MiB {
			return errors.New("insufficient space for configuration check")
		}
		if e = writeAtomic(path, buf, 0600); e != nil {
			return e
		}
		defer os.Remove(path)
		return m.Inspect(ctx, m.launch(*p, v), path)
	})
}
func (m *Manager) BeginAuthorized(ctx context.Context, config, expectedTag string, authorities []string) (launch Launch, err error) {
	authorities = slices.Clone(authorities)
	slices.Sort(authorities)
	authorities = slices.Compact(authorities)
	if len(authorities) > 1024 {
		return Launch{}, errors.New("too many configuration authorities")
	}
	for _, a := range authorities {
		if !validAuthority(a) {
			return Launch{}, errors.New("invalid configuration authority")
		}
	}

	err = m.locked(func(s *state) error {
		if s.Pending != nil || s.Repair {
			return errors.New("kernel transaction in progress")
		}
		v, e := m.stored(s.Selected)
		if e != nil {
			return e
		}
		p := *s.Selected
		if expectedTag != "" && p.Tag != expectedTag {
			return errors.New("selected kernel changed during compilation")
		}
		if e = m.verifySlot(p, v); e != nil {
			if !p.Volatile {
				return e
			}
			if e = m.restore(ctx, p, v); e != nil {
				return e
			}
		}
		// Always validate the caller's newly authorized configuration, including
		// when selecting a previous binary whose historical snapshot is retained.
		data, e := readSmall(config, 16<<20)
		if e != nil {
			return errors.New("cannot read candidate configuration")
		}
		if !validConfig(data) {
			return errors.New("invalid candidate configuration")
		}
		p.Authorities = authorities
		if s.Active != nil && s.Active.Slot == p.Slot && s.Active.Config != "" && slices.Equal(s.Active.Authorities, authorities) {
			l := m.launch(*s.Active, v)
			old, e := readSmall(l.Config, 16<<20)
			if e == nil && Digest(old) == Digest(data) {
				launch = l
				return nil
			}
		}
		token, e := uidToken()
		if e != nil {
			return e
		}
		p.Config = token
		launch = m.launch(p, v)
		launch.Token = token
		cap, e := m.Capacity(m.Root, m.Temp)
		if e != nil {
			return e
		}
		if cap.Persistent.Free < 16*MiB+int64(len(data))+2*MiB {
			return errors.New("insufficient storage for the config snapshot and reserve")
		}
		if e = writeAtomic(launch.Config, data, 0600); e != nil {
			return e
		}
		if e = m.Inspect(ctx, launch, launch.Config); e != nil {
			os.Remove(launch.Config)
			return e
		}
		s.Pending = &trial{token, p}
		return m.commit(s)
	})
	return
}
func validAuthority(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	return strings.Trim(v, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789:._-") == ""
}
func (m *Manager) Invalidate(source string) error {
	if !validAuthority(source) {
		return errors.New("invalid configuration authority")
	}
	return m.locked(func(s *state) error {
		keep := map[string]bool{}
		changed := false
		clear := func(p *pick) bool {
			if p == nil || p.Config == "" {
				return false
			}
			if len(p.Authorities) > 0 && !slices.Contains(p.Authorities, source) {
				keep[p.Config+".yaml"] = true
				return false
			}
			p.Config = ""
			p.Authorities = nil
			changed = true
			return true
		}
		for _, p := range []*pick{s.Active, s.Previous, s.Selected, s.Staged, s.RuntimeCopy} {
			clear(p)
		}
		for i := range s.Evict {
			clear(&s.Evict[i])
		}
		if s.Pending != nil && clear(&s.Pending.Candidate) {
			s.Pending = nil
		}
		dir := filepath.Join(m.Root, "configs")
		files, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		// Delete even orphaned snapshots left by a power cut before metadata
		// commit, so an older pointer cannot resurrect a revoked credential.
		for _, f := range files {
			if !keep[f.Name()] && !f.IsDir() {
				if e = os.Remove(filepath.Join(dir, f.Name())); e != nil {
					return e
				}
			}
		}
		if e = atomicfile.SyncDir(dir); e != nil {
			return e
		}
		if changed {
			return m.commit(s)
		}
		return nil
	})
}

// Closed is called only after the supervised process exits. A running single-
// slot candidate may still dynamically load a companion library from RAM.
func (m *Manager) Closed() error {
	return m.locked(func(s *state) error {
		old := s.RuntimeCopy
		if old == nil {
			return nil
		}
		s.RuntimeCopy = nil
		m.forget(old, s)
		return m.commit(s)
	})
}
func (m *Manager) Abort(token string, keepSelection ...bool) error {
	return m.locked(func(s *state) error {
		if s.Pending == nil || s.Pending.Token != token {
			return errors.New("stale kernel trial")
		}
		p := s.Pending.Candidate
		s.Pending = nil
		if s.Active != nil && (len(keepSelection) == 0 || !keepSelection[0]) {
			s.Selected = s.Active
		}
		if e := m.commit(s); e != nil {
			return e
		}
		m.forget(&p, s)
		return nil
	})
}

func validConfig(raw []byte) bool {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if dec.Decode(&doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	return dec.Decode(&doc) == io.EOF
}
