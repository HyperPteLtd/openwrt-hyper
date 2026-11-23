package subscription

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/openweir/subconv/rules"
)

// The router's copy of the rules branch (TemplateDir) is the last version a refresh got whole:
// its manifest, floor, template and rule sets, under their branch paths, in a generation
// directory that the link @current names. A refresh reads the generation that was current when
// it began, and keeps what it gets in memory (templateStage). Only once every rule set of the
// template arrived does it write the next generation and move the link to it, in one rename: a
// refresh that fails, runs out of time or is killed leaves the copy as it was. Names that start
// with '@', which no branch path has, are the copy's own.
const (
	// Held, exclusively, to pin a generation, promote one and sweep.
	templateLock = "@lock"
	// The link to the current generation, and the link that replaces it.
	templateCurrent     = "@current"
	templateCurrentNext = "@current.next"
	// A generation: complete once the link names it.
	templateGeneration = "@gen-"
	// The temporary files of the copy before generations, which a helper killed mid-write left.
	// A helper of that version may still be writing one during an upgrade: only old ones go.
	templateTemp      = ".new-"
	templateTempStale = 10 * time.Minute
	// How long a refresh waits, at most, for another's promotion or sweep to let go of the lock,
	// and how often it looks. Past it, it reads the copy without the lock, and does not promote.
	templateLockWait = 10 * time.Second
	templateLockPoll = 20 * time.Millisecond
)

// templateStage is the rules client's cache in one refresh: the generation current when the
// refresh first used the copy (its base), held so that no sweep removes it meanwhile, under what
// the refresh got that differs from it, in memory.
type templateStage struct {
	root string
	// ctx is the refresh's, which no wait for the lock outlasts (nil: none).
	ctx context.Context

	mu     sync.Mutex
	pinned bool
	name   string   // of the base generation; "" when the base is root, the copy before generations
	base   *os.File // the base directory, with a shared lock; nil without one
	staged map[string][]byte
}

func (s *templateStage) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	value, ok := s.staged[key]
	base := s.pin()
	s.mu.Unlock()
	if ok {
		return value, true
	}
	return readGeneration(base, key)
}

func (s *templateStage) Put(key string, value []byte) {
	if !rules.ValidPath(key) || len(value) > MaxBody {
		return
	}
	s.mu.Lock()
	base := s.pin()
	s.mu.Unlock()
	old, ok := readGeneration(base, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok && bytes.Equal(old, value) {
		delete(s.staged, key)
		return
	}
	if s.staged == nil {
		s.staged = map[string][]byte{}
	}
	s.staged[key] = bytes.Clone(value)
}

// pin sweeps the copy and holds its current generation, on first use. It returns the base
// directory, or "" without one. A copy the refresh cannot write (lockTemplate) is not swept,
// and one it cannot lock at all is read as the link names it, without the lock: holding the
// generation keeps sweeps from removing it all the same.
func (s *templateStage) pin() string {
	if !s.pinned {
		s.pinned = true
		lock, writable, err := lockTemplate(s.ctx, s.root)
		var current string
		if err == nil && writable {
			current, _ = sweepTemplate(s.root)
		} else {
			current, _ = currentGeneration(s.root)
		}
		s.name, s.base = holdBase(s.root, current)
		if err == nil {
			unlock(lock)
		}
	}
	if s.base == nil {
		return ""
	}
	return s.base.Name()
}

// discard forgets what the refresh got: the client reads the base alone.
func (s *templateStage) discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.staged = nil
}

// close lets go of the base.
func (s *templateStage) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.staged = nil
	if s.base != nil {
		s.base.Close()
		s.base = nil
	}
}

