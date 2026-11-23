//go:build ignore

// This local fault is accepted by a single qualification VM using an ephemeral
// test key. Its version/config checks pass; the runtime deliberately exits.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-v":
		fmt.Println("Mihomo Meta v0.0.0-qa linux arm64 with go-test")
	case "-t":
		return
	default:
		if _, err := os.Stat("/tmp/hold-hyper-test-candidate"); err == nil {
			time.Sleep(time.Hour)
		}
		os.Exit(75)
	}
}
