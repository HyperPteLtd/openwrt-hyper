package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/openweir/subconv/rules"
)

// branchManifest lists files by path and content.
func branchManifest(t *testing.T, version int64, files map[string]string) []byte {
	t.Helper()
	m := rules.Manifest{Version: version}
	for _, path := range slices.Sorted(maps.Keys(files)) {
		sum := sha256.Sum256([]byte(files[path]))
		m.Files = append(m.Files, rules.File{Path: path, Size: int64(len(files[path])), SHA256: hex.EncodeToString(sum[:])})
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// stageVersion stages a version's manifest, floor and files over the stage's base.
func stageVersion(t *testing.T, s *templateStage, version int64, files map[string]string) {
	t.Helper()
	s.Put(rules.ManifestPath, branchManifest(t, version, files))
	s.Put(rules.FloorKey, []byte(fmt.Sprintln(version)))
	for path, data := range files {
		s.Put(path, []byte(data))
	}
}

func current(t *testing.T, root string) string {
	t.Helper()
	name, err := os.Readlink(filepath.Join(root, templateCurrent))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, name)
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(names)
	return names
}

var version1 = map[string]string{rules.TemplatePath: "t1", "providers/a.txt": "a", "providers/b.txt": "b"}

// A promotion writes what changed, links what did not and leaves behind what the manifest no
// longer lists; a refresh still reading the generation it replaced keeps it until done.
func TestTemplatePromotionWritesOnlyChanges(t *testing.T) {
	root := t.TempDir()
	s := &templateStage{root: root}
	stageVersion(t, s, 1, version1)
	if _, err := os.Lstat(filepath.Join(root, templateCurrent)); err == nil {
		t.Fatal("staged values reached the copy before the promotion")
	}
	s.promote()
	s.close()
	gen1 := current(t, root)
	if got := entries(t, gen1); !slices.Equal(got, []string{"floor", "manifest.json", "providers/a.txt", "providers/b.txt", "template.yaml"}) {
		t.Fatal(got)
	}
	a1, _ := os.Stat(filepath.Join(gen1, "providers/a.txt"))

	// The same version again: nothing to promote.
	s = &templateStage{root: root}
	stageVersion(t, s, 1, version1)
	s.promote()
	s.close()
	if current(t, root) != gen1 {
		t.Fatal("an unchanged version was written again")
	}

	reader := &templateStage{root: root}
	if _, ok := reader.Get(rules.ManifestPath); !ok {
		t.Fatal("no manifest")
	}
	s = &templateStage{root: root}
	stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t1", "providers/a.txt": "a", "providers/c.txt": "c"})
	s.Put("../escape", []byte("x"))
	if _, ok := s.Get("../escape"); ok {
		t.Fatal("cache left its directory")
	}
	s.promote()
	s.close()
	gen2 := current(t, root)
	if got := entries(t, gen2); gen2 == gen1 || !slices.Equal(got, []string{"floor", "manifest.json", "providers/a.txt", "providers/c.txt", "template.yaml"}) {
		t.Fatal(gen2, got)
	}
	if a2, _ := os.Stat(filepath.Join(gen2, "providers/a.txt")); !os.SameFile(a1, a2) {
		t.Fatal("an unchanged rule set was written again")
	}
	// The reader keeps its generation, whole.
	if b, ok := reader.Get("providers/b.txt"); !ok || string(b) != "b" {
		t.Fatal("a generation in use went")
	}
	reader.close()
	next := &templateStage{root: root}
	next.Get(rules.ManifestPath)
	next.close()
	if _, err := os.Stat(gen1); err == nil {
		t.Fatal("a generation no refresh uses stayed")
	}
}

// A refresh that began on an older copy does not replace a newer version another one promoted
// meanwhile, nor lower its floor.
func TestTemplatePromotionKeepsANewerCopy(t *testing.T) {
	root := t.TempDir()
	s := &templateStage{root: root}
	stageVersion(t, s, 1, version1)
	s.promote()
	s.close()

	older, newer := &templateStage{root: root}, &templateStage{root: root}
	older.Get(rules.ManifestPath)
	newer.Get(rules.ManifestPath)
	s = &templateStage{root: root}
	stageVersion(t, s, 3, map[string]string{rules.TemplatePath: "t3", "providers/a.txt": "a"})
	s.promote()
	s.close()
	gen3 := current(t, root)
	stageVersion(t, older, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
	older.promote()
	older.close()
	if current(t, root) != gen3 {
		t.Fatal("an older version replaced a newer one")
	}
	stageVersion(t, newer, 4, map[string]string{rules.TemplatePath: "t4", "providers/a.txt": "a"})
	newer.Put(rules.FloorKey, []byte("1\n"))
	newer.promote()
	newer.close()
	gen4 := current(t, root)
	if b, _ := os.ReadFile(filepath.Join(gen4, rules.ManifestPath)); gen4 == gen3 || !slices.Equal(b, branchManifest(t, 4, map[string]string{rules.TemplatePath: "t4", "providers/a.txt": "a"})) {
		t.Fatal("a newer version stayed out")
	}
	if floor, _ := os.ReadFile(filepath.Join(gen4, rules.FloorKey)); string(floor) != "3\n" {
		t.Fatalf("floor %q", floor)
	}
}

// What killed helpers and the copy before generations left goes, once no refresh can use it.
func TestTemplateSweep(t *testing.T) {
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The copy as earlier versions kept it, with the temporary files of killed writes.
	write(rules.ManifestPath, string(branchManifest(t, 1, version1)))
	for path, data := range version1 {
		write(path, data)
	}
	write("providers/.new-old", "x")
	write("providers/.new-recent", "x")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, "providers/.new-old"), old, old); err != nil {
		t.Fatal(err)
	}
	// A generation a killed promotion did not finish, and its link.
	write(templateGeneration+"cut/manifest.json", "{}")
	if err := os.Symlink(templateGeneration+"cut", filepath.Join(root, templateCurrentNext)); err != nil {
		t.Fatal(err)
	}

	s := &templateStage{root: root}
	if b, ok := s.Get("providers/a.txt"); !ok || string(b) != "a" {
		t.Fatal("the copy before generations is not read")
	}
	if got := entries(t, root); !slices.Equal(got, []string{"@lock", "manifest.json", "providers/.new-recent", "providers/a.txt", "providers/b.txt", "template.yaml"}) {
		t.Fatal(got)
	}
	stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
	s.promote()
	s.close()
	gen := current(t, root)
	if got := entries(t, gen); !slices.Equal(got, []string{"floor", "manifest.json", "providers/a.txt", "template.yaml"}) {
		t.Fatal(got)
	}
	names, _ := os.ReadDir(root)
	var left []string
	for _, e := range names {
		left = append(left, e.Name())
	}
	if !slices.Equal(left, []string{templateCurrent, filepath.Base(gen), templateLock}) {
		t.Fatal(left)
	}
}

