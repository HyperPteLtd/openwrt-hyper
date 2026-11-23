// Package atomicfile replaces a file whole or not at all: a reader, or the router after a power
// cut, finds either the old content or the new, never a mix of the two or a torn file.
package atomicfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile replaces path with data, as Write does.
func WriteFile(path string, data []byte, mode fs.FileMode, temp string) error {
	return Write(path, mode, temp, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// Write replaces path with what fill writes. fill writes a new file in path's directory, which
// has exactly mode, whatever the umask; the file is synced, closed and renamed over path, and
// then the directory is synced, so that the rename too survives a power cut. fill must not
// close the file.
//
// temp is the os.CreateTemp pattern of the new file's name until the rename. A crash can leave
// that file behind: a caller that sweeps them keeps the name its sweep recognises.
//
// When fill or any step up to the rename fails, path is left as it was and the new file is
// removed. A failure to sync the directory comes after the rename: path holds the new content
// then, but a power cut may still undo the rename.
func Write(path string, mode fs.FileMode, temp string, fill func(*os.File) error) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, temp)
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		err = fill(f)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	if err = syncDir(dir); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

// SyncDir makes the entries of a directory durable: the files created, renamed into or removed
// from it since.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// syncDir is SyncDir, which tests watch.
var syncDir = SyncDir
