package subscription

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

// Source is a subscription the router reads: the Hyper account's (hyper.go), the only kind a
// router has, or in tests a fixture (FixtureSource).
type Source struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Disabled names the Hyper catalog's lists the administrator turned off (hyper sources).
	Disabled []string `json:"disabled,omitempty"`
}

// packageVersion is set when the package is built (-X).
var packageVersion = "dev"

// PackageVersion is the plugin version this build was packaged as.
func PackageVersion() string { return packageVersion }

func webURL(s string, secure bool) (*url.URL, error) {
	u, e := url.Parse(s)
	if e != nil || len(s) > 8192 || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && (secure || u.Scheme != "http")) {
		return nil, errors.New("invalid_source_url")
	}
	return u, nil
}
func sourceIDValid(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == ':' || c == '.') {
			return false
		}
	}
	return true
}
func (s Source) validate() error {
	if !sourceIDValid(s.ID) || !safeLabel(s.ID) || !safeLabel(s.Name) {
		return errors.New("invalid_source")
	}
	switch s.Kind {
	case HyperSource:
		return s.validateHyper()
	case FixtureSource:
		if Fixture == nil || len(s.Disabled) > 0 {
			return errors.New("invalid_source_kind")
		}
		return nil
	default:
		return errors.New("invalid_source_kind")
	}
}

// FixtureSource is a kind of source only tests have: its profile is the Clash document, or the
// share links, that Fixture serves for it, read as Parse reads them.
const FixtureSource = "fixture"

// Fixture serves the documents of fixture sources by source ID. Only tests set it; while it is
// nil no source of that kind can be configured or read.
var Fixture func(ctx context.Context, id string) ([]byte, error)

type Validator func(context.Context, Profile) (func() error, error)
type Fetcher struct {
	previous     *record
	Client       *http.Client
	BeforeCommit Validator
	// Fallback lets the account's requests take the router's other way out (fallback.go).
	Fallback bool
}

func (f Fetcher) readProfile(ctx context.Context, s Source, usage **Usage) (Profile, error) {
	switch {
	case s.Kind == HyperSource:
		return f.readHyper(ctx, s, usage)
	case s.Kind == FixtureSource && Fixture != nil:
		body, err := Fixture(ctx, s.ID)
		if err != nil {
			return Profile{}, err
		}
		return Parse(s.ID, body)
	}
	return Profile{}, errors.New("invalid_source_kind")
}

