//go:build !linux

package router

import (
	"os/exec"
	"syscall"
)

func coreProcess(c *exec.Cmd, gid int) {}

func signalCore(c *exec.Cmd, s syscall.Signal) error { return c.Process.Signal(s) }

const coreFiles = 1000000

func setFileLimit(n uint64) error { return nil }
