package subscription

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	rejectingFeed = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['DOMAIN,ads.example,REJECT', 'MATCH,DIRECT']"
	// The same subscription without its REJECT rule: applying it needs the administrator.
	permissiveFeed = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['DOMAIN,media.example,DIRECT', 'MATCH,DIRECT']"
)

// A scheduled refresh whose update the router cannot apply by itself keeps it on the source
// instead of passing for done, and keeps counting attempts so that the scheduler's back-off
// goes on. A commit or the administrator's own refresh settles it.
func TestScheduledRefreshKeepsUpdateItCannotApply(t *testing.T) {
	store, fd := fixtureSource(t, "sub", rejectingFeed)
	src := Source{ID: "sub"}
	var afterBegin os.FileInfo
	fd.onRead = func(context.Context) { afterBegin, _ = os.Stat(store.statusPath(src.ID)) }
	f := Fetcher{}
	refresh := func(policy Policy, decision string) (Candidate, Status) {
		t.Helper()
		c, err := f.Refresh(t.Context(), store, src.ID, policy)
		if (err == nil) != fd.reachable || c.Decision != decision {
			t.Fatal(err, c.Decision, c.Notices)
		}
		st, err := store.Status(src.ID)
		if err != nil {
			t.Fatal(err)
		}
		return c, st
	}
	refresh(Policy{}, "ready")
	fd.reachable = false
	if _, st := refresh(Policy{Scheduled: true}, ""); st.LastFailure != "locations_unreachable" || st.Attempts != 1 {
		t.Fatal(st.LastFailure, st.Attempts)
	}

	fd.reachable, fd.body = true, permissiveFeed
	c, st := refresh(Policy{Scheduled: true}, "confirmation_required")
	if p := st.PendingUpdate; p == nil || p.Code != PendingConfirmation || !slices.Equal(p.Notices, []string{"rejection_removed"}) {
		t.Fatal("scheduled refresh left no pending update", p)
	}
	// The download worked: the earlier failure is over, and the back-off is not.
	if st.LastFailure != "" || st.Attempts != 2 || st.AttemptedAt == "" || !st.Ready {
		t.Fatal(st.LastFailure, st.Attempts, st.Ready)
	}
	if current, _ := store.Load(src.ID); len(current.Rules) != 1 || current.Rules[0].Target != Reject {
		t.Fatal("pending update replaced the current profile")
	}
	_, again := refresh(Policy{Scheduled: true}, "confirmation_required")
	if now, _ := os.Stat(store.statusPath(src.ID)); again.Attempts != 3 || !os.SameFile(afterBegin, now) || again.PendingUpdate.Code != PendingConfirmation {
		t.Fatal("the same pending update rewrote the status", again.Attempts)
	}

	// The administrator's refresh showed the update to them. Dismissed, it is no longer pending.
	if _, st := refresh(Policy{}, "confirmation_required"); st.PendingUpdate != nil || st.Attempts != 1 {
		t.Fatal("reviewed update still pending", st.PendingUpdate)
	}
	refresh(Policy{Scheduled: true}, "confirmation_required")
	// Applied, it is part of the current profile.
	if _, st := refresh(Policy{Confirm: c.ConfirmationID}, "ready"); st.PendingUpdate != nil || st.Attempts != 0 {
		t.Fatal("applied update still pending", st.PendingUpdate)
	}

	// An update that would drop a member in use is blocked; the next commit clears it.
	if _, st := refresh(Policy{Scheduled: true, ActiveIDs: []string{"group:gone"}}, "blocked"); st.PendingUpdate == nil ||
		st.PendingUpdate.Code != PendingBlocked || !slices.Contains(st.PendingUpdate.Notices, "active_target_missing") || st.LastFailure != "" {
		t.Fatal("blocked update not pending", st.PendingUpdate, st.LastFailure)
	}
	if _, st := refresh(Policy{Scheduled: true}, "ready"); st.PendingUpdate != nil || st.Attempts != 0 {
		t.Fatal("committed refresh left the pending update", st.PendingUpdate)
	}
}

// Only notice codes reach the record, dangerous ones first and each once.
func TestPendingUpdateKeepsNoticeCodes(t *testing.T) {
	if (Candidate{Decision: "ready"}).Pending() != nil {
		t.Fatal("ready candidate pending")
	}
	c := Candidate{Decision: "confirmation_required", Notices: []Notice{
		{Code: "mihomo_unsupported_rule", At: "https://feed.example/?token=secret"},
		{Code: "rejection_removed", Dangerous: true},
		{Code: "mihomo_unsupported_rule", At: "rule 2"},
		{Code: "Not A Code"},
	}}
	if p := c.Pending(); p.Code != PendingConfirmation || !slices.Equal(p.Notices, []string{"rejection_removed", "mihomo_unsupported_rule"}) {
		t.Fatal(p)
	}
	for range 20 {
		c.Notices = append(c.Notices, Notice{Code: "code_" + string(rune('a'+len(c.Notices)))})
	}
	if p := c.Pending(); len(p.Notices) != maxPendingNotices {
		t.Fatal(len(p.Notices))
	}
}

