// Package coregroup names the group the core runs in.
package coregroup

import (
	"errors"
	"os/user"
	"strconv"
)

// Name is the core's group: Hyper runs the core as root:nogroup, as OpenClash runs its own.
// The firewall lets sockets of this group out before it captures the router's own traffic
// (meta skgid in the network helper), so that the core's connections do not loop back into
// it. GID is the only lookup: the manager passes its result to the helper (gid= on apply), so
// the group the core runs in and the group the firewall exempts are the same. The trade-off is
// OpenClash's too: any other process running as nogroup, such as one that dropped to
// nobody:nogroup, bypasses the capture as well.
const Name = "nogroup"

// Fallback is nogroup's ID in OpenWrt's base-files (/etc/group), used only when no group has
// that name.
const Fallback = 65534

var lookup = user.LookupGroup

// GID resolves Name, or Fallback when /etc/group does not name it. It refuses root's group,
// which would let every root process out uncaptured.
func GID() (int, error) {
	g, err := lookup(Name)
	if err != nil {
		var unknown user.UnknownGroupError
		if errors.As(err, &unknown) {
			return Fallback, nil
		}
		return 0, errors.New("core group unavailable")
	}
	gid, err := strconv.ParseUint(g.Gid, 10, 32)
	if err != nil || gid == 0 || gid == 1<<32-1 {
		return 0, errors.New("invalid core group")
	}
	return int(gid), nil
}
