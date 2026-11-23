// Package router serializes the router's forwarding lifecycle.
package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

var ErrNotReady = errors.New("state not ready")
var ErrSchema = errors.New("unsupported state schema")

type Intent struct {
	Schema   int                          `json:"schema_version"`
	Revision uint64                       `json:"revision"`
	Enabled  bool                         `json:"enabled"`
	Mode     string                       `json:"mode"`
	Source   string                       `json:"source,omitempty"`
	Policy   Policy                       `json:"policy,omitempty"`
	Choices  map[string]map[string]string `json:"choices,omitempty"`
}

func initial() Intent { return Intent{Schema: 1, Mode: "smart"} }
func (s Intent) clone() Intent {
	r := s
	r.Choices = make(map[string]map[string]string, len(s.Choices))
	for src, groups := range s.Choices {
		x := make(map[string]string, len(groups))
		for k, v := range groups {
			x[k] = v
		}
		r.Choices[src] = x
	}
	return r
}
func validMode(mode string) bool { return mode == "smart" || mode == "global" || mode == "direct" }

type Disk struct{ Path string }

func (s Disk) Load() (Intent, error) {
	b, e := os.ReadFile(s.Path)
	if e == nil {
		if v, err := decodeIntent(b); !errors.Is(err, ErrNotReady) {
			return v, err
		}
	} else if !os.IsNotExist(e) {
		return Intent{}, e
	}
	previous, backupErr := os.ReadFile(s.Path + ".previous")
	if backupErr == nil {
		return decodeIntent(previous)
	}
	if !os.IsNotExist(backupErr) {
		return Intent{}, backupErr
	}
	if e != nil {
		return Intent{}, e
	}
	return Intent{}, ErrNotReady
}
func decodeIntent(b []byte) (Intent, error) {
	var header struct {
		Schema int `json:"schema_version"`
	}
	if !json.Valid(b) {
		return Intent{}, ErrNotReady
	}
	if e := json.Unmarshal(b, &header); e != nil {
		return Intent{}, ErrNotReady
	}
	if header.Schema != 1 {
		return Intent{}, ErrSchema
	}
	var v Intent
	if e := json.Unmarshal(b, &v); e != nil || !validMode(v.Mode) {
		return Intent{}, ErrNotReady
	}
	return v, nil
}
func (s Disk) Save(v Intent) error { return s.save(v, Atomic) }
func (s Disk) save(v Intent, write func(string, []byte, os.FileMode) error) error {
	previous, err := os.ReadFile(s.Path)
	if err == nil {
		if _, e := decodeIntent(previous); e == nil {
			if e = write(s.Path+".previous", previous, 0600); e != nil {
				return e
			}
		} else if errors.Is(e, ErrSchema) {
			return e
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if e = write(s.Path, b, 0600); e != nil {
		// A directory-sync error can follow a successful rename. If the new
		// bytes became visible, restore the verified checkpoint before returning.
		if live, err := os.ReadFile(s.Path); err == nil && bytes.Equal(live, b) {
			if old, err := os.ReadFile(s.Path + ".previous"); err == nil {
				if restore := Atomic(s.Path, old, 0600); restore != nil {
					return errors.Join(e, restore)
				}
			} else if os.IsNotExist(err) {
				if restore := os.Remove(s.Path); restore != nil {
					return errors.Join(e, restore)
				}
			}
		}
		return e
	}
	return nil
}

// Atomic publishes only a completely written file on the same volume (atomicfile.WriteFile),
// creating its directory, private, if missing.
func Atomic(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	return atomicfile.WriteFile(path, b, mode, ".hyper-")
}
