package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRoutineTasksStayOutOfTheLog(t *testing.T) {
	failed, cancelled := errors.New("core stopped"), fmt.Errorf("probe: %w", context.Canceled)
	for _, tc := range []struct {
		action string
		err    error
		quiet  bool
	}{
		{"latency/url", nil, true}, {"latency/tcp", cancelled, true}, {"latency/url", failed, false},
		{"health_check", nil, true}, {"configure/mode", nil, true}, {"configure/mode", failed, false},
		{"subscription_write/refresh", nil, false}, {"kernel/install", cancelled, false},
		// The account pages read the service at every view; a failed read is news.
		{"subscription_write/api", nil, true}, {"subscription_write/api", failed, false},
	} {
		if quietTask(tc.action, tc.err) != tc.quiet {
			t.Error(tc.action, tc.err)
		}
	}
}