// A subscription whose every download needs confirmation, as one with a REJECT rule the router
// cannot carry out, is confirmed once. Its scheduled refreshes of the same content then find it
// unchanged: nothing pending, the update date advancing and the back-off over, so the scheduler
// keeps the update interval. Content that routes otherwise still waits for the administrator,
// and an update blocked by a target the router refers to stays blocked.
func TestConfirmedUpdateIsNotHeldAgain(t *testing.T) {
	const proxies = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\n"
	const routes = "'DOMAIN,a.example,Edge', 'DOMAIN,b.example,Edge', 'DOMAIN,c.example,Edge', 'DOMAIN,d.example,Edge', 'DOMAIN,e.example,Edge', 'DOMAIN,f.example,Edge', 'DOMAIN,g.example,Edge', 'DOMAIN,h.example,Edge', 'MATCH,DIRECT'"
	for name, feed := range map[string]string{
		"script rule":      proxies + "rules: ['SCRIPT,quic,REJECT', " + routes + "]",
		"sub-rule":         proxies + "rules: ['SUB-RULE,(NETWORK,tcp),REJECT', " + routes + "]",
		"missing rule set": proxies + "rules: ['RULE-SET,missing,REJECT', " + routes + "]",
		"process name":     proxies + "rules: ['PROCESS-NAME,ads,REJECT', " + routes + "]",
		"local routing":    proxies + "proxy-groups: [{name: Chain, type: relay, proxies: [Edge]}]\nrules: [" + routes + "]",
	} {
		t.Run(name, func(t *testing.T) {
			store, fd := fixtureSource(t, "sub", feed)
			src := Source{ID: "sub"}
			f := Fetcher{}
			c, err := f.Refresh(t.Context(), store, src.ID, Policy{})
			if err != nil || c.Decision != "confirmation_required" {
				t.Fatal(err, c.Decision, c.Notices)
			}
			if c, err = f.Refresh(t.Context(), store, src.ID, Policy{Confirm: c.ConfirmationID}); err != nil || c.Decision != "ready" || c.Unchanged() {
				t.Fatal(err, c.Decision)
			}
			confirmed, err := store.Load(src.ID)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				// Long since updated, and held by a scheduled refresh before this one.
				if err := store.update(src.ID, func(r *record) error {
					r.UpdatedAt, r.PendingUpdate = "2000-01-01T00:00:00Z", &PendingUpdate{Code: PendingConfirmation}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				c, err := f.Refresh(t.Context(), store, src.ID, Policy{Scheduled: true})
				if err != nil || c.Decision != "ready" || !c.Unchanged() || len(c.Notices) == 0 {
					t.Fatal(err, c.Decision, c.Unchanged(), c.Notices)
				}
				st, err := store.Status(src.ID)
				if err != nil || st.PendingUpdate != nil || st.UpdatedAt == "2000-01-01T00:00:00Z" || st.Attempts != 0 || st.AttemptedAt != "" || st.LastFailure != "" || st.HasHistory {
					t.Fatalf("%+v %v", st, err)
				}
				if current, _ := store.Load(src.ID); digest(current.routing()) != digest(confirmed.routing()) {
					t.Fatal("the confirmed routing changed")
				}
			}

			if _, st := scheduledRefresh(t, f, store, src.ID, Policy{Scheduled: true, ActiveIDs: []string{"node:gone"}}, "blocked"); st.PendingUpdate == nil || st.PendingUpdate.Code != PendingBlocked {
				t.Fatal("identical update with a missing target not blocked", st.PendingUpdate)
			}
			fd.body = strings.Replace(feed, "edge.example", "edge2.example", 1)
			if _, st := scheduledRefresh(t, f, store, src.ID, Policy{Scheduled: true}, "confirmation_required"); st.PendingUpdate == nil || st.PendingUpdate.Code != PendingConfirmation || st.Attempts != 2 {
				t.Fatal("changed update not held", st.PendingUpdate, st.Attempts)
			}
		})
	}
}

func scheduledRefresh(t *testing.T, f Fetcher, store *Store, id string, policy Policy, decision string) (Candidate, Status) {
	t.Helper()
	c, err := f.Refresh(t.Context(), store, id, policy)
	if err != nil || c.Decision != decision {
		t.Fatal(err, c.Decision, c.Notices)
	}
	st, err := store.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	return c, st
}
