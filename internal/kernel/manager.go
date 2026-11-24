package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type Options struct {
	Root, Temp, Target, Board, Key string
	Capacity                       func(string, string) (Capacity, error)
	Inspect                        func(context.Context, Launch, string) error
}
type Manager struct{ Options }
type pick struct {
	Tag         string   `json:"tag"`
	Mode        string   `json:"mode"`
	Slot        string   `json:"slot"`
	Volatile    bool     `json:"volatile"`
	Config      string   `json:"config,omitempty"`
	Source      Source   `json:"restore_source"`
	Authorities []string `json:"authorization_sources,omitempty"`
}
type trial struct {
	Token     string `json:"token"`
	Candidate pick   `json:"candidate"`
}
type state struct {
	Schema                     int    `json:"schema_version"`
	Revision                   uint64 `json:"revision"`
	Active, Previous, Selected *pick
	Staged                     *pick `json:"staged,omitempty"`
	Pending                    *trial
	Repair                     bool
	Evict                      []pick `json:"evict,omitempty"`
	RuntimeCopy                *pick  `json:"running_temporary_slot,omitempty"`
}
type Launch struct {
	Tag    string `json:"tag"`
	Token  string `json:"token"`
	Path   string `json:"path"`
	Config string `json:"config"`
}
type Status struct {
	Schema     int    `json:"schema_version"`
	Revision   uint64 `json:"revision"`
	Staged     string `json:"staged,omitempty"`
	Mode       string `json:"mode"`
	Target     string `json:"target"`
	Selected   string `json:"selected"`
	Active     string `json:"active"`
	Previous   string `json:"previous"`
	Pending    bool   `json:"pending"`
	TrustReady bool   `json:"trust_ready"`
	Repair     bool   `json:"needs_repair"`
}

func New(o Options) (*Manager, error) {
	if _, ok := platforms[o.Target]; !ok {
		return nil, errors.New("invalid kernel target")
	}
	if !filepath.IsAbs(o.Root) || !filepath.IsAbs(o.Temp) || o.Root == o.Temp {
		return nil, errors.New("invalid kernel storage directories")
	}
	m := &Manager{o}
	if m.Capacity == nil {
		m.Capacity = systemCapacity
	}
	if m.Inspect == nil {
		return nil, errors.New("restricted core inspector required")
	}
	return m, nil
}
func (m *Manager) setup() error {
	// Only root (or the current owner in an isolated test) may replace one of
	// these path components. In particular /tmp/hyper cannot be supplied
	// by an unprivileged process before the first startup.
	for _, base := range []string{m.Root, m.Temp} {
		for q := base; q != filepath.Dir(q); q = filepath.Dir(q) {
			i, e := os.Lstat(q)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			st, ok := i.Sys().(*syscall.Stat_t)
			if !ok {
				return errors.New("cannot verify kernel storage ownership")
			}
			if int(st.Uid) != 0 && int(st.Uid) != os.Geteuid() {
				return errors.New("kernel storage is owned by another user")
			}
			if i.Mode()&os.ModeSymlink != 0 {
				// System-owned ancestor aliases (/var on OpenWrt, /tmp on macOS)
				// are safe; the managed base itself may not be a symlink.
				if int(st.Uid) != 0 || q == base {
					return errors.New("untrusted kernel storage symlink")
				}
				continue
			}
			if !i.IsDir() || i.Mode().Perm()&0022 != 0 && i.Mode()&os.ModeSticky == 0 {
				return errors.New("kernel storage parent is writable by another user")
			}
		}
	}

	for _, base := range []string{m.Root, m.Temp} {
		// These two shared application parents are the only directories
		// outside the managed roots whose traversal mode we repair.
		if base == "/etc/hyper/mihomo-kernel" || base == "/tmp/hyper/mihomo-kernel" {
			if err := makeManagedDir(filepath.Dir(base), 0711); err != nil {
				return err
			}
		}
		for _, dir := range []string{base, filepath.Join(base, "slots")} {
			if err := makeManagedDir(dir, 0711); err != nil {
				return err
			}
		}
	}
	if err := makeManagedDir(filepath.Join(m.Root, "meta"), 0700); err != nil {
		return err
	}
	return makeManagedDir(filepath.Join(m.Root, "configs"), 0711)
}

var errSchema = errors.New("unknown kernel state schema; keep LAN direct and preserve recovery files")

func parseState(b []byte) (state, error) {
	var s state
	if json.Unmarshal(b, &s) != nil {
		return s, errors.New("kernel state unreadable or unsupported schema; retain state and stay direct")
	}
	if s.Schema != 1 {
		return s, errSchema
	}
	if s.RuntimeCopy != nil && (!validPick(s.RuntimeCopy) || !s.RuntimeCopy.Volatile) {
		return s, errors.New("invalid running slot")
	}
	if s.Repair && s.Active == nil {
		return s, errors.New("orphan repair record")
	}
	for i := range s.Evict {
		if !validPick(&s.Evict[i]) {
			return s, errors.New("invalid eviction record")
		}
	}
	for _, p := range []*pick{s.Selected, s.Active, s.Previous, s.Staged} {
		if p != nil && !validPick(p) {
			return s, errors.New("invalid persisted kernel state")
		}
	}
	if s.Pending != nil && (!validPick(&s.Pending.Candidate) || !validID(s.Pending.Token)) {
		return s, errors.New("invalid persisted trial")
	}
	return s, nil
}

