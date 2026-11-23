package mirror

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func urls(cs []Candidate) (out []string) {
	for _, c := range cs {
		out = append(out, c.URL)
	}
	return
}

func TestCandidates(t *testing.T) {
	StateFile = filepath.Join(t.TempDir(), "state")
	saved := RawCDNs
	RawCDNs = []string{"https://cdn.example/gh/"}
	defer func() { RawCDNs = saved }()
	p := []string{"https://m.example/"}
	if got := urls(Candidates("https://example.org/a", p)); !reflect.DeepEqual(got, []string{"https://example.org/a"}) {
		t.Fatal(got)
	}
	rel := "https://github.com/o/r/releases/download/v1/f"
	if got := urls(Candidates(rel, p)); !reflect.DeepEqual(got, []string{rel, "https://m.example/" + rel}) {
		t.Fatal(got)
	}
	raw := "https://raw.githubusercontent.com/o/r/abc/dir/geo.dat"
	want := []string{raw, "https://m.example/" + raw, "https://cdn.example/gh/o/r@abc/dir/geo.dat"}
	if got := urls(Candidates(raw, p)); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	Remember("https://cdn.example/gh/")
	if b, _ := os.ReadFile(StateFile); string(b) != "https://cdn.example/gh/\n" {
		t.Fatal(string(b))
	}
	if got := urls(Candidates(raw, p)); got[0] != want[2] || len(got) != 3 {
		t.Fatal(got)
	}
	if got := urls(Candidates(rel, p)); got[0] != rel {
		t.Fatal("a CDN prefix never applies to release assets", got)
	}
	if Valid("http://x/") || Valid("https://x") || !Valid("https://x/") {
		t.Fatal("Valid")
	}
}
