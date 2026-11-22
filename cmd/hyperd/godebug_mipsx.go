//go:build mips || mipsle

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Go's MIPS xorBytes reads words from unaligned buffers, and the SHA-3 behind TLS's
// post-quantum X25519MLKEM768 key share hands it such buffers: QEMU stops with SIGBUS, and a
// real router traps into the kernel for every word. hyperd only talks to the Hyper API,
// GitHub and the rule list sources, so its TLS on MIPS keeps to X25519 and the NIST curves
// (coreEnv does the same for the core).
//go:debug tlsmlkem=0

package main