func (m *Manager) read() (state, error) {
	b, e := readSmall(filepath.Join(m.Root, "state.json"), maxDocument)
	if e == nil {
		var s state
		s, e = parseState(b)
		if e == nil || errors.Is(e, errSchema) {
			return s, e
		}
	}
	backup, backErr := readSmall(filepath.Join(m.Root, "state.prev.json"), maxDocument)
	if backErr == nil {
		return parseState(backup)
	}
	if os.IsNotExist(e) && os.IsNotExist(backErr) {
		return state{Schema: 1}, nil
	}
	return state{}, errors.New("no readable committed kernel state")
}
func validID(s string) bool { return len(s) == 32 && validHash(s+s) }
func validPick(p *pick) bool {
	return len(p.Tag) < 101 && TagPattern.MatchString(p.Tag) && validID(p.Slot) && (p.Mode == "single" || p.Mode == "tmp" || p.Mode == "double") && (p.Config == "" || validID(p.Config))
}
func (m *Manager) commit(s *state) error {
	if old, e := readSmall(filepath.Join(m.Root, "state.json"), maxDocument); e == nil {
		if _, e = parseState(old); e == nil {
			if e = writeAtomic(filepath.Join(m.Root, "state.prev.json"), old, 0600); e != nil {
				return e
			}
		}
	}
	s.Revision++
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	return writeAtomic(filepath.Join(m.Root, "state.json"), b, 0600)
}
func (m *Manager) locked(fn func(*state) error) error {
	if e := m.setup(); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(m.Root, ".lock"), os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	s, e := m.read()
	if e != nil {
		return e
	}
	return fn(&s)
}
func (m *Manager) Status() (Status, error) {
	s, e := m.read()
	if e != nil {
		return Status{}, e
	}
	v := Status{Schema: 1, Revision: s.Revision, Target: m.Target, Pending: s.Pending != nil, TrustReady: true, Repair: s.Repair}
	if s.Staged != nil {
		v.Staged = s.Staged.Tag
	}
	if s.Selected != nil {
		v.Selected = s.Selected.Tag
		v.Mode = s.Selected.Mode
	}
	if s.Active != nil {
		v.Active = s.Active.Tag
	}
	if s.Previous != nil {
		v.Previous = s.Previous.Tag
	}
	return v, nil
}
func (m *Manager) stored(p *pick) (*Verified, error) {
	if p == nil {
		return nil, errors.New("no selected core")
	}
	_, v, e := m.storedMetadata(p.Tag)
	return v, e
}
func (m *Manager) Capabilities(tag ...string) (Capabilities, error) {
	if len(tag) > 0 && tag[0] != "" {
		if !TagPattern.MatchString(tag[0]) || len(tag[0]) > 100 {
			return Capabilities{}, errors.New("invalid core tag")
		}
		v, e := m.stored(&pick{Tag: tag[0]})
		if e != nil {
			return Capabilities{}, e
		}
		return v.Capabilities, nil
	}
	s, e := m.read()
	if e != nil {
		return Capabilities{}, e
	}
	v, e := m.stored(s.Selected)
	if e != nil {
		return Capabilities{}, e
	}
	return v.Capabilities, nil
}
func (m *Manager) slot(p pick) string {
	r := m.Root
	if p.Volatile {
		r = m.Temp
	}
	return filepath.Join(r, "slots", p.Slot)
}
func (m *Manager) launch(p pick, v *Verified) Launch {
	l := Launch{Tag: p.Tag}
	for _, f := range v.Asset.Files {
		if f.Kind == "core" {
			l.Path = filepath.Join(m.slot(p), f.Path)
			break
		}
	}
	if p.Config != "" {
		l.Config = filepath.Join(m.Root, "configs", p.Config+".yaml")
	}
	return l
}
func (m *Manager) verifySlot(p pick, v *Verified) error {
	for _, f := range v.Asset.Files {
		in, e := openRegular(filepath.Join(m.slot(p), f.Path))
		if e != nil {
			return e
		}
		e = copyChecked(io.Discard, in, f.Size, f.SHA256)
		in.Close()
		if e != nil {
			return e
		}
	}
	return slotPermissions(m.slot(p), v.Asset)
}
func (m *Manager) Active() (Launch, error) {
	s, e := m.read()
	if e != nil {
		return Launch{}, e
	}
	if s.Active == nil || s.Active.Config == "" || s.Repair {
		return Launch{}, errors.New("no confirmed runnable core")
	}
	v, e := m.stored(s.Active)
	if e != nil {
		return Launch{}, e
	}
	l := m.launch(*s.Active, v)
	if _, e = os.Stat(l.Config); e != nil {
		return Launch{}, e
	}
	if _, e = os.Stat(l.Path); e != nil {
		return Launch{}, e
	}
	return l, nil
}