// promote makes what the refresh got the router's copy: a new generation of the manifest, the
// floor and the files the manifest lists takes the place of the current one. Files the refresh
// did not change are links to the base's; the rest of the base, which the manifest no longer
// lists, is left behind with it. When another refresh promoted the same version or a newer one
// since this one began, with the same floor or a higher one, that stays; otherwise the copy
// keeps the higher floor of the two. The stage is spent.
//
// It returns why the copy could not be written: the copy is then as it was, or, when only the
// sync after the link moved failed, it names the new generation, which a power cut may undo.
func (s *templateStage) promote() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	staged := s.staged
	s.staged = nil
	if len(staged) == 0 {
		return nil
	}
	base := s.pin()
	get := func(key string) ([]byte, bool) {
		if value, ok := staged[key]; ok {
			return value, true
		}
		return readGeneration(base, key)
	}
	raw, _ := get(rules.ManifestPath)
	m, err := rules.ParseManifest(raw)
	if err != nil {
		// Nothing whole to keep.
		return nil
	}
	lock, _, err := lockTemplate(s.ctx, s.root)
	if err != nil {
		return err
	}
	defer unlock(lock)
	if current, _ := currentGeneration(s.root); current != s.name {
		other := filepath.Join(s.root, current)
		theirs, _ := readGeneration(other, rules.FloorKey)
		ours, _ := get(rules.FloorKey)
		if b, ok := readGeneration(other, rules.ManifestPath); ok {
			if om, err := rules.ParseManifest(b); err == nil && om.Version >= m.Version && floorVersion(theirs) >= floorVersion(ours) {
				return nil
			}
		}
		if floorVersion(theirs) > floorVersion(ours) {
			staged[rules.FloorKey] = theirs
		}
	}
	dir, err := os.MkdirTemp(s.root, templateGeneration)
	if err != nil {
		return err
	}
	published := false
	if err = fillGeneration(dir, base, staged, m); err == nil {
		published, err = switchGeneration(s.root, filepath.Base(dir))
	}
	if !published {
		os.RemoveAll(dir)
		return err
	}
	// The refresh reads no more: its base can go now.
	if s.base != nil {
		s.base.Close()
		s.base = nil
	}
	// The link names the new generation, but the sync after the rename failed: a power cut may
	// still bring back the one it replaced, which stays until a later sweep.
	if err != nil {
		return err
	}
	sweepTemplate(s.root)
	return nil
}

// fillGeneration writes the manifest, the floor and the files the manifest lists into dir: what
// the refresh staged, and links to the rest in the base. A file neither has is left out.
func fillGeneration(dir, base string, staged map[string][]byte, m *rules.Manifest) error {
	keys := []string{rules.ManifestPath, rules.FloorKey}
	for _, f := range m.Files {
		keys = append(keys, f.Path)
	}
	dirs := map[string]bool{dir: true}
	done := map[string]bool{}
	for _, key := range keys {
		if done[key] || !rules.ValidPath(key) {
			continue
		}
		done[key] = true
		path := filepath.Join(dir, filepath.FromSlash(key))
		for d := filepath.Dir(path); !dirs[d]; d = filepath.Dir(d) {
			dirs[d] = true
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if value, ok := staged[key]; ok {
			if err := writeSynced(path, value); err != nil {
				return err
			}
			continue
		}
		if base == "" {
			continue
		}
		from := filepath.Join(base, filepath.FromSlash(key))
		if err := os.Link(from, path); err == nil || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		// A file system without links: copy.
		if value, err := os.ReadFile(from); err != nil || writeSynced(path, value) != nil {
			return errors.New("template_cache_write_failed")
		}
	}
	for d := range dirs {
		if err := templateSyncDir(d); err != nil {
			return err
		}
	}
	return nil
}

// switchGeneration points the link at a generation, in one rename. published tells that the
// rename was done: the link names the generation from then on, even when the sync of the
// directory after it fails (err), and the generation must stay.
func switchGeneration(root, name string) (published bool, err error) {
	next := filepath.Join(root, templateCurrentNext)
	os.Remove(next)
	err = os.Symlink(name, next)
	if err == nil {
		err = templateSyncDir(root)
	}
	if err == nil {
		err = os.Rename(next, filepath.Join(root, templateCurrent))
	}
	if err != nil {
		os.Remove(next)
		return false, err
	}
	return true, templateSyncDir(root)
}

// templateSyncDir syncs a directory of the copy; tests make it fail.
var templateSyncDir = atomicfile.SyncDir

// writeSynced is not atomicfile: it fills a new generation, which switchGeneration publishes whole.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readGeneration(dir, key string) ([]byte, bool) {
	if dir == "" || !rules.ValidPath(key) {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(key)))
	return b, err == nil
}

func floorVersion(b []byte) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v
}

// keepFloor raises the floor of the generation in dir to the one in from, when that is higher,
// and reports whether dir's floor is now the higher of the two. A floor only rises, so a refresh
// reading the generation meanwhile may read either; the file, which may be a link to another
// generation's, is replaced rather than written over.
func keepFloor(dir, from string) bool {
	theirs, ok := readGeneration(from, rules.FloorKey)
	if !ok {
		return true
	}
	if ours, _ := readGeneration(dir, rules.FloorKey); floorVersion(theirs) <= floorVersion(ours) {
		return true
	}
	return atomicfile.WriteFile(filepath.Join(dir, rules.FloorKey), theirs, 0600, templateTemp+"*") == nil
}

