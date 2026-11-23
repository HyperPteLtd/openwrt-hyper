package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

func (m *Manager) Confirm(ctx context.Context, token string) (Launch, error) {
	var l Launch
	e := m.locked(func(s *state) error {
		if s.Pending == nil || s.Pending.Token != token {
			return errors.New("stale kernel trial")
		}
		p := s.Pending.Candidate
		v, e := m.stored(&p)
		if e != nil {
			return e
		}
		oldPrev, oldActive := s.Previous, s.Active
		s.Pending = nil
		s.Active = &p
		s.Selected = &p
		s.Previous = nil
		if p.Mode == "double" && oldActive != nil && !oldActive.Volatile {
			if oldActive.Slot == p.Slot {
				s.Previous = oldPrev
			} else {
				s.Previous = oldActive
			}
		}
		if p.Mode == "single" && p.Volatile {
			// Publish new recovery identity before reclaiming the only old copy.
			s.Repair = true
			s.Evict = nil
			for _, old := range []*pick{oldPrev, oldActive} {
				if old != nil && (old.Slot != p.Slot || old.Volatile != p.Volatile) {
					s.Evict = append(s.Evict, *old)
				}
			}
		}
		if e = m.commit(s); e != nil {
			return e
		}
		m.forget(oldPrev, s)
		m.forget(oldActive, s)
		if s.Repair {
			if e = m.finishSingle(ctx, s, v); e != nil {
				return e
			}
		}
		l = m.launch(*s.Active, v)
		return nil
	})
	return l, e
}
func (m *Manager) finishSingle(ctx context.Context, s *state, v *Verified) error {
	for i := range s.Evict {
		m.forget(&s.Evict[i], s)
	}
	old := *s.Active
	p := old
	p.Volatile = false
	if e := m.verifySlot(p, v); e != nil {
		_ = os.RemoveAll(m.slot(p))
		if e = m.verifySlot(old, v); e != nil {
			if e = m.restore(ctx, old, v); e != nil {
				return e
			}
		}
		if e = os.Mkdir(m.slot(p), 0755); e != nil {
			return e
		}
		for _, member := range v.Asset.Files {
			src := filepath.Join(m.slot(old), member.Path)
			dst := filepath.Join(m.slot(p), member.Path)
			if e = os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
				return e
			}
			if e = copyMember(src, dst, member); e != nil {
				return e
			}
			dir := filepath.Dir(dst)
			for dir != filepath.Dir(m.slot(p)) {
				if e = atomicfile.SyncDir(dir); e != nil {
					return e
				}
				dir = filepath.Dir(dir)
			}
		}
		if e = slotPermissions(m.slot(p), v.Asset); e != nil {
			return e
		}
		if e = atomicfile.SyncDir(filepath.Dir(m.slot(p))); e != nil {
			return e
		}
	}
	s.Repair = false
	s.Evict = nil
	s.RuntimeCopy = &old
	s.Active = &p
	s.Selected = &p
	if e := m.commit(s); e != nil {
		return e
	}
	m.forget(&old, s)
	return nil
}
func copyMember(src, dst string, member Member) error {
	in, e := openRegular(src)
	if e != nil {
		return e
	}
	defer in.Close()
	mode := os.FileMode(0644)
	if member.Kind == "core" {
		mode = 0755
	}
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	defer out.Close()
	if e = copyChecked(out, in, member.Size, member.SHA256); e != nil {
		return e
	}
	return out.Sync()
}
func (m *Manager) restore(ctx context.Context, p pick, v *Verified) error {
	if !p.Volatile {
		return errors.New("confirmed persistent core is damaged")
	}
	reservation, e := m.reservePayload()
	if e != nil {
		return e
	}
	defer reservation.Close()
	var sources []Source
	// A core kept in RAM comes back from the package that carries it.
	if src, ok := m.bundled(p.Tag); ok {
		sources = append(sources, src)
	}
	if p.Source.Cache != "" {
		sources = append(sources, Source{Bundle: filepath.Join(p.Source.Cache, p.Tag)})
	}
	if p.Source.URL != "" || p.Source.Mirror != "" {
		sources = append(sources, p.Source)
	}
	sources = append(sources, Source{})
	for _, src := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, e := m.Capacity(m.Root, m.Temp)
		if e != nil {
			return e
		}
		spool := v.Asset.Size
		if src.Bundle != "" {
			spool = 0
		}
		if e = CheckCapacity(c, p.Mode, spool, v.Asset.Expanded, v.Target.PeakRAM, 0); e != nil {
			continue
		}
		_ = os.RemoveAll(m.slot(p))
		if e = m.materialize(ctx, p, v, src); e == nil {
			return nil
		}
	}
	return fmt.Errorf("cannot restore verified core %s; stay direct and provide the original bundle", p.Tag)
}
func (m *Manager) Recover(ctx context.Context) (Launch, error) {
	var l Launch
	e := m.locked(func(s *state) error {
		if s.Pending != nil {
			trial := s.Pending.Candidate
			s.Pending = nil
			if s.Active != nil {
				s.Selected = s.Active
			}
			if e := m.commit(s); e != nil {
				return e
			}
			m.forget(&trial, s)
		}
		if s.Active == nil {
			return errors.New("no confirmed core; stay direct")
		}
		v, e := m.stored(s.Active)
		if e != nil {
			return e
		}
		if s.Repair {
			if e = m.finishSingle(ctx, s, v); e != nil {
				return e
			}
		}
		if e = m.verifySlot(*s.Active, v); e != nil {
			if e = m.restore(ctx, *s.Active, v); e != nil {
				return e
			}
		}
		l = m.launch(*s.Active, v)
		return nil
	})
	return l, e
}