func (f Fetcher) Refresh(ctx context.Context, store *Store, id string, policy Policy) (candidate Candidate, failure error) {
	t, r, e := store.begin(ctx, id, policy.ClockRetryGeneration, policy.Scheduled)
	if e != nil {
		return Candidate{}, e
	}
	// Keep only a stable diagnostic code. A newer refresh, logout or account
	// change always wins and a transport error cannot overwrite its history.
	defer func() {
		var reason string
		// The subscription was read but its update not applied, which ends an earlier failure.
		// A scheduled refresh leaves the update on the source for the administrator; one they
		// ran themselves has shown it to them.
		reviewed := failure == nil && candidate.Decision != "ready"
		switch {
		case errors.Is(ctx.Err(), context.Canceled), errors.Is(failure, context.Canceled), errors.Is(failure, ErrSuperseded), errors.Is(failure, ErrRevoked):
			return
		case failure != nil:
			reason = failure.Error()
		case !reviewed:
			return
		case candidate.Decision == "blocked":
			reason = candidate.Profile.credentialIssue()
		}
		if len(reason) > 60 || (reason == "" && failure != nil) || strings.Trim(reason, "abcdefghijklmnopqrstuvwxyz_0123456789") != "" {
			reason = "update_failed"
		}
		diagnosticCtx := ctx
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			diagnosticCtx = context.WithoutCancel(ctx)
		}
		_ = store.updateContext(diagnosticCtx, id, func(r *record) error {
			if e := check(r, t); e != nil {
				return e
			}
			before := failureDigest(r)
			r.LastFailure = reason
			r.LastTLS = tlsdiag.From(failure)
			r.ClockRetryGeneration = 0
			if r.LastTLS != nil && r.Config != nil && policy.Confirm == "" {
				r.ClockRetryGeneration = t.Generation
			}
			if reviewed {
				r.PendingUpdate = nil
				if policy.Scheduled {
					r.PendingUpdate = candidate.Pending()
				}
			}
			// A retry that ends the same way leaves the record as it was.
			if failureDigest(r) == before {
				return errUnchanged
			}
			return nil
		})
	}()
	if r.Config == nil {
		return Candidate{}, ErrNotReady
	}
	src := *r.Config
	var usage *Usage
	f.previous = r
	p, e := f.readProfile(ctx, src, &usage)
	if err := ctx.Err(); err != nil && (e == nil || errors.Is(err, context.Canceled)) {
		return Candidate{}, err
	}
	if e != nil && !(errors.Is(e, errNoUsableNodes) && p.credentialIssue() != "") {
		if errors.Is(e, ErrSuspended) {
			if err := store.Suspend(t); err != nil {
				return Candidate{}, err
			}
		}
		if errors.Is(e, ErrLoginRequired) {
			if err := store.RequireLogin(t); err != nil {
				return Candidate{}, err
			}
		}
		return Candidate{}, e
	}
	c := Assess(p, r.Current, policy)
	if c.Decision == "confirmation_required" && r.Current != nil {
		// An update that needs confirmation yet routes as the current profile (Profile.routing)
		// asks nothing new: its notices repeat those of the routing the router already runs,
		// which the administrator confirmed, or which needed no confirmation then. It goes on as
		// confirmed, so that a subscription whose every download drops a rule the router cannot
		// carry out, a REJECT one say, is found unchanged by its scheduled refreshes instead of
		// held for the administrator again each time. The profile compared is the one the
		// confirmation would commit: one falling back to local routing is committed without its
		// rules. A blocked update stays blocked even then, as nobody confirms it: credential
		// trouble blocks only an update that loses nodes, which cannot route the same, and an
		// active target it lacks (Policy) is a reference of the router's that the current
		// profile lacks too, which the administrator has to change.
		confirmed := policy
		confirmed.Confirm = c.ConfirmationID
		// Assess appends to the notices: keep its second reading's apart from the first's.
		p.Notices = slices.Clip(p.Notices)
		if same := Assess(p, r.Current, confirmed); same.Decision == "ready" && digest(same.Profile.routing()) == digest(r.Current.routing()) {
			c = same
		}
	}
	c.Usage = usage
	if c.Decision != "ready" {
		if !store.Current(t) {
			return Candidate{}, ErrSuperseded
		}
		return c, nil
	}
	// A profile that routes as the usable one (Profile.routing) leaves the router as it is: it
	// needs no check, keeps the rollback snapshot and tells nobody (Unchanged), and the commit
	// writes only the source's status. Hash the serialized form because stored rule values use
	// json.Number.
	c.unchanged = r.Current != nil && !r.Suspended && digest(c.Profile.routing()) == digest(r.Current.routing())
	var confirm func() error
	if !c.unchanged && f.BeforeCommit != nil {
		if !store.Current(t) {
			return Candidate{}, ErrSuperseded
		}
		confirm, e = f.BeforeCommit(ctx, c.Profile)
		if e != nil {
			return Candidate{}, e
		}
		if confirm == nil {
			return Candidate{}, errors.New("preflight_revision_missing")
		}
	}
	e = store.commit(ctx, t, c, confirm)
	if errors.Is(e, ErrRevoked) {
		// Another operation withdrew or replaced the source during this download.
		// It already owns the notification for that change.
		return Candidate{}, ErrSuperseded
	}
	if e != nil {
		return Candidate{}, e
	}
	return c, nil
}
