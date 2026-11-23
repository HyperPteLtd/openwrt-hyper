//go:build linux

package router

import (
	"encoding/binary"
	"syscall"

	"golang.org/x/sys/unix"
)

// nftTableGone asks the kernel over netlink whether an inet table is missing. Running nft
// every two seconds would cost a weak router more than this one request. Any doubt counts
// as present: only a definite answer may set off a repair.
func nftTableGone(name string) bool {
	fd, e := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_NETFILTER)
	if e != nil {
		return false
	}
	defer unix.Close(fd)
	timeout := unix.Timeval{Sec: 1}
	if unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout) != nil {
		return false
	}
	// nlmsghdr, nfgenmsg and one NFTA_TABLE_NAME attribute.
	attr := 4 + len(name) + 1
	msg := make([]byte, unix.SizeofNlMsghdr+4+(attr+3)&^3)
	binary.NativeEndian.PutUint32(msg[0:], uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:], unix.NFNL_SUBSYS_NFTABLES<<8|unix.NFT_MSG_GETTABLE)
	binary.NativeEndian.PutUint16(msg[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	msg[16], msg[17] = unix.NFPROTO_INET, unix.NFNETLINK_V0
	binary.NativeEndian.PutUint16(msg[20:], uint16(attr))
	binary.NativeEndian.PutUint16(msg[22:], unix.NFTA_TABLE_NAME)
	copy(msg[24:], name)
	if unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}) != nil {
		return false
	}
	buf := make([]byte, 4096)
	for {
		n, _, e := unix.Recvfrom(fd, buf, 0)
		if e != nil {
			return false
		}
		replies, e := syscall.ParseNetlinkMessage(buf[:n])
		if e != nil {
			return false
		}
		// The table itself, if any, comes first; the acknowledgement ends the answer.
		for _, m := range replies {
			if m.Header.Type == unix.NLMSG_ERROR && m.Header.Seq == 1 {
				return len(m.Data) >= 4 && int32(binary.NativeEndian.Uint32(m.Data)) == -int32(unix.ENOENT)
			}
		}
	}
}
