//go:build linux

package kernelcli

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/HyperPteLtd/openwrt-hyper/internal/coregroup"
)

// execRestricted runs a core version or configuration check in the running core's identity: root, so
// it reads the root-owned configuration and home, in the core group (coregroup: nogroup),
// whose sockets the firewall exempts. Unlike the running core it holds no capability at all: the bounding set
// is emptied and no_new_privs set before execve, which then gives root nothing back.
func execRestricted(args []string) error {
	path, env, e := restrictedArgs(args)
	if e != nil {
		return e
	}
	gid, e := coregroup.GID()
	if e != nil {
		return e
	}
	b, e := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if e != nil {
		return e
	}
	last, e := strconv.Atoi(strings.TrimSpace(string(b)))
	if e != nil || last < 0 || last > 127 {
		return errors.New("invalid capability bound")
	}
	// prctl and capset affect the calling thread, so remain on it through exec.
	runtime.LockOSThread()
	if e = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); e != nil {
		return e
	}
	for i := 0; i <= last; i++ {
		if e = unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(i), 0, 0, 0); e != nil {
			return errors.New("cannot drop bounding capability")
		}
	}
	if e = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); e != nil {
		return e
	}
	if e = syscall.Setgroups([]int{}); e != nil {
		return e
	}
	if e = syscall.Setresgid(gid, gid, gid); e != nil {
		return e
	}
	if e = syscall.Setresuid(0, 0, 0); e != nil {
		return e
	}
	// Empty permitted, effective and inheritable sets: root's execve grants the bounding and
	// inheritable sets, both empty now.
	var none [2]unix.CapUserData
	if e = unix.Capset(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &none[0]); e != nil {
		return errors.New("cannot clear capabilities")
	}
	return syscall.Exec(path, append([]string{path}, args[1:]...), env)
}
