// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// coreFiles is the core's open-file limit, soft and hard, as OpenClash's procd instance has
// it (limits nofile="1000000 1000000"); the init script asks procd for the same for serve.
// Every proxied connection holds at least two descriptors, and procd's default is 1024
// soft, 4096 hard.
const coreFiles = 1000000

// setFileLimit makes n this process's open-file limit, soft and hard, before serve starts a
// core. Go raised its own soft limit at startup and puts the original soft limit back in
// each process it starts, unless the program sets the limit itself, as here: from now on
// the core inherits it. The kernel refuses a hard limit above fs.nr_open, even to root, and
// procd's setrlimit then failed as well, so the limit becomes n capped at fs.nr_open, the
// most root may hold, which is no fault. Should that fail too (raising a hard limit takes
// CAP_SYS_RESOURCE), the soft limit is raised to the hard one, and the error tells that
// the core holds less than it could.
func setFileLimit(n uint64) error {
	err := setFiles(n)
	if err == nil {
		return nil
	}
	nrOpen, _ := os.ReadFile("/proc/sys/fs/nr_open")
	var held syscall.Rlimit
	_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &held)
	for _, v := range fileLimitFallbacks(n, nrOpen, held.Max) {
		if setFiles(v) == nil {
			if ceiling, e := strconv.ParseUint(strings.TrimSpace(string(nrOpen)), 10, 64); e == nil && v == ceiling {
				return nil
			}
			break
		}
	}
	return err
}

func setFiles(n uint64) error {
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: n, Max: n})
}

// fileLimitFallbacks lists, in order, the limits (soft and hard alike) to try once the
// kernel refused n: fs.nr_open (nrOpen, the file's contents), then hard, the hard limit
// already held. Neither is tried unless it is below n, nor twice, nor when unknown (an
// unreadable nr_open, or a hard limit of 0, which would leave no descriptor at all).
func fileLimitFallbacks(n uint64, nrOpen []byte, hard uint64) []uint64 {
	var limits []uint64
	if v, err := strconv.ParseUint(strings.TrimSpace(string(nrOpen)), 10, 64); err == nil && v > 0 && v < n {
		limits = append(limits, v)
	}
	if hard > 0 && hard < n && (len(limits) == 0 || limits[0] != hard) {
		limits = append(limits, hard)
	}
	return limits
}
