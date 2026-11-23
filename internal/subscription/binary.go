package subscription

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

type BinarySet struct {
	Behavior string `json:"behavior,omitempty"`
	Entries  int    `json:"entries"`
	Format   string `json:"format,omitempty"`
	Tag      string `json:"tag"`
	SHA256   string `json:"sha256"`
	Data     []byte `json:"data"`
}

const maxExpandedSet = 16 << 20

// maxBinarySets bounds the rule sets of one profile.
const maxBinarySets = 96

func (s BinarySet) FileFormat() string { return s.Format }
func (s BinarySet) Filename() string {
	if s.Format == "text" {
		return s.SHA256 + ".list"
	}
	return s.SHA256 + "." + s.Format
}
func (s BinarySet) contents() ([]byte, error) {
	if s.Format != "text" && s.Format != "yaml" && s.Format != "mrs" {
		return nil, errors.New("invalid_rule_set_format")
	}
	h := sha256.Sum256(s.Data)
	if s.SHA256 != hex.EncodeToString(h[:]) || !safeLabel(s.Tag) || len(s.Data) > MaxBody {
		return nil, errors.New("binary_set_digest_mismatch")
	}
	return s.Data, nil
}
func lockSets(dir string) (*os.File, error) {
	// The directory is private to root, which the core runs as. The core
	// watches it with inotify for each local rule set.
	e := os.Mkdir(dir, 0700)
	if e != nil && !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	d, e := os.Lstat(dir)
	if e != nil || !d.IsDir() {
		return nil, errors.New("invalid_rule_directory")
	}
	if int(d.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return nil, errors.New("invalid_rule_directory_owner")
	}
	if e = os.Chmod(dir, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, "sets.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}

// MaterializeSets writes the exact bytes referenced by the approved profile.
// Both formats use content-addressed paths independent of remote tags.
func MaterializeSets(p Profile, dir string) error {
	if len(p.BinarySets) == 0 {
		return nil
	}
	if len(p.BinarySets) > maxBinarySets {
		return errors.New("invalid_binary_set_config")
	}
	lock, e := lockSets(dir)
	if e != nil {
		return e
	}
	defer unlock(lock)
	used := map[string]bool{}
	total, expanded := 0, 0
	for _, s := range p.BinarySets {
		total += len(s.Data)
		if total > MaxBody || used[s.Tag] {
			return errors.New("invalid_binary_set_config")
		}
		used[s.Tag] = true
		data, e := s.contents()
		if e != nil {
			return e
		}
		expanded += len(data)
		if expanded > maxSetMaterialization {
			return errors.New("provider_size_limit")
		}
		dst := filepath.Join(dir, s.Filename())
		if st, e := os.Lstat(dst); e == nil {
			if !st.Mode().IsRegular() {
				return errors.New("invalid_cached_rule_file")
			}
			if st.Size() == int64(len(data)) && st.Mode().Perm() == 0600 && int(st.Sys().(*syscall.Stat_t).Uid) == os.Geteuid() {
				b, e := os.ReadFile(dst)
				if e == nil && bytes.Equal(b, data) {
					continue
				}
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e = writeSet(dir, dst, data); e != nil {
			return e
		}
	}
	return atomicfile.SyncDir(dir)
}

// writeSet is not atomicfile.WriteFile: MaterializeSets syncs the directory once for the whole batch.
func writeSet(dir, dst string, b []byte) error {
	// CreateTemp makes the file 0600, private to root like its directory.
	f, e := os.CreateTemp(dir, ".rule-set-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), dst)
}

// CollectSets releases files no longer referenced by a current or recoverable
// authorized snapshot. Other sources may still own the same content hash.
func (s *Store) CollectSets(dir string) error {
	if _, e := os.Lstat(dir); errors.Is(e, os.ErrNotExist) {
		return nil
	}
	lock, e := lockSets(dir)
	if e != nil {
		return e
	}
	defer unlock(lock)
	list, e := s.List()
	if e != nil {
		return e
	}
	keep := map[string]bool{}
	for _, v := range list {
		r, e := s.read(v.Source)
		if e != nil {
			return e
		}
		if r.Revoked {
			continue
		}
		for _, p := range []*Profile{r.Current, r.Previous} {
			if p != nil {
				for _, rs := range p.BinarySets {
					keep[rs.Filename()] = true
				}
			}
		}
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	for _, v := range entries {
		name := v.Name()
		if keep[name] {
			continue
		}
		if strings.HasPrefix(name, ".rule-set-") || strings.HasPrefix(name, ".srs-") {
			if e = os.Remove(filepath.Join(dir, name)); e != nil {
				return e
			}
			continue
		}
		ext := filepath.Ext(name)
		switch ext {
		case ".yaml", ".list", ".mrs", ".srs", ".json": // Includes caches from the previous core.
		default:
			continue
		}
		sum, err := hex.DecodeString(strings.TrimSuffix(name, ext))
		if err != nil || len(sum) != sha256.Size {
			continue
		}
		if e = os.Remove(filepath.Join(dir, name)); e != nil {
			return e
		}
	}
	return atomicfile.SyncDir(dir)
}
