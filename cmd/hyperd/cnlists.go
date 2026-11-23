package main

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
)

// keepChinaLists checks the mainland China lists about once a week, and a few minutes after a
// start when the copies in use are older than that (cnlist.Due). Routine checks stay out of
// the log; only a third failure in a row is recorded.
func (s *app) keepChinaLists(ctx context.Context) {
	u := cnlist.NewUpdater()
	var checked, attempt time.Time
	failures, ranges := 0, false
	// The clock and the core settle first after boot.
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(10 * time.Minute)
		now := time.Now()
		if !cnlist.Due(now, cnlist.Updated(), checked, attempt, failures) {
			continue
		}
		attempt = now
		paths := [2]string{cnlist.Path(cnlist.Domains), cnlist.Path(cnlist.Addresses)}
		changed, err := u.Update(ctx)
		if ctx.Err() != nil {
			return
		}
		// Mihomo re-reads a file provider replaced in place; one that moves between the packaged
		// copy and the download needs the configuration rebuilt.
		if paths != [2]string{cnlist.Path(cnlist.Domains), cnlist.Path(cnlist.Addresses)} {
			s.m.Reapply()
		}
		ranges = ranges || slices.Contains(changed, cnlist.Addresses)
		if ranges {
			e := s.sys.RefreshChinaRanges(ctx)
			ranges, err = e != nil, errors.Join(err, e)
		}
		if err != nil {
			if failures++; failures == 3 {
				s.sys.Records().Log("task", "warn", "china_lists_update_failed")
			}
			continue
		}
		checked, failures = now, 0
	}
}
