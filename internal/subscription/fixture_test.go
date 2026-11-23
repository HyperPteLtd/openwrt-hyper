package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"go.yaml.in/yaml/v3"
)

// errUnreachable is how a fixture fails in transit, as the Hyper account's reads do when the
// service does not answer.
var errUnreachable = errors.New("locations_unreachable")

// serve makes fn serve the fixture sources' documents (Fixture) until the test ends.
func serve(t *testing.T, fn func(ctx context.Context, id string) ([]byte, error)) {
	t.Helper()
	previous := Fixture
	Fixture = fn
	t.Cleanup(func() { Fixture = previous })
}

// feed is a fixture source's subscription as the test changes it.
type feed struct {
	body      string
	reachable bool
	// failure is what an unreachable feed fails with: errUnreachable unless set.
	failure error
	reads   int
	// onRead runs on every read, after the refresh began.
	onRead func(ctx context.Context)
}

func (f *feed) read(ctx context.Context) ([]byte, error) {
	f.reads++
	if f.onRead != nil {
		f.onRead(ctx)
	}
	if !f.reachable {
		if f.failure != nil {
			return nil, f.failure
		}
		return nil, errUnreachable
	}
	return []byte(f.body), nil
}

// fixtureSource configures the fixture source id in a new store, served from the feed it
// returns, which starts reachable with body.
func fixtureSource(t *testing.T, id, body string) (*Store, *feed) {
	t.Helper()
	f := &feed{body: body, reachable: true}
	serve(t, func(ctx context.Context, got string) ([]byte, error) {
		if got != id {
			return nil, errors.New("unknown_fixture")
		}
		return f.read(ctx)
	})
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: id, Name: id, Kind: FixtureSource}); err != nil {
		t.Fatal(err)
	}
	return store, f
}

// textSet is a rule set of the profile's own, in the text format.
func textSet(tag, behavior, data string) BinarySet {
	sum := sha256.Sum256([]byte(data))
	entries := 0
	for _, c := range data {
		if c == '\n' {
			entries++
		}
	}
	return BinarySet{Tag: tag, Format: "text", Behavior: behavior, Entries: entries, SHA256: hex.EncodeToString(sum[:]), Data: []byte(data)}
}

// parseWithSets reads a Clash document whose RULE-SET rules use sets, as a Hyper profile's do.
func parseWithSets(t *testing.T, id, body string, sets ...BinarySet) Profile {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	p := Profile{SchemaVersion: SchemaVersion, Source: id, Format: "clash", BinarySets: sets}
	if err := parseClash(&p, doc); err != nil {
		t.Fatal(err)
	}
	resolve(&p)
	if len(p.Nodes) == 0 {
		t.Fatal(p.Notices)
	}
	return p
}

func TestFixtureSourceOnlyInTests(t *testing.T) {
	store := OpenStore(t.TempDir())
	src := Source{ID: "fixture", Name: "fixture", Kind: FixtureSource}
	if err := store.Configure(src); err == nil {
		t.Fatal("fixture source configured without a fixture")
	}
	for _, other := range []Source{NewHyperSource(nil), {ID: "x", Name: "x", Kind: "external"}, {ID: "x", Name: "x", Kind: "file"}, {ID: "x", Name: "x", Kind: "platform"}} {
		if err := store.Configure(other); err == nil {
			t.Fatal("configured", other.Kind)
		}
	}
	serve(t, func(context.Context, string) ([]byte, error) { return []byte(basic), nil })
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	c, err := (Fetcher{}).Refresh(t.Context(), store, src.ID, Policy{})
	if err != nil || c.Decision != "ready" || len(c.Profile.Nodes) != 1 {
		t.Fatal(err, c.Decision)
	}
	// Without the fixture the source cannot be read.
	Fixture = nil
	if _, err := (Fetcher{}).Refresh(t.Context(), store, src.ID, Policy{}); err == nil || err.Error() != "invalid_source_kind" {
		t.Fatal(err)
	}
}
