package subscription

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// ImportDir holds the files a user copies to the router for its subscriptions to read, such as
// the local rule files under rulesets/source-<id>.
const ImportDir = "/etc/hyper/imports"

const ruleImportRoot = ImportDir + "/rulesets"

var sidecarBase = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,120}\.(yaml|yml|txt|list|mrs)$`)

func (f Fetcher) localClashSet(raw string) ([]byte, error) {
	name := filepath.Base(raw)
	if !sourceIDValid(f.sourceID) || !sidecarBase.MatchString(name) {
		return nil, errRulePayload
	}
	root := f.ruleImportRoot
	if root == "" {
		root = ruleImportRoot
	}
	dir := filepath.Join(root, "source-"+f.sourceID)
	return trustedFileContents(filepath.Join(dir, name), MaxBody, root, dir)
}

var errRuleFileLimit = errors.New("rule file size exceeds limit")

func trustedFileContents(file string, limit int64, dirs ...string) ([]byte, error) {
	for _, dir := range dirs {
		st, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		// Links do not declare an eligible local original. Do not follow them
		// or consult their targets, including when deciding whether one exists.
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, os.ErrNotExist
		}
		if !st.IsDir() || st.Mode().Perm()&0022 != 0 || int(st.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
			return nil, os.ErrPermission
		}
	}
	st, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrNotExist
	}
	if !st.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0444 == 0 || int(st.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return nil, os.ErrPermission
	}
	if st.Size() > limit {
		return nil, errRuleFileLimit
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errRuleFileLimit
	}
	return b, err
}
