package coregroup

import (
	"errors"
	"os/user"
	"testing"
)

func TestGIDResolvesNogroupByName(t *testing.T) {
	defer func(previous func(string) (*user.Group, error)) { lookup = previous }(lookup)
	for _, c := range []struct {
		group *user.Group
		err   error
		gid   int
		fails string
	}{
		{group: &user.Group{Name: "nogroup", Gid: "65534"}, gid: 65534},
		{group: &user.Group{Name: "nogroup", Gid: "4242"}, gid: 4242},
		// The name is missing: OpenWrt's base-files ID.
		{err: user.UnknownGroupError("nogroup"), gid: Fallback},
		// Root's group would exempt every root process from the capture.
		{group: &user.Group{Name: "nogroup", Gid: "0"}, fails: "invalid core group"},
		{group: &user.Group{Name: "nogroup", Gid: "-1"}, fails: "invalid core group"},
		{group: &user.Group{Name: "nogroup", Gid: "4294967295"}, fails: "invalid core group"},
		{group: &user.Group{Name: "nogroup", Gid: "x"}, fails: "invalid core group"},
		// An unreadable /etc/group is not a missing name.
		{err: errors.New("open /etc/group: permission denied"), fails: "core group unavailable"},
	} {
		lookup = func(name string) (*user.Group, error) {
			if name != Name {
				t.Fatal(name)
			}
			return c.group, c.err
		}
		gid, err := GID()
		if c.fails != "" {
			if err == nil || err.Error() != c.fails {
				t.Fatal(c.group, c.err, gid, err)
			}
		} else if err != nil || gid != c.gid {
			t.Fatal(c.group, c.err, gid, err)
		}
	}
	if Name != "nogroup" || Fallback != 65534 {
		t.Fatal(Name, Fallback)
	}
}