// failSyncs makes the copy's directory syncs fail with EIO while fail says so, until the test
// ends.
func failSyncs(t *testing.T, fail func(dir string) bool) {
	t.Helper()
	templateSyncDir = func(dir string) error {
		if fail(dir) {
			return syscall.EIO
		}
		return atomicfile.SyncDir(dir)
	}
	t.Cleanup(func() { templateSyncDir = atomicfile.SyncDir })
}

func manifestVersion(t *testing.T, s *templateStage) int64 {
	t.Helper()
	raw, ok := s.Get(rules.ManifestPath)
	if !ok {
		return 0
	}
	m, err := rules.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m.Version
}

// A sync that fails after the link moved leaves the new generation, which the link names, and
// the one it replaced until a later sweep; one that fails before leaves the copy as it was.
func TestTemplatePromotionSurvivesAFailedSync(t *testing.T) {
	root := t.TempDir()
	s := &templateStage{root: root}
	stageVersion(t, s, 1, version1)
	s.promote()
	s.close()
	gen1 := current(t, root)

	// The generation's own sync fails: nothing changes.
	failSyncs(t, func(dir string) bool { return dir != root })
	s = &templateStage{root: root}
	stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
	if err := s.promote(); err == nil {
		t.Fatal("a failed sync went unreported")
	}
	s.close()
	if names, _ := os.ReadDir(root); current(t, root) != gen1 || len(names) != 3 {
		t.Fatal("a generation that failed its sync was kept or published", names)
	}

	// The root's sync after the rename fails (the link is in place, its replacement gone).
	failSyncs(t, func(dir string) bool {
		_, err := os.Lstat(filepath.Join(root, templateCurrentNext))
		return dir == root && err != nil
	})
	s = &templateStage{root: root}
	stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
	if err := s.promote(); err == nil {
		t.Fatal("a failed sync went unreported")
	}
	s.close()
	gen2 := current(t, root)
	if _, err := os.Stat(gen2); gen2 == gen1 || err != nil {
		t.Fatal("the link names a generation that is gone", err)
	}
	if _, err := os.Stat(gen1); err != nil {
		t.Fatal("the generation a power cut may bring back went", err)
	}
	failSyncs(t, func(string) bool { return false })
	next := &templateStage{root: root}
	if v := manifestVersion(t, next); v != 2 {
		t.Fatal("the next refresh reads version", v)
	}
	next.close()
	if _, err := os.Stat(gen1); err == nil {
		t.Fatal("a later sweep left the replaced generation")
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

// A link that names no generation, or that cannot be read, tells the sweep nothing: it removes
// no generation and not the copy before generations, and the refresh reads what that copy left.
// The next promotion puts the link right, and the sweep after it cleans up.
func TestTemplateSweepTrustsOnlyAWholeLink(t *testing.T) {
	for name, link := range map[string]func(path string) error{
		"dangling":   func(path string) error { return os.Symlink(templateGeneration+"gone", path) },
		"elsewhere":  func(path string) error { return os.Symlink("../template", path) },
		"unreadable": func(path string) error { return os.WriteFile(path, nil, 0600) }, // readlink: EINVAL
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			s := &templateStage{root: root}
			stageVersion(t, s, 1, version1)
			s.promote()
			s.close()
			gen1 := current(t, root)
			// The copy before generations, as an older helper may still write it.
			if err := os.WriteFile(filepath.Join(root, rules.ManifestPath), branchManifest(t, 1, version1), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, templateCurrent)); err != nil {
				t.Fatal(err)
			}
			if err := link(filepath.Join(root, templateCurrent)); err != nil {
				t.Fatal(err)
			}
			before := dirNames(t, root)

			reader := &templateStage{root: root}
			if v := manifestVersion(t, reader); v != 1 {
				t.Fatal("the copy before generations was not read:", v)
			}
			if got := dirNames(t, root); !slices.Equal(got, before) {
				t.Fatal("swept without a link to trust:", got, before)
			}
			reader.close()

			s = &templateStage{root: root}
			stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
			s.promote()
			s.close()
			gen2 := current(t, root)
			if got := dirNames(t, root); gen2 == gen1 || !slices.Equal(got, []string{templateCurrent, filepath.Base(gen2), templateLock}) {
				t.Fatal(got)
			}
		})
	}
}

