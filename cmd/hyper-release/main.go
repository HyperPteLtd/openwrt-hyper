// Command hyper-release names and checks the Hyper Release assets for the build.
//
//	hyper-release version VERSION
//	hyper-release matrix [--format ipk|apk]
//	hyper-release arches ipk|apk
//	hyper-release package-name VERSION ipk|apk ARCH
//	hyper-release fake-latest DIR RESPONSE [--invalid KIND] [--contents FILE]
//	hyper-release licenses OUTPUT
//	hyper-release rules [--check-package DIR]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/release"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "hyper-release: "+format+"\n", args...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func args(fs *flag.FlagSet, argv []string, n int, usage string) []string {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: hyper-release %s %s\n", fs.Name(), usage)
		fs.PrintDefaults()
	}
	check(fs.Parse(argv))
	if fs.NArg() != n {
		fs.Usage()
		os.Exit(2)
	}
	return fs.Args()
}

func main() {
	if len(os.Args) < 2 {
		fail("expected a subcommand; see go doc ./cmd/hyper-release")
	}
	cmd, argv := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "version":
		a := args(fs, argv, 1, "VERSION")
		if !release.ValidVersion(a[0]) {
			fail("expected a numeric major.minor.patch version: %q", a[0])
		}
	case "arches":
		a := args(fs, argv, 1, "ipk|apk")
		if !slices.Contains(release.Formats, a[0]) {
			fail("invalid format %q", a[0])
		}
		fmt.Println(strings.Join(release.Arches[a[0]], " "))
	case "package-name":
		a := args(fs, argv, 3, "VERSION ipk|apk ARCH")
		if !release.ValidVersion(a[0]) || !slices.Contains(release.Formats, a[1]) || !slices.Contains(release.Arches[a[1]], a[2]) {
			fail("invalid version, format or architecture")
		}
		fmt.Println(release.PackageName(a[0], a[1], a[2]))
	case "matrix":
		format := fs.String("format", "", "ipk or apk")
		args(fs, argv, 0, "[--format ipk|apk]")
		if *format != "" && !slices.Contains(release.Formats, *format) {
			fail("invalid format %q", *format)
		}
		data, err := json.Marshal(map[string]any{"include": release.Matrix(*format)})
		check(err)
		fmt.Println(string(data))
	case "fake-latest":
		invalid := fs.String("invalid", "", strings.Join(release.FakeLatestInvalid, ", "))
		contents := fs.String("contents", "", "serve the hash of an alternate fixture package")
		// Accept the options after the positional arguments, as install-test.sh passes them.
		var positional []string
		for len(argv) > 0 {
			check(fs.Parse(argv))
			if fs.NArg() == 0 {
				break
			}
			positional = append(positional, fs.Arg(0))
			argv = fs.Args()[1:]
		}
		if len(positional) != 2 || (*invalid != "" && !slices.Contains(release.FakeLatestInvalid, *invalid)) {
			fail("usage: fake-latest DIR RESPONSE [--invalid KIND] [--contents FILE]")
		}
		asset, err := release.FakeLatest(positional[0], positional[1], *invalid, *contents)
		check(err)
		fmt.Println(asset)
	case "licenses":
		a := args(fs, argv, 1, "OUTPUT")
		check(os.MkdirAll(a[0], 0o755))
		check(release.InstallLicenses(a[0]))
	case "rules":
		pkg := fs.String("check-package", "", "verify a staged package rules directory instead of refreshing")
		args(fs, argv, 0, "[--check-package DIR]")
		if *pkg != "" {
			check(release.CheckRules(*pkg, false))
			return
		}
		// Run from the repository root.
		check(release.CheckRules(filepath.FromSlash("files/usr/share/hyper/rules"), true))
	default:
		fail("unknown subcommand %q", cmd)
	}
}
