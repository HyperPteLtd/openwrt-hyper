//go:build mips || mipsle

// Go's MIPS xorBytes reads words from unaligned buffers, and the SHA-3 behind TLS's
// post-quantum X25519MLKEM768 key share hands it such buffers: QEMU stops with SIGBUS, and a
// real router traps into the kernel for every word. These binaries only fetch subscriptions,
// rule sets and releases, so their TLS on MIPS keeps to X25519 and the NIST curves.
//go:debug tlsmlkem=0

package main
