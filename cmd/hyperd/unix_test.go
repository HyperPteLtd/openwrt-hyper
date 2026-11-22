// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import "net"

func netListenUnix(path string) (net.Listener, error) { return net.Listen("unix", path) }
