// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package core

import "syscall"

func syscallKill0(pid int) error { return syscall.Kill(pid, 0) }
