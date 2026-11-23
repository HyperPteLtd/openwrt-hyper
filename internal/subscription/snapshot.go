package subscription

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

type sourceSnapshot struct {
	path, withdrawal string
	digest           []byte
}

// SnapshotReader records the exact source files read for one startup. It stores
// only digests and paths and is used by a single goroutine.
type SnapshotReader struct {
	store *Store
	files []sourceSnapshot
}

func (s *Store) SnapshotReader() *SnapshotReader { return &SnapshotReader{store: s} }

func (s *SnapshotReader) Load(id string) (Profile, error) {
	hash := sha256.New()
	r, err := s.store.readWithChecksum(id, hash)
	profile, err := currentProfile(r, err)
	if err == nil {
		s.files = append(s.files, sourceSnapshot{s.store.path(id), s.store.withdrawalPath(r), hash.Sum(nil)})
	}
	return profile, err
}

// Unchanged rechecks the bytes that were decoded, including credentials and
// authorization state. Account withdrawal can precede any write to plan files.
func (s *SnapshotReader) Unchanged() bool {
	if len(s.files) == 0 {
		return false
	}
	buf := make([]byte, 32<<10)
	hash := sha256.New()
	for _, entry := range s.files {
		f, err := openRecordFile(entry.path)
		if err != nil {
			return false
		}
		hash.Reset()
		n, err := io.CopyBuffer(hash, io.LimitReader(f, maxRecord+1), buf)
		f.Close()
		if err != nil || n > maxRecord || !bytes.Equal(hash.Sum(nil), entry.digest) {
			return false
		}
	}
	for _, entry := range s.files {
		if entry.withdrawal != "" {
			if _, err := os.Stat(entry.withdrawal); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
	}
	return true
}