// readOnly makes dir and the files given in it read-only until the test ends. Only a process
// without root's override sees them so.
func readOnly(t *testing.T, dir string, files ...string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only files")
	}
	for _, name := range files {
		if err := os.Chmod(filepath.Join(dir, name), 0400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
}

// A copy the router cannot write, as on a file system remounted read-only or one full before
// the lock file was ever created, is still read: through a lock opened for reading, or without
// one. Nothing of it is swept, and a promotion leaves it as it was.
func TestTemplateCopyReadWithoutWriting(t *testing.T) {
	t.Run("before generations, without a lock file", func(t *testing.T) {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, rules.ManifestPath), branchManifest(t, 1, version1), 0600)
		os.WriteFile(filepath.Join(root, rules.TemplatePath), []byte("t1"), 0600)
		readOnly(t, root)
		s := &templateStage{root: root}
		if v := manifestVersion(t, s); v != 1 {
			t.Fatal("the copy was not read:", v)
		}
		if b, ok := s.Get(rules.TemplatePath); !ok || string(b) != "t1" {
			t.Fatal("the template was not read")
		}
		s.close()
	})
	for name, files := range map[string][]string{"read-only lock": {templateLock}, "no lock file": nil} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			s := &templateStage{root: root}
			stageVersion(t, s, 1, version1)
			s.promote()
			s.close()
			// A generation a promotion did not finish, which a sweep would remove.
			os.Mkdir(filepath.Join(root, templateGeneration+"cut"), 0700)
			if files == nil {
				os.Remove(filepath.Join(root, templateLock))
			}
			readOnly(t, root, files...)
			before := dirNames(t, root)

			s = &templateStage{root: root}
			if v := manifestVersion(t, s); v != 1 {
				t.Fatal("the copy was not read:", v)
			}
			if b, ok := s.Get("providers/b.txt"); !ok || string(b) != "b" {
				t.Fatal("a rule set was not read")
			}
			stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
			if err := s.promote(); err == nil {
				t.Fatal("a copy that cannot be written went unreported")
			}
			s.close()
			if got := dirNames(t, root); !slices.Equal(got, before) {
				t.Fatal(got, before)
			}
		})
	}
}

// A refresh waits for the lock no longer than it has: then it reads the copy without the lock,
// and leaves it as it is.
func TestTemplateLockWaitEndsWithTheRefresh(t *testing.T) {
	root := t.TempDir()
	s := &templateStage{root: root}
	stageVersion(t, s, 1, version1)
	s.promote()
	s.close()
	gen1 := current(t, root)
	// Another helper holds the lock and does not let go.
	other, err := os.Open(filepath.Join(root, templateLock))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	s = &templateStage{root: root, ctx: ctx}
	if v := manifestVersion(t, s); v != 1 {
		t.Fatal("the copy was not read:", v)
	}
	stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
	if err := s.promote(); err == nil {
		t.Fatal("a promotion without the lock went unreported")
	}
	s.close()
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatal("waited past the refresh:", waited)
	}
	if current(t, root) != gen1 {
		t.Fatal("promoted without the lock")
	}
}

