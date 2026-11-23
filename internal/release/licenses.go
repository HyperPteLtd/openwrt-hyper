package release

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// InstallLicenses copies the notices of the Go runtime and of every module linked into
// ./cmd/hyperd (run from the module root) into output.
func InstallLicenses(output string) error {
	list, err := exec.Command("go", "list", "-mod=readonly", "-deps", "-f",
		"{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}", "./cmd/hyperd").Output()
	if err != nil {
		return fmt.Errorf("go list: %w", err)
	}
	modules := map[string]string{}
	rows := bufio.NewScanner(bytes.NewReader(list))
	for rows.Scan() {
		if rows.Text() == "" {
			continue
		}
		fields := strings.Split(rows.Text(), "\t")
		if len(fields) != 3 {
			return fmt.Errorf("unexpected go list row: %q", rows.Text())
		}
		name, version, dir := fields[0], fields[1], fields[2]
		if _, seen := modules[name]; seen {
			continue
		}
		modules[name] = version
		dest := filepath.Join(output, "modules", name)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		found := false
		for _, e := range entries {
			lower := strings.ToLower(e.Name())
			if !e.Type().IsRegular() || !(strings.HasPrefix(lower, "license") || strings.HasPrefix(lower, "copying") ||
				strings.HasPrefix(lower, "notice") || strings.HasPrefix(lower, "patents")) {
				continue
			}
			found = true
			if err := copyOver(filepath.Join(dir, e.Name()), filepath.Join(dest, e.Name())); err != nil {
				return err
			}
		}
		if !found {
			return fmt.Errorf("no license notice for %s", name)
		}
	}
	names := make([]string, 0, len(modules))
	for n := range modules {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s %s\n", n, modules[n])
	}
	if err := os.WriteFile(filepath.Join(output, "modules.txt"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(goroot))
	if err := os.MkdirAll(filepath.Join(output, "go"), 0o755); err != nil {
		return err
	}
	for _, n := range []string{"LICENSE", "PATENTS"} {
		if info, err := os.Stat(filepath.Join(root, n)); err == nil && info.Mode().IsRegular() {
			if err := copyOver(filepath.Join(root, n), filepath.Join(output, "go", n)); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyOver(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
