package subscription

import (
	"slices"
	"strings"
)

// Codes of a PendingUpdate.
const (
	PendingConfirmation = "confirmation_required"
	PendingBlocked      = "update_blocked"
)

// maxPendingNotices bounds the notice codes a PendingUpdate keeps on the record.
const maxPendingNotices = 8

// PendingUpdate is an update a scheduled refresh downloaded but could not apply by itself:
// its changes need the administrator's confirmation, or the router cannot use them. The
// source keeps its current profile and the scheduler's back-off until a refresh commits or
// the administrator reviews the update by refreshing it themselves.
type PendingUpdate struct {
	// Code is PendingConfirmation or PendingBlocked.
	Code string `json:"code"`
	// Notices are the update's notice codes, dangerous ones first. The notices' labels come
	// from the subscription and stay out of the record.
	Notices []string `json:"notices,omitempty"`
}

// Pending describes a candidate the router did not apply, or returns nil for a ready one.
func (c Candidate) Pending() *PendingUpdate {
	if c.Decision == "" || c.Decision == "ready" {
		return nil
	}
	p := &PendingUpdate{Code: PendingBlocked}
	if c.Decision == "confirmation_required" {
		p.Code = PendingConfirmation
	}
	for _, dangerous := range []bool{true, false} {
		for _, n := range c.Notices {
			if n.Dangerous == dangerous && len(p.Notices) < maxPendingNotices && plainCode(n.Code) && !slices.Contains(p.Notices, n.Code) {
				p.Notices = append(p.Notices, n.Code)
			}
		}
	}
	return p
}

func plainCode(code string) bool {
	return code != "" && len(code) <= 64 && strings.Trim(code, "abcdefghijklmnopqrstuvwxyz_0123456789") == ""
}