// lockTemplate takes the copy's lock, exclusively, waiting for it at most templateLockWait and
// not past ctx. On a copy it cannot write, such as a file system remounted read-only, the lock
// file is opened for reading, which flock needs no more: writable is false then. Without the
// lock file, as on a full file system before one was ever created, there is no lock.
func lockTemplate(ctx context.Context, root string) (lock *os.File, writable bool, err error) {
	path := filepath.Join(root, templateLock)
	if err = os.MkdirAll(root, 0700); err == nil {
		lock, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	}
	if writable = err == nil; !writable {
		if lock, err = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err != nil {
			return nil, false, err
		}
	}
	if err = flockWithin(ctx, lock); err != nil {
		lock.Close()
		return nil, false, err
	}
	return lock, writable, nil
}

// flockWithin takes an exclusive flock of f, which another process may hold: it tries again
// every templateLockPoll, for templateLockWait at most and not past ctx.
func flockWithin(ctx context.Context, f *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, templateLockWait)
	defer cancel()
	poll := time.NewTicker(templateLockPoll)
	defer poll.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
		}
	}
}

// currentGeneration is the generation the link names, or "" for root: the copy before
// generations, or none. ok tells that the copy is as the link says, which a sweep relies on:
// there is no link, or it names a generation's directory. A link that cannot be read, that
// names anything else, or whose generation is gone, is not.
func currentGeneration(root string) (name string, ok bool) {
	name, err := os.Readlink(filepath.Join(root, templateCurrent))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", true
	case err != nil || !strings.HasPrefix(name, templateGeneration) || strings.ContainsRune(name, filepath.Separator):
		return "", false
	}
	info, err := os.Stat(filepath.Join(root, name))
	return name, err == nil && info.IsDir()
}

// holdBase holds the generation the link names (name) or, without one or when it cannot be
// held, the copy before generations (root, as ""), whatever of it is left.
func holdBase(root, name string) (string, *os.File) {
	if name != "" {
		if f := holdGeneration(filepath.Join(root, name)); f != nil {
			return name, f
		}
	}
	return "", holdGeneration(root)
}

// holdGeneration opens a generation's directory with a shared lock, which keeps sweeps from
// removing it while a refresh reads it. The lock goes with the process, however it ends.
func holdGeneration(dir string) *os.File {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}

// ifUnheld runs remove when no refresh holds the generation in dir.
func ifUnheld(dir string, remove func()) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		remove()
	}
}

// sweepTemplate, under the lock, removes what no refresh uses: generations other than the
// current one that no refresh holds (one a promotion did not finish, or that a newer one
// replaced), a link a promotion did not finish, and once a generation is current, the copy
// before generations; while that copy is still current, its old temporary files. It returns
// the current generation (currentGeneration). A link it cannot trust leaves it nothing to
// tell the current generation from the rest by: it removes nothing then.
func sweepTemplate(root string) (string, bool) {
	current, ok := currentGeneration(root)
	if !ok {
		return current, false
	}
	entries, _ := os.ReadDir(root)
	before := false
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(root, name)
		switch {
		case name == templateLock || name == templateCurrent || name == current:
		case strings.HasPrefix(name, templateGeneration) && e.IsDir():
			ifUnheld(path, func() { os.RemoveAll(path) })
		case strings.HasPrefix(name, "@"):
			os.RemoveAll(path)
		default:
			before = true
		}
	}
	switch {
	case before && current != "":
		ifUnheld(root, func() {
			// Not a floor higher than the generation's, which an older helper may have raised.
			if !keepFloor(filepath.Join(root, current), root) {
				return
			}
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), "@") {
					os.RemoveAll(filepath.Join(root, e.Name()))
				}
			}
		})
	case before:
		stale := time.Now().Add(-templateTempStale)
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return nil
			case path != root && strings.HasPrefix(d.Name(), "@"):
				if d.IsDir() {
					return filepath.SkipDir
				}
			case d.Type().IsRegular() && strings.HasPrefix(d.Name(), templateTemp):
				if info, err := d.Info(); err == nil && info.ModTime().Before(stale) {
					os.Remove(path)
				}
			}
			return nil
		})
	}
	return current, true
}
