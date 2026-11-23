package subscription

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

var ErrSuperseded = errors.New("subscription intent superseded")
var ErrRevoked = errors.New("subscription access revoked")
var ErrNotReady = errors.New("subscription is not ready")
var ErrSuspended = errors.New("subscription temporarily unavailable")
var ErrLoginRequired = errors.New("subscription login required")

type Store struct{ dir string }

func OpenStore(dir string) *Store { return &Store{dir} }

type Ticket struct {
	Source     string `json:"source"`
	Epoch      uint64 `json:"epoch"`
	Generation uint64 `json:"generation"`
}

// A record is kept in two files on flash. The record file (path) holds the source, its
// authority and its profiles with their rule sets; the status file (statusPath) the bookkeeping
// of its refreshes, which changes with every attempt. An update writes only the files whose
// content it changed (save), so a refresh that finds nothing new rewrites a few hundred bytes
// of status rather than the profiles.
//
// The record file is written first, and the status file names the record file it was written
// with by its digest. A status file that names another record file is older: the record file was
// written after it, by an update interrupted before its status file, or by an earlier version
// that kept everything in the record file. The record file's own bookkeeping counts then, as in
// the earlier format. When the record file has none, the older status file still tells when the
// source last updated and how many attempts followed, but not what it held of the profiles it
// was written with (bookkeeping.older). Until a status file names it, the record file carries
// the bookkeeping itself (save).
type record struct {
	legacy        bool
	disk          recordDisk
	NeedsRefresh  bool     `json:"needs_refresh,omitempty"`
	SchemaVersion int      `json:"schema_version"`
	Source        string   `json:"source"`
	Epoch         uint64   `json:"epoch"`
	Generation    uint64   `json:"generation"`
	Revoked       bool     `json:"revoked,omitempty"`
	Suspended     bool     `json:"suspended,omitempty"`
	LoginRequired bool     `json:"login_required,omitempty"`
	Current       *Profile `json:"current,omitempty"`
	Config        *Source  `json:"config,omitempty"`
	Previous      *Profile `json:"previous,omitempty"`
	bookkeeping
}

// bookkeeping is what a record's refreshes keep up to date. It goes to the status file; the
// record files of earlier versions hold it themselves.
type bookkeeping struct {
	LastTLS              *tlsdiag.Diagnostic `json:"last_tls,omitempty"`
	ClockRetryGeneration uint64              `json:"clock_retry_generation,omitempty"`
	Usage                *Usage              `json:"usage,omitempty"`
	LastFailure          string              `json:"last_failure,omitempty"`
	LastChange           *ChangeCounts       `json:"last_change,omitempty"`
	PendingUpdate        *PendingUpdate      `json:"pending_update,omitempty"`
	UpdatedAt            string              `json:"updated_at,omitempty"`
	// Attempts counts refreshes begun since the last success. With the latest start they
	// space out the scheduler's retries; begin writes them to the status file anyway.
	Attempts    int    `json:"attempts,omitempty"`
	AttemptedAt string `json:"attempted_at,omitempty"`
}

// older is what a status file still tells of a source once the record file was written after
// it: when it last updated, the attempts since, the last failure, the update waiting. What
// described the profiles it was written with, the counts of the change, does not reach the
// record file's.
func (b bookkeeping) older() bookkeeping {
	b.LastChange = nil
	return b
}

// statusFile is the content of a record's status file. Record is the hex SHA-256 of the record
// file it was written with; the status files of the previous version name only the generation.
type statusFile struct {
	Source     string `json:"source"`
	Epoch      uint64 `json:"epoch,omitempty"`
	Generation uint64 `json:"generation"`
	Record     string `json:"record,omitempty"`
	bookkeeping
}

// recordDisk is what a record's files held when it was read, for an update to leave alone what
// it did not change.
type recordDisk struct {
	generation uint64 // of the record file
	digest     []byte // SHA-256 of the record file
	status     []byte // the status file, when it names the record file
}

const maxRecord = 64 << 20
const maxStatus = 1 << 20