// Refreshes promote and read at once: the link always names a whole generation, and a sweep
// leaves only it.
func TestTemplateConcurrentRefreshes(t *testing.T) {
	root := t.TempDir()
	files := func(v int64) map[string]string {
		return map[string]string{rules.TemplatePath: fmt.Sprint("t", v), "providers/a.txt": "a", fmt.Sprintf("providers/v%d.txt", v%3): fmt.Sprint("x", v)}
	}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			s := &templateStage{root: root, ctx: t.Context()}
			s.Get(rules.FloorKey)
			v := int64(1 + rand.IntN(10))
			stageVersion(t, s, v, files(v))
			s.promote()
			s.close()
		})
		if i%3 == 0 {
			wg.Go(func() {
				r := &templateStage{root: root, ctx: t.Context()}
				defer r.close()
				raw, ok := r.Get(rules.ManifestPath)
				if !ok {
					return
				}
				m, err := rules.ParseManifest(raw)
				if err != nil {
					t.Error("torn manifest")
					return
				}
				for _, f := range m.Files {
					if b, ok := r.Get(f.Path); !ok || !f.Matches(b) {
						t.Errorf("version %d: %s missing or unlike the manifest", m.Version, f.Path)
					}
				}
			})
		}
	}
	wg.Wait()
	gen := current(t, root)
	raw, err := os.ReadFile(filepath.Join(gen, rules.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	m, err := rules.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		if b, err := os.ReadFile(filepath.Join(gen, f.Path)); err != nil || !f.Matches(b) {
			t.Fatal("the current generation lacks", f.Path)
		}
	}
	n := &templateStage{root: root}
	n.Get(rules.FloorKey)
	n.close()
	if got := dirNames(t, root); !slices.Equal(got, []string{templateCurrent, filepath.Base(gen), templateLock}) {
		t.Fatal("after a sweep:", got)
	}
}

func floorOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rules.FloorKey))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Two refreshes that began on the same copy: one got a version from a single operator, which
// leaves the floor, and the other got it, or an older one, trusted, which raises the floor. The
// floor the trusted one raised stays, whichever promotes first, with its version.
func TestTemplateFloorNeverGoesBack(t *testing.T) {
	v2 := map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"}
	v3 := map[string]string{rules.TemplatePath: "t3", "providers/a.txt": "a"}
	for _, tc := range []struct {
		name           string
		untrusted      int64
		files          map[string]string
		version, floor int64
		untrustedFirst bool
	}{
		{name: "same version, untrusted first", untrusted: 2, files: v2, version: 2, floor: 2, untrustedFirst: true},
		{name: "same version, trusted first", untrusted: 2, files: v2, version: 2, floor: 2},
		{name: "newer untrusted version first", untrusted: 3, files: v3, version: 2, floor: 2, untrustedFirst: true},
		{name: "newer untrusted version after", untrusted: 3, files: v3, version: 3, floor: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := &templateStage{root: root}
			stageVersion(t, s, 1, version1)
			s.promote()
			s.close()
			untrusted, trusted := &templateStage{root: root}, &templateStage{root: root}
			untrusted.Get(rules.FloorKey)
			trusted.Get(rules.FloorKey)
			untrusted.Put(rules.ManifestPath, branchManifest(t, tc.untrusted, tc.files))
			for path, data := range tc.files {
				untrusted.Put(path, []byte(data))
			}
			stageVersion(t, trusted, 2, v2)
			first, second := trusted, untrusted
			if tc.untrustedFirst {
				first, second = untrusted, trusted
			}
			if first.promote() != nil || second.promote() != nil {
				t.Fatal("a promotion failed")
			}
			first.close()
			second.close()
			gen := current(t, root)
			n := &templateStage{root: root}
			if v := manifestVersion(t, n); v != tc.version || floorOf(t, gen) != fmt.Sprintln(tc.floor) {
				t.Fatalf("version %d, floor %q", v, floorOf(t, gen))
			}
			n.close()
		})
	}
}

// The copy before generations goes once a generation is current, but not with a floor higher
// than the generation's, which a helper of the version before generations may have raised.
func TestTemplateSweepKeepsTheHigherFloor(t *testing.T) {
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700)
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(rules.ManifestPath, string(branchManifest(t, 1, version1)))
	write(rules.FloorKey, "1\n")
	for path, data := range version1 {
		write(path, data)
	}
	s := &templateStage{root: root}
	stageVersion(t, s, 2, map[string]string{rules.TemplatePath: "t2", "providers/a.txt": "a"})
	// Meanwhile, an older helper got version 3 trusted.
	write(rules.FloorKey, "3\n")
	s.promote()
	s.close()
	gen := current(t, root)
	if floor := floorOf(t, gen); floor != "3\n" {
		t.Fatalf("floor %q", floor)
	}
	if got := dirNames(t, root); !slices.Equal(got, []string{templateCurrent, filepath.Base(gen), templateLock}) {
		t.Fatal(got)
	}
}
