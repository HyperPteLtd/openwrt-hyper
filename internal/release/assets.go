package release

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// MaxPackage is the same limit as the router's package updater.
const MaxPackage = 256 << 20

func expectEntries(dir string, names []string, directories bool) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected a directory: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var changed []string
	for _, e := range entries {
		if !want[e.Name()] {
			changed = append(changed, e.Name())
		}
		delete(want, e.Name())
	}
	changed = append(changed, slices.Collect(maps.Keys(want))...)
	if len(changed) > 0 {
		sort.Strings(changed)
		return fmt.Errorf("unexpected or missing entries in %s: %v", dir, changed)
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 || e.IsDir() != directories || (!directories && !e.Type().IsRegular()) {
			return fmt.Errorf("unexpected file type: %s", filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sameDigest(a, b string) (bool, error) {
	da, err := Digest(a)
	if err != nil {
		return false, err
	}
	db, err := Digest(b)
	return da == db, err
}

// commitOutputs publishes the validated package directory last. A retry can complete after
// a sidecar was moved, but cannot combine it with a different build's packages.
func commitOutputs(work, output string) error {
	parent := filepath.Dir(output)
	fd, err := syscall.Open(parent, syscall.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return err
	}
	stage := filepath.Join(work, "packages")
	entries, err := os.ReadDir(work)
	if err != nil {
		return err
	}
	var sidecars []string
	for _, e := range entries {
		if e.Name() == "packages" {
			continue
		}
		if e.Name() == filepath.Base(output) {
			return fmt.Errorf("asset directory conflicts with release metadata: %s", output)
		}
		sidecars = append(sidecars, e.Name())
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(output)
	exists := statErr == nil
	if exists && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unexpected output symlink: %s", output)
	}
	if exists {
		var names []string
		for _, e := range staged {
			names = append(names, e.Name())
		}
		if err := expectEntries(output, names, false); err != nil {
			return err
		}
		for _, e := range staged {
			same, err := sameDigest(filepath.Join(output, e.Name()), filepath.Join(stage, e.Name()))
			if err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("existing output differs: %s", e.Name())
			}
		}
	}
	for _, name := range sidecars {
		dest := filepath.Join(parent, name)
		di, err := os.Lstat(dest)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !di.Mode().IsRegular() {
			return fmt.Errorf("existing release metadata differs: %s", dest)
		}
		if same, err := sameDigest(dest, filepath.Join(work, name)); err != nil || !same {
			return fmt.Errorf("existing release metadata differs: %s", dest)
		}
	}
	for _, name := range sidecars {
		dest := filepath.Join(parent, name)
		if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(filepath.Join(work, name), dest); err != nil {
				return err
			}
		}
	}
	if !exists {
		return os.Rename(stage, output)
	}
	return nil
}

// Assemble verifies the per-architecture installers in feeds/release-installer-{ipk,apk} against their
// checksums and stages them in output, with one combined SHA256SUMS beside it.
func Assemble(version, feeds, output string) error {
	if !ValidPackageVersion(version) {
		return fmt.Errorf("expected a release version such as 1.0.0, or one with a package revision such as 1.0.0-r3: %s", version)
	}
	var dirs []string
	for _, f := range Formats {
		dirs = append(dirs, "release-installer-"+f)
	}
	if err := expectEntries(feeds, dirs, true); err != nil {
		return err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(parent, ".assets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	stage := filepath.Join(work, "packages")
	if err := os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	var sums []string
	for _, format := range Formats {
		names := FormatNames(version, format)
		source := filepath.Join(feeds, "release-installer-"+format)
		if err := expectEntries(source, append(slices.Clone(names), "SHA256SUMS"), false); err != nil {
			return err
		}
		checksum := filepath.Join(source, "SHA256SUMS")
		if info, err := os.Stat(checksum); err != nil || info.Size() > 64<<10 {
			return fmt.Errorf("invalid checksum file: %s", checksum)
		}
		listedBytes, err := os.ReadFile(checksum)
		if err != nil {
			return err
		}
		var lines []string
		for _, name := range names {
			pkg := filepath.Join(source, name)
			info, err := os.Stat(pkg)
			if err != nil {
				return err
			}
			if info.Size() <= 0 || info.Size() > MaxPackage {
				return fmt.Errorf("invalid package size: %s", name)
			}
			if err := copyFile(pkg, filepath.Join(stage, name)); err != nil {
				return err
			}
			d, err := Digest(filepath.Join(stage, name))
			if err != nil {
				return err
			}
			lines = append(lines, d+"  "+name+"\n")
		}
		listed := strings.SplitAfter(string(listedBytes), "\n")
		if listed[len(listed)-1] == "" {
			listed = listed[:len(listed)-1]
		}
		sort.Strings(listed)
		sorted := slices.Clone(lines)
		sort.Strings(sorted)
		if !slices.Equal(listed, sorted) {
			return fmt.Errorf("installer checksums mismatch: release-installer-%s", format)
		}
		sums = append(sums, lines...)
	}
	sort.Slice(sums, func(i, j int) bool { return strings.Fields(sums[i])[1] < strings.Fields(sums[j])[1] })
	if err := os.WriteFile(filepath.Join(work, "SHA256SUMS"), []byte(strings.Join(sums, "")), 0o644); err != nil {
		return err
	}
	return commitOutputs(work, output)
}
