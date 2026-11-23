package kernel

import (
	"context"
	"debug/buildinfo"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

func (m *Manager) saveMeta(tag string, mat material) error {
	dir := filepath.Join(m.Root, "meta", tag)
	old, _, e := m.storedMetadata(tag)
	if e == nil {
		if !old.same(mat) {
			return errors.New("previously pinned tag has different contents")
		}
		return nil
	}
	if !os.IsNotExist(e) {
		return e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(dir), ".release-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	for n, b := range mat.parts() {
		if e = writeAtomic(filepath.Join(tmp, n), b, 0600); e != nil {
			return e
		}
	}
	if e = os.Rename(tmp, dir); e != nil {
		return e
	}
	return atomicfile.SyncDir(filepath.Dir(dir))
}
func (m *Manager) materialize(ctx context.Context, p pick, v *Verified, src Source) error {
	a := v.Asset
	var archive string
	if src.Bundle != "" {
		archive = filepath.Join(src.Bundle, a.Name)
		// unpack verifies the compressed digest, each member and the ELF
		// before publishing any file or executing a version check.
	} else {
		in, e := src.open(ctx, p.Tag, a.Name, true)
		if e != nil {
			return e
		}
		defer in.Close()
		f, e := os.CreateTemp(m.Temp, ".archive-")
		if e != nil {
			return e
		}
		archive = f.Name()
		defer os.Remove(archive)
		e = copyChecked(f, in, a.Size, a.SHA256)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	dir := m.slot(p)
	tmp, e := os.MkdirTemp(filepath.Dir(dir), ".unpack-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = unpack(archive, tmp, m.Target, m.Board, a); e != nil {
		return e
	}
	if e = slotPermissions(tmp, a); e != nil {
		return e
	}
	if e = os.Rename(tmp, dir); e != nil {
		return e
	}
	if e = atomicfile.SyncDir(filepath.Dir(dir)); e != nil {
		return e
	}
	if e = m.Inspect(ctx, m.launch(p, v), ""); e != nil {
		return e
	}

	if src.Cache != "" && src.Bundle != src.Cache {
		if e = cacheArchive(src.Cache, p.Tag, a, archive); e != nil {
			return e
		}
	}
	return nil
}
func cacheArchive(root, tag string, a Asset, input string) error {
	dir := filepath.Join(root, tag)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	file := filepath.Join(dir, a.Name)
	if f, e := openRegular(file); e == nil {
		e = copyChecked(io.Discard, f, a.Size, a.SHA256)
		f.Close()
		if e == nil {
			return nil
		}
	}
	in, e := openRegular(input)
	if e != nil {
		return e
	}
	defer in.Close()
	return atomicfile.Write(file, 0600, ".asset-", func(out *os.File) error {
		return copyChecked(out, in, a.Size, a.SHA256)
	})
}

// Serialize payload writers across processes without holding the intent lock
// through a network download. A second writer may not spend the same free bytes.
var errPayloadBusy = errors.New("kernel payload installation already in progress")

func (m *Manager) reservePayload() (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(m.Root, ".payload.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if errors.Is(e, syscall.EWOULDBLOCK) {
			return nil, errPayloadBusy
		}
		return nil, e
	}
	return f, nil
}
func (m *Manager) reclaim(s *state) error {
	reachable := map[string]bool{}
	add := func(p *pick) {
		if p != nil {
			reachable[m.slot(*p)] = true
			if p.Config != "" {
				reachable[filepath.Join(m.Root, "configs", p.Config+".yaml")] = true
			}
		}
	}
	for _, p := range []*pick{s.Active, s.Previous, s.Selected, s.Staged, s.RuntimeCopy} {
		add(p)
	}
	for i := range s.Evict {
		add(&s.Evict[i])
	}
	if s.Pending != nil {
		add(&s.Pending.Candidate)
	}
	// The state lock protects trial validation and slot restoration, while
	// .payload.lock protects as-yet unpublished downloads on either volume.
	for _, dir := range []string{filepath.Join(m.Root, "slots"), filepath.Join(m.Temp, "slots"), filepath.Join(m.Root, "configs"), filepath.Join(m.Root, "meta"), m.Temp} {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			n := entry.Name()
			path := filepath.Join(dir, n)
			orphan := false
			switch dir {
			case filepath.Join(m.Root, "slots"), filepath.Join(m.Temp, "slots"):
				orphan = strings.HasPrefix(n, ".unpack-") || validID(n)
			case filepath.Join(m.Root, "configs"):
				orphan = strings.HasPrefix(n, ".preflight-") || strings.HasSuffix(n, ".yaml") && validID(strings.TrimSuffix(n, ".yaml"))
			case filepath.Join(m.Root, "meta"):
				orphan = strings.HasPrefix(n, ".release-")
			case m.Temp:
				orphan = strings.HasPrefix(n, ".archive-")
			}
			if orphan && !reachable[path] {
				if e = os.RemoveAll(path); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func (m *Manager) Install(ctx context.Context, tag, mode string, src Source, stageOnly ...bool) error {
	if mode != "double" && mode != "single" && mode != "tmp" {
		return errors.New("storage mode must be double, single or tmp")
	}
	if e := src.validate(); e != nil {
		return e
	}
	if e := m.setup(); e != nil {
		return e
	}
	if !TagPattern.MatchString(tag) || len(tag) > 100 {
		return errors.New("invalid release tag")
	}
	reservation, e := m.reservePayload()
	if e != nil {
		return e
	}
	defer reservation.Close()
	return m.installReserved(ctx, tag, mode, src, len(stageOnly) > 0 && stageOnly[0], nil)
}

func (m *Manager) installReserved(ctx context.Context, tag, mode string, src Source, stageOnly bool, releasePin *pin) error {
	if releasePin == nil {
		if e := m.locked(m.reclaim); e != nil {
			return e
		}
	}
	start, e := m.read()
	if e != nil {
		return e
	}
	if releasePin != nil && start.Revision != 0 {
		return errors.New("install superseded by newer intent")
	}
	if start.Pending != nil || start.Repair {
		return errors.New("kernel transaction in progress")
	}
	candidate, e := m.prepare(ctx, tag, src, releasePin)
	defer candidate.close()
	if e != nil {
		return e
	}
	mat, v := candidate.material, candidate.verified
	// Reject a retag before running a candidate's version command.
	if old, _, pe := m.storedMetadata(tag); pe == nil && !old.same(mat) {
		return errors.New("existing tag has different release metadata")
	} else if pe != nil && !os.IsNotExist(pe) {
		return pe
	}
	c, e := m.Capacity(m.Root, m.Temp)
	if e != nil {
		return e
	}
	spool := v.Asset.Size
	if candidate.source.Bundle != "" {
		spool = 0
	}
	if src.Cache != "" {
		if e = os.MkdirAll(src.Cache, 0700); e != nil {
			return e
		}
		cached, e := volume(src.Cache)
		if e != nil {
			return e
		}
		if cached.InMemory {
			return errors.New("offline cache must reside on persistent media")
		}
		if cached.Free < v.Asset.Size+16*MiB {
			return errors.New("insufficient space on cache volume")
		}
		if cached.Device == c.Persistent.Device {
			c.Persistent.Free -= v.Asset.Size
		}
		if cached.Device == c.Temporary.Device {
			c.Temporary.Free -= v.Asset.Size
		}
	}
	var replace int64
	if mode == "single" && start.Active != nil && !start.Active.Volatile {
		old, e := m.stored(start.Active)
		if e != nil {
			return e
		}
		replace = old.Asset.Expanded
	}
	if e = CheckCapacity(c, mode, spool, v.Asset.Expanded, v.Target.PeakRAM, replace); e != nil {
		// A second update can reuse the inactive previous slot. Publish its
		// removal before unlinking; keep the active and selected slots intact.
		if mode != "double" || start.Active == nil || start.Previous == nil || start.Previous.Volatile || start.Active.Slot == start.Previous.Slot || start.Selected != nil && start.Selected.Slot == start.Previous.Slot {
			return e
		}
		prev, pe := m.stored(start.Previous)
		if pe != nil {
			return e
		}
		possible := c
		possible.Persistent.Free += prev.Asset.Expanded
		if CheckCapacity(possible, mode, spool, v.Asset.Expanded, v.Target.PeakRAM, replace) != nil {
			return e
		}
		if e = m.locked(func(s *state) error {
			if s.Revision != start.Revision {
				return errors.New("kernel selection changed")
			}
			old := s.Previous
			s.Previous = nil
			if e := m.commit(s); e != nil {
				return e
			}
			m.forget(old, s)
			start = *s
			return nil
		}); e != nil {
			return e
		}
		actual, e := m.Capacity(m.Root, m.Temp)
		if e != nil {
			return e
		}
		if src.Cache != "" {
			cv, e := volume(src.Cache)
			if e != nil {
				return e
			}
			if actual.Persistent.Device == cv.Device {
				actual.Persistent.Free -= v.Asset.Size
			}
			if actual.Temporary.Device == cv.Device {
				actual.Temporary.Free -= v.Asset.Size
			}
		}
		if e = CheckCapacity(actual, mode, spool, v.Asset.Expanded, v.Target.PeakRAM, replace); e != nil {
			return e
		}
	}
	id, e := uidToken()
	if e != nil {
		return e
	}
	p := pick{Tag: tag, Slot: id, Mode: mode, Volatile: mode != "double", Source: src}
	p.Source.Bundle = ""
	if e = m.materialize(ctx, p, v, candidate.source); e != nil {
		os.RemoveAll(m.slot(p))
		return e
	}
	if len(mat.receipt) > 0 && v.Target.ProtocolSupport == nil {
		profile, err := m.probeFeatures(ctx, m.launch(p, v), v.Target)
		if err != nil {
			os.RemoveAll(m.slot(p))
			return err
		}
		mat, v, err = newReceipt(tag, m.Target, v.Asset, profile)
		if err != nil {
			os.RemoveAll(m.slot(p))
			return err
		}
	}
	committed := false
	defer func() {
		if !committed {
			// A directory fsync can fail after rename; retain reachable data.
			current, e := m.read()
			if e == nil {
				m.forget(&p, &current)
			}
		}
	}()
	return m.locked(func(s *state) error {
		if s.Revision != start.Revision || ctx.Err() != nil {
			return errors.New("install superseded by newer intent")
		}
		if e = m.saveMeta(tag, mat); e != nil {
			return e
		}
		old := s.Selected
		if stageOnly {
			old = s.Staged
			s.Staged = &p
		} else {
			s.Selected = &p
		}
		if e = m.commit(s); e != nil {
			return e
		}
		committed = true
		m.forget(old, s)
		return nil
	})
}
func (m *Manager) forget(p *pick, s *state) {
	if p == nil {
		return
	}
	sameSlot := false
	sameConfig := false
	picks := []*pick{s.Active, s.Previous, s.Selected, s.Staged, s.RuntimeCopy}
	if s.Pending != nil {
		picks = append(picks, &s.Pending.Candidate)
	}
	for _, keep := range picks {
		if keep != nil {
			sameSlot = sameSlot || (keep.Slot == p.Slot && keep.Volatile == p.Volatile)
			sameConfig = sameConfig || keep.Config == p.Config
		}
	}
	if !sameSlot {
		_ = os.RemoveAll(m.slot(*p))
	}
	if p.Config != "" && !sameConfig {
		_ = os.Remove(filepath.Join(m.Root, "configs", p.Config+".yaml"))
	}
}

// Bootstrap reserves the same payload space as manual installations. Inspect
// persisted picks and unreferenced payloads before reclaim can remove anything.
func (m *Manager) Bootstrap(ctx context.Context) (map[string]string, error) {
	if err := m.setup(); err != nil {
		return nil, err
	}
	lock, err := m.reservePayload()
	if errors.Is(err, errPayloadBusy) {
		return map[string]string{"skipped": "installation_in_progress"}, nil
	}
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	s, err := m.read()
	if err != nil {
		return nil, err
	}
	if s.Revision != 0 || s.Selected != nil || s.Active != nil || s.Previous != nil || s.Staged != nil || s.RuntimeCopy != nil || s.Pending != nil || s.Repair || len(s.Evict) > 0 {
		return map[string]string{"skipped": "existing_kernel"}, nil
	}
	for _, dir := range []string{filepath.Join(m.Root, "meta"), filepath.Join(m.Root, "slots"), filepath.Join(m.Temp, "slots")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return map[string]string{"skipped": "existing_kernel"}, nil
		}
	}
	// The package's own core first: no download, no network.
	if tag, p, ok, err := ReadBundle(BundleDir, m.Target); err != nil {
		return nil, err
	} else if ok {
		if err = m.installBundle(ctx, tag, "", BundleDir, p); err != nil {
			return nil, err
		}
		return map[string]string{"staged": tag}, nil
	}
	tag, verified, err := latestStable(ctx, m.Target)
	if err != nil {
		return nil, err
	}
	if err = m.installReserved(ctx, tag, "double", Source{}, true, &verified); err != nil {
		return nil, err
	}
	return map[string]string{"staged": tag}, nil
}

// Probe each capability using the actual candidate under the restricted core identity.
func (m *Manager) probeFeatures(ctx context.Context, l Launch, tc TargetCapabilities) (TargetCapabilities, error) {
	token, e := uidToken()
	if e != nil {
		return tc, e
	}
	path := filepath.Join(m.Root, "configs", ".capability-"+token)
	defer os.Remove(path)
	check := func(settings string) (bool, error) {
		cfg := []byte("external-controller: 127.0.0.1:19761\nsecret: local-qualification-only\n" + settings + "\nrules: ['MATCH,DIRECT']\n")
		if e := writeAtomic(path, cfg, 0600); e != nil {
			return false, e
		}
		e := m.Inspect(ctx, l, path)
		var rejected interface{ RejectedConfiguration() bool }
		if errors.As(e, &rejected) && rejected.RejectedConfiguration() {
			return false, nil
		}
		return e == nil, e
	}
	for _, settings := range []string{"tun: {enable: true, stack: system, auto-route: false}", "tproxy-port: 19762"} {
		ok, err := check(settings)
		if err != nil {
			return tc, err
		}
		if !ok {
			return tc, errors.New("release lacks required Mihomo runtime capabilities")
		}
	}
	tc.ProtocolSupport = map[string]bool{}
	for _, test := range []struct{ name, fields string }{
		{"nowhere", "type: nowhere, network: tcp, password: '0102030405060708090a0b0c0d0e0f10'"},
		{"snell-v6", "type: snell, version: 6, psk: example-test-psk, mode: default"},
		{"anytls-reality", "type: anytls, password: example, client-fingerprint: chrome, sni: example.invalid, reality-opts: {public-key: 'AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8', short-id: '0123456789abcdef'}"},
	} {
		config := "proxies: [{name: test, server: example.invalid, port: 443, " + test.fields + "}]"
		ok, err := check(config)
		if err != nil {
			return tc, err
		}
		if test.name == "anytls-reality" && ok {
			accepted, err := check(strings.Replace(config, "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", "invalid", 1))
			if err != nil {
				return tc, err
			}
			ok = !accepted
		}
		tc.ProtocolSupport[test.name] = ok
	}
	// Linux with_purego can parse Naive even when its external Cronet library
	// is missing. Require the native, statically linked build as well as the
	// candidate's configuration check; archive metadata alone is insufficient.
	tc.Naive = false
	if nativeNaive(l.Path) {
		ok, err := check("proxies: [{name: test, type: naive, server: example.invalid, port: 443, username: test, password: test}]")
		if err != nil {
			return tc, err
		}
		tc.Naive = ok
	}
	return tc, nil
}

func nativeNaive(path string) bool {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return false
	}
	cgo := false
	var tags []string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "CGO_ENABLED":
			cgo = setting.Value == "1"
		case "-tags":
			tags = strings.FieldsFunc(setting.Value, func(r rune) bool { return r == ',' || r == ' ' })
		}
	}
	return cgo && slices.Contains(tags, "with_naive") && slices.Contains(tags, "with_musl") && !slices.Contains(tags, "with_purego")
}
