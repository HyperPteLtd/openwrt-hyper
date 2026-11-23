package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Apply the final permissions through an owned file descriptor. Both procd and
// RPC installations may inherit umask 077, including when repairing old slots.
func managedMode(f *os.File, mode os.FileMode) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != 0 && int(st.Uid) != os.Geteuid() || info.Mode().Perm()&0022 != 0 {
		return errors.New("untrusted kernel storage permissions")
	}
	if info.Mode().Perm() == mode && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 {
		return nil
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	return f.Sync()
}

func directoryMode(path string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return managedMode(f, mode)
}
func makeManagedDir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return directoryMode(path, mode)
}

// The archive has already been verified. Only its recorded paths may be
// exposed to other accounts, checking ancestors before their children.
func slotPermissions(root string, asset Asset) error {
	if err := directoryMode(root, 0755); err != nil {
		return err
	}
	dirs := map[string]bool{}
	for _, member := range asset.Files {
		dir := root
		parts := strings.Split(member.Path, "/")
		for _, part := range parts[:len(parts)-1] {
			dir = filepath.Join(dir, part)
			if !dirs[dir] {
				if err := directoryMode(dir, 0755); err != nil {
					return err
				}
				dirs[dir] = true
			}
		}
		f, err := openRegular(filepath.Join(root, member.Path))
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if member.Kind == "core" {
			mode = 0755
		}
		err = managedMode(f, mode)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