// errUnchanged lets an update leave the record as it is: records hold whole profiles and
// rule sets, and every write is synced to flash.
var errUnchanged = errors.New("record unchanged")

func failureDigest(r *record) string {
	return digest([]any{r.LastFailure, r.LastTLS, r.ClockRetryGeneration, r.PendingUpdate})
}

// A single writer lock covers read, authorization check and publication. All
// authorization generations and the only two usable snapshots share one commit.
func (s *Store) update(id string, fn func(*record) error) error {
	return s.updateContext(context.Background(), id, fn)
}

func (s *Store) updateContext(ctx context.Context, id string, fn func(*record) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !sourceIDValid(id) || s.dir == "" {
		return errors.New("invalid source")
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	lock, e := os.OpenFile(filepath.Join(s.dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	r, e := s.read(id)
	if errors.Is(e, os.ErrNotExist) {
		r = &record{SchemaVersion: SchemaVersion, Source: id, Epoch: 1}
	} else if e != nil {
		return e
	}
	if e = fn(r); errors.Is(e, errUnchanged) {
		return nil
	} else if e != nil {
		return e
	}
	return s.save(ctx, r)
}

// save writes what an update changed of a record: the record file when its content changed,
// then the status file, naming the record file, when the bookkeeping, the generation or the
// record file did. The record file is written with the generation. While no status file names
// the record (one of the earlier format, or one this update creates), the record file carries
// the bookkeeping too, so that an update interrupted before the status file keeps it; the next
// update writes the record file without it.
func (s *Store) save(ctx context.Context, r *record) error {
	file := *r
	file.bookkeeping = bookkeeping{}
	file.Generation = r.disk.generation
	// Records decode and encode to the same bytes: an update that changed none of the record
	// file's fields matches the file it read.
	same := sha256.New()
	if err := json.NewEncoder(same).Encode(&file); err != nil {
		return err
	}
	sum := r.disk.digest
	if !bytes.Equal(same.Sum(nil), sum) {
		file.Generation = r.Generation
		if r.disk.status == nil {
			file.bookkeeping = r.bookkeeping
		}
		written := sha256.New()
		if err := s.replace(ctx, s.path(r.Source), maxRecord, func(w io.Writer) error { return json.NewEncoder(io.MultiWriter(w, written)).Encode(&file) }); err != nil {
			return err
		}
		sum = written.Sum(nil)
		// The status file belongs with the record file now published.
		ctx = context.WithoutCancel(ctx)
	}
	status, err := json.Marshal(statusFile{r.Source, r.Epoch, r.Generation, hex.EncodeToString(sum), r.bookkeeping})
	if err != nil {
		return err
	}
	status = append(status, '\n')
	if bytes.Equal(status, r.disk.status) {
		return nil
	}
	return s.replace(ctx, s.statusPath(r.Source), maxStatus, func(w io.Writer) error { _, err := w.Write(status); return err })
}

// replace writes a file whole or not at all (atomicfile.Write): a synced temporary file, named
// as sweep expects, renamed over it, then the directory synced, so that a later write never
// reaches flash before it. A cancelled update publishes nothing.
func (s *Store) replace(ctx context.Context, path string, limit int64, write func(io.Writer) error) error {
	err := atomicfile.Write(path, 0600, ".snapshot-*", func(f *os.File) error {
		if err := write(f); err != nil {
			return err
		}
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if st.Size() > limit {
			return errors.New("snapshot exceeds storage limit")
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	replaced(path)
	return nil
}

// replaced learns of each file an update writes, for tests to count them.
var replaced = func(string) {}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, "subscription-"+digest(id)+".json")
}

// statusPath is the record's status file. List reads only record files.
func (s *Store) statusPath(id string) string {
	return filepath.Join(s.dir, "status-"+digest(id)+".json")
}
func (s *Store) raw(id string) (*record, error) {
	return s.decode(s.path(id), nil)
}

func openRecordFile(path string) (*os.File, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > maxRecord {
		f.Close()
		return nil, errors.New("invalid subscription storage")
	}
	return f, nil
}

func (s *Store) decode(path string, checksum io.Writer) (*record, error) {
	f, e := openRecordFile(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	sum := sha256.New()
	var hashed io.Writer = sum
	if checksum != nil {
		hashed = io.MultiWriter(sum, checksum)
	}
	d := json.NewDecoder(io.TeeReader(io.LimitReader(f, maxRecord+1), hashed))
	d.UseNumber()
	var r record
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || (r.SchemaVersion != SchemaVersion && r.SchemaVersion != 1) || !sourceIDValid(r.Source) || s.path(r.Source) != path || r.Epoch == 0 {
		return nil, errors.New("invalid subscription state")
	}
	r.disk.generation, r.disk.digest = r.Generation, sum.Sum(nil)
	if r.SchemaVersion == 1 {
		r.legacy = true
		r.NeedsRefresh = r.Config != nil && !r.Revoked
		r.SchemaVersion = SchemaVersion
		r.Current = nil
		r.Previous = nil
		r.LastChange = nil
		r.LastFailure = ""
		r.Epoch++
	}
	for _, p := range []*Profile{r.Current, r.Previous} {
		if p != nil && (p.SchemaVersion != SchemaVersion || p.Source != r.Source) {
			return nil, errors.New("invalid subscription snapshot")
		}
	}
	if !r.legacy {
		s.readStatus(&r)
	}
	return &r, nil
}

// readStatus reads a record's status file into it when it names the record file, or, of the
// previous version, names none and is no older than the record file. An older one leaves the
// record file's bookkeeping, or gives what it still tells of the source when the record file has
// none (record). A status file that is missing or unreadable leaves the record file's own
// fields; the next update writes it again.
func (s *Store) readStatus(r *record) {
	f, err := os.Open(s.statusPath(r.Source))
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() > maxStatus {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxStatus+1))
	if err != nil || len(raw) > maxStatus {
		return
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v statusFile
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || v.Source != r.Source {
		return
	}
	if v.Record == hex.EncodeToString(r.disk.digest) || v.Record == "" && v.Generation >= r.Generation {
		r.Generation, r.bookkeeping, r.disk.status = v.Generation, v.bookkeeping, raw
	} else if v.Epoch == r.Epoch && r.bookkeeping == (bookkeeping{}) {
		// The generation stays the record file's, so that a reader that found the record file
		// before a commit and the commit's status file holds a ticket the commit superseded.
		r.bookkeeping = v.older()
	}
}
func (s *Store) Begin(id string) (Ticket, error) {
	t, _, err := s.begin(context.Background(), id, 0, false)
	return t, err
}

// sweep removes the temporary files of record writes that a killed helper left. Every write
// holds state.lock, so under it none is in use.
func (s *Store) sweep() {
	lock, e := os.OpenFile(filepath.Join(s.dir, "state.lock"), os.O_RDWR, 0600)
	if e != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entries, _ := os.ReadDir(s.dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".snapshot-") {
			os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

func (s *Store) begin(ctx context.Context, id string, retryGeneration uint64, scheduled bool) (Ticket, *record, error) {
	s.sweep()
	var t Ticket
	var snapshot *record
	e := s.updateContext(ctx, id, func(r *record) error {
		if retryGeneration != 0 && (r.ClockRetryGeneration != retryGeneration || r.Generation != retryGeneration || !r.LastTLS.Recheck(time.Now())) {
			return ErrSuperseded
		}
		if r.Revoked {
			return ErrRevoked
		}
		if r.LoginRequired {
			return ErrLoginRequired
		}
		r.Generation++
		// An administrator's refresh starts the retry sequence over.
		if !scheduled {
			r.Attempts = 0
		}
		r.Attempts++
		r.AttemptedAt = time.Now().UTC().Format(time.RFC3339)
		t = Ticket{id, r.Epoch, r.Generation}
		snapshot = r
		return nil
	})
	return t, snapshot, e
}
func check(r *record, t Ticket) error {
	if r.Revoked || r.Epoch != t.Epoch {
		return ErrRevoked
	}
	if r.Generation != t.Generation {
		return ErrSuperseded
	}
	return nil
}
func (s *Store) Commit(t Ticket, c Candidate) error {
	// Only Refresh knows whether a profile matched this particular ticket.
	c.unchanged = false
	return s.commit(context.Background(), t, c, nil)
}

// commit makes a ready candidate the source's profile.
func (s *Store) commit(ctx context.Context, t Ticket, c Candidate, recheck func() error) error {
	if c.Decision != "ready" || c.Profile.Source != t.Source || c.Profile.SchemaVersion != SchemaVersion {
		return errors.New("invalid candidate")
	}
	return s.updateContext(ctx, t.Source, func(r *record) error {
		if e := check(r, t); e != nil {
			return e
		}
		if recheck != nil {
			if e := recheck(); e != nil {
				return e
			}
		}
		if c.unchanged && r.Current != nil {
			// The profile routes as the current one (Profile.routing), which the router runs: it
			// takes only what the profile shows, and the rollback snapshot stays. Usually nothing
			// of the record file changes.
			shown := *r.Current
			shown.Notices, shown.Title, shown.Format = c.Profile.Notices, c.Profile.Title, c.Profile.Format
			r.Current = &shown
		} else {
			// A suspended plan that resumes routing as before tells the router again, but its
			// profile only repeats the current one: the rollback snapshot stays.
			if !r.Suspended || r.Current == nil || digest(c.Profile.routing()) != digest(r.Current.routing()) {
				r.Previous = r.Current
			}
			r.Current = &c.Profile
		}
		r.NeedsRefresh = false
		r.Usage = c.Usage
		r.LastFailure = ""
		r.LastTLS = nil
		r.ClockRetryGeneration = 0
		r.LastChange = &ChangeCounts{len(c.Changes.Added), len(c.Changes.Removed), len(c.Changes.Groups)}
		r.PendingUpdate = nil
		r.Suspended = false
		r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		r.Attempts, r.AttemptedAt = 0, ""
		r.Generation++
		return nil
	})
}
func (s *Store) Load(id string) (Profile, error) {
	return currentProfile(s.read(id))
}

func currentProfile(r *record, e error) (Profile, error) {
	if errors.Is(e, os.ErrNotExist) {
		return Profile{}, ErrNotReady
	}
	if e != nil {
		return Profile{}, e
	}
	if r.Revoked {
		return Profile{}, ErrRevoked
	}
	if r.Suspended {
		return Profile{}, ErrSuspended
	}
	if r.Current == nil {
		return Profile{}, ErrNotReady
	}
	return *r.Current, nil
}
func (s *Store) Rollback(id string) error { return s.RollbackChecked(context.Background(), id, nil) }
func (s *Store) RollbackChecked(ctx context.Context, id string, v Validator) error {
	var t Ticket
	var recheck func() error
	var validated string
	if v != nil {
		r, e := s.read(id)
		if e != nil {
			return e
		}
		if r.Revoked {
			return ErrRevoked
		}
		if r.Suspended {
			return ErrSuspended
		}
		if r.Previous == nil {
			return ErrNotReady
		}
		t = Ticket{id, r.Epoch, r.Generation}
		validated = digest(*r.Previous)
		recheck, e = v(ctx, *r.Previous)
		if e != nil {
			return e
		}
		if recheck == nil {
			return errors.New("preflight_revision_missing")
		}
	}

	return s.update(id, func(r *record) error {
		if r.Revoked {
			return ErrRevoked
		}
		if r.Suspended {
			return ErrSuspended
		}
		if r.Previous == nil {
			return ErrNotReady
		}
		if recheck != nil {
			if err := check(r, t); err != nil {
				return err
			}
			// The snapshot restored is the one validated, whatever the read without the lock
			// found of a record a commit was replacing.
			if digest(*r.Previous) != validated {
				return ErrSuperseded
			}
			if err := recheck(); err != nil {
				return err
			}
		}
		r.Generation++
		r.Current, r.Previous = r.Previous, r.Current
		return nil
	})
}
func (s *Store) Revoke(id string) error {
	return s.update(id, func(r *record) error {
		r.Epoch++
		r.Generation++
		r.Revoked = true
		r.Config = nil
		r.Usage = nil
		r.LastFailure = ""
		r.LastTLS = nil
		r.ClockRetryGeneration = 0
		r.LastChange = nil
		r.PendingUpdate = nil
		r.LoginRequired = false
		r.Current = nil
		r.Previous = nil
		return nil
	})
}
func (s *Store) RequireLogin(t Ticket) error {
	return s.update(t.Source, func(r *record) error {
		if e := check(r, t); e != nil {
			return e
		}
		r.Generation++
		r.LoginRequired = true
		return nil
	})
}

func (s *Store) Current(t Ticket) bool {
	r, e := s.read(t.Source)
	return e == nil && check(r, t) == nil
}

// Configure creates a source or updates its settings. Only a fixture source can be configured
// this way, and only in tests (FixtureSource): the Hyper source is the account's
// (ConfigureHyper).
func (s *Store) Configure(src Source) error {
	if src.Kind != FixtureSource {
		return errors.New("invalid_source_kind")
	}
	return s.configure(src, false)
}

// configure saves a source's settings. A verified sign-in (authenticated) renews a source a
// sign-out revoked; it ends the need to sign in again too.
func (s *Store) configure(src Source, authenticated bool) error {
	if e := src.validate(); e != nil {
		return e
	}
	return s.update(src.ID, func(r *record) error {
		if r.Config != nil && r.Config.Kind != src.Kind {
			return errors.New("source_identity_mismatch")
		}
		if r.Revoked {
			r.Epoch++
			r.Revoked = false
			r.LoginRequired = false
		}
		if authenticated {
			r.LoginRequired = false
		}
		r.Generation++
		r.Config = &src
		// A changed source deserves a prompt retry.
		r.Attempts, r.AttemptedAt = 0, ""
		return nil
	})
}

type ChangeCounts struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Groups  int `json:"affected_groups"`
}
type Status struct {
	NeedsRefresh    bool                `json:"needs_refresh,omitempty"`
	LastTLS         *tlsdiag.Diagnostic `json:"last_tls,omitempty"`
	RetryAfterClock bool                `json:"-"`
	Attempts        int                 `json:"-"`
	AttemptedAt     string              `json:"-"`
	Usage           *Usage              `json:"usage,omitempty"`
	LastFailure     string              `json:"last_failure,omitempty"`
	LastChange      *ChangeCounts       `json:"last_change,omitempty"`
	PendingUpdate   *PendingUpdate      `json:"pending_update,omitempty"`
	HasHistory      bool                `json:"has_history,omitempty"`
	UpdatedAt       string              `json:"updated_at,omitempty"`
	Source          string              `json:"source"`
	Name            string              `json:"name,omitempty"`
	Kind            string              `json:"kind,omitempty"`
	Epoch           uint64              `json:"epoch"`
	Generation      uint64              `json:"generation"`
	Revoked         bool                `json:"revoked"`
	Suspended       bool                `json:"suspended"`
	LoginRequired   bool                `json:"login_required"`
	Ready           bool                `json:"ready"`
	Format          string              `json:"format,omitempty"`
	Local           bool                `json:"local"`
	NodeCount       int                 `json:"node_count"`
	GroupCount      int                 `json:"group_count"`
	RuleCount       int                 `json:"rule_count"`
	Notices         []Notice            `json:"notices,omitempty"`
}

func statusOf(r *record) Status {
	st := Status{NeedsRefresh: r.NeedsRefresh, LastTLS: r.LastTLS, RetryAfterClock: r.ClockRetryGeneration != 0 && r.ClockRetryGeneration == r.Generation, Attempts: r.Attempts, AttemptedAt: r.AttemptedAt, LastFailure: r.LastFailure, LastChange: r.LastChange, PendingUpdate: r.PendingUpdate, UpdatedAt: r.UpdatedAt, Source: r.Source, Epoch: r.Epoch, Generation: r.Generation, Suspended: r.Suspended, Revoked: r.Revoked, LoginRequired: r.LoginRequired, Ready: r.Current != nil && !r.Revoked && !r.Suspended}
	if r.Config != nil {
		st.Name = publicLabel(r.Config.Name, "Subscription")
		st.Kind = r.Config.Kind
		if !r.Revoked {
			st.Usage = r.Usage
		}
		st.HasHistory = r.Previous != nil && !r.Suspended && !r.Revoked
	}
	if st.Ready {
		p := r.Current
		st.Format = p.Format
		st.Local = p.Local
		st.NodeCount = len(p.Nodes)
		st.GroupCount = len(p.Groups)
		st.RuleCount = len(p.Rules)
		st.Notices = p.Notices
	}
	return st
}

func (s *Store) Status(id string) (Status, error) {
	r, e := s.read(id)
	if e != nil {
		return Status{}, e
	}
	return statusOf(r), nil
}
func (s *Store) List() ([]Status, error) {
	entries, e := os.ReadDir(s.dir)
	out := []Status{}
	if errors.Is(e, os.ErrNotExist) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	for _, file := range entries {
		if !strings.HasPrefix(file.Name(), "subscription-") || filepath.Ext(file.Name()) != ".json" || !file.Type().IsRegular() {
			continue
		}
		r, e := s.readPath(filepath.Join(s.dir, file.Name()), nil)
		if e != nil {
			return nil, e
		}
		if r.legacy {
			// Re-read under the writer lock. A concurrent refresh may already have migrated it.
			if e := s.update(r.Source, func(*record) error { return nil }); e != nil {
				return nil, e
			}
			r, e = s.read(r.Source)
			if e != nil {
				return nil, e
			}
		}
		out = append(out, statusOf(r))
	}
	return out, nil
}

type PublicGroup struct {
	Hidden  bool     `json:"hidden,omitempty"`
	Default string   `json:"default,omitempty"`
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Members []string `json:"members"`
}
type PublicProfile struct {
	Source  string        `json:"source"`
	Format  string        `json:"format"`
	Local   bool          `json:"local"`
	Nodes   []Member      `json:"nodes"`
	Groups  []PublicGroup `json:"groups"`
	Rules   []Rule        `json:"rules"`
	Final   string        `json:"final,omitempty"`
	Notices []Notice      `json:"notices,omitempty"`
}

func (p Profile) Summary() PublicProfile {
	s := PublicProfile{Source: p.Source, Format: p.Format, Local: p.Local, Final: p.Final, Rules: p.Rules, Notices: p.Notices, Nodes: []Member{}, Groups: []PublicGroup{}}
	for _, n := range p.Nodes {
		s.Nodes = append(s.Nodes, n.Summary())
	}
	for _, g := range p.Groups {
		member, _ := g.Options["default"].(string)
		if !slices.Contains(g.Members, member) {
			member = ""
		}
		s.Groups = append(s.Groups, PublicGroup{Hidden: g.Hidden, ID: g.ID, Name: publicLabel(g.Name, "Policy group"), Kind: g.Kind, Members: g.Members, Default: member})
	}
	return s
}

func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func (s *Store) read(id string) (*record, error) {
	return s.readWithChecksum(id, nil)
}

func (s *Store) readWithChecksum(id string, checksum io.Writer) (*record, error) {
	return s.readPath(s.path(id), checksum)
}

func (s *Store) readPath(path string, checksum io.Writer) (*record, error) {
	return s.decode(path, checksum)
}

func (s *Store) Suspend(t Ticket) error {
	return s.update(t.Source, func(r *record) error {
		if e := check(r, t); e != nil {
			return e
		}
		r.Generation++
		r.Suspended = true
		return nil
	})
}
