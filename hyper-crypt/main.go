// SPDX-License-Identifier: GPL-2.0-only
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
)

// Exit codes, unchanged from the C helper: callers (sync_subscription) rely on them.
const (
	exitUsage  = 2
	exitDecode = 3
	exitCrypto = 4
	maxInput   = 8 << 20
)

const usage = `usage: hyper-crypt keygen
       HYPER_CRYPT_PRIVATE_KEY=<b64> hyper-crypt open <spk> <salt> <nonce> <info> < data
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && args[0] == "keygen":
		priv, pub, err := keygen(rand.Reader)
		if err != nil {
			fmt.Fprintln(stderr, "keygen failed:", err)
			return exitCrypto
		}
		fmt.Fprintf(stdout, "{\"private_key\":\"%s\",\"public_key\":\"%s\"}\n", encodeB64URL(priv), encodeB64URL(pub))
		return 0
	case len(args) == 5 && args[0] == "open":
		return cmdOpen(args[1], args[2], args[3], args[4], stdin, stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
}

func cmdOpen(spkB64, saltB64, nonceB64, info string, stdin io.Reader, stdout, stderr io.Writer) int {
	privB64, ok := os.LookupEnv("HYPER_CRYPT_PRIVATE_KEY")
	if !ok {
		fmt.Fprintln(stderr, "HYPER_CRYPT_PRIVATE_KEY is not set")
		return exitUsage
	}
	input, err := io.ReadAll(io.LimitReader(stdin, maxInput+1))
	if err != nil || len(input) > maxInput {
		fmt.Fprintln(stderr, errDecode)
		return exitDecode
	}
	decoded := make([][]byte, 0, 5)
	for _, s := range []string{privB64, spkB64, saltB64, nonceB64, string(input)} {
		b, err := decodeB64(s)
		if err != nil {
			fmt.Fprintln(stderr, errDecode)
			return exitDecode
		}
		decoded = append(decoded, b)
	}
	plain, err := open(decoded[0], decoded[1], decoded[2], decoded[3], info, decoded[4])
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.Is(err, errDecode) {
			return exitDecode
		}
		return exitCrypto
	}
	stdout.Write(plain)
	return 0
}
