package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
)

func TestKernelJobRequiresConfirmedHealthOrVerifiedFallback(t *testing.T) {
	const next = "v1.19.34"
	const old = "v1.19.33"
	for _, tc := range []struct {
		name     string
		statuses []kernel.Status
		expect   string
	}{
		{"confirmed", []kernel.Status{{Selected: next, Active: old, Pending: true}, {Selected: next, Active: next}}, ""},
		{"restored", []kernel.Status{{Selected: next, Active: old}, {Selected: old, Active: old}}, "previous kernel v1.19.33 is running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			i := 0
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			err := waitForKernel(ctx, next, old, func() (string, string, bool) { return "running", "", true }, func(context.Context) (kernel.Status, error) {
				if i >= len(tc.statuses) {
					t.Fatal("ignored final state")
				}
				s := tc.statuses[i]
				i++
				return s, nil
			})
			if i != len(tc.statuses) || (tc.expect == "" && err != nil) || (tc.expect != "" && (err == nil || !strings.Contains(err.Error(), tc.expect))) {
				t.Fatal("wrong task outcome", i, err)
			}
		})
	}
}

func TestKernelRequestOwnsVerification(t *testing.T) {
	for _, key := range []string{"sha256", "cache", "bundle", "capabilities", "manifest", "stage_only"} {
		args := map[string]json.RawMessage{"tag": json.RawMessage(`"v1.19.34"`), key: json.RawMessage(`"custom"`)}
		if err := checkKernelArgs("install-json", args); err == nil {
			t.Errorf("LuCI accepted caller-defined %s", key)
		}
	}
	args := map[string]json.RawMessage{"tag": json.RawMessage(`"v1.19.34"`), "mirror": json.RawMessage(`"https://example.com"`)}
	if err := checkKernelArgs("install-json", args); err != nil {
		t.Fatal(err)
	}
	if err := checkKernelArgs("rollback", args); err == nil {
		t.Fatal("rollback accepted an installation source")
	}
}

func TestKernelErrorDoesNotExposeSource(t *testing.T) {
	for input, want := range map[string]string{
		"release file size or SHA-256 mismatch\n":                                "release checksum mismatch",
		"cannot verify original release via GitHub: https://example.com/private": "upstream release checksum unavailable",
		"open /tmp/private-credential: permission denied":                        "kernel operation failed",
	} {
		if got := publicKernelError(input).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
