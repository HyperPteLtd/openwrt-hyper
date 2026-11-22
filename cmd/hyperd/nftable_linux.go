// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"encoding/binary"
	"syscall"
)

// nfnetlink constants (linux/netfilter/nfnetlink.h, nf_tables.h) the syscall package lacks.
const (
	nfnlSubsysNFTables = 10
	nftMsgGetTable     = 1
	nfprotoINet        = 1
	nftaTableName      = 1
)

// nftTableGone asks the kernel over netlink whether an inet table is missing:
// running nft every few seconds would cost a weak router far more than one
// request. Any doubt counts as present; only a definite "no such table" sets off a repair.
func nftTableGone(name string) bool {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_NETFILTER)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	if syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: 1}) != nil {
		return false
	}
	// nlmsghdr, nfgenmsg and one NFTA_TABLE_NAME attribute.
	attr := 4 + len(name) + 1
	msg := make([]byte, syscall.SizeofNlMsghdr+4+(attr+3)&^3)
	binary.NativeEndian.PutUint32(msg[0:], uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:], nfnlSubsysNFTables<<8|nftMsgGetTable)
	binary.NativeEndian.PutUint16(msg[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_ACK)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	msg[16], msg[17] = nfprotoINet, 0 // NFNETLINK_V0
	binary.NativeEndian.PutUint16(msg[20:], uint16(attr))
	binary.NativeEndian.PutUint16(msg[22:], nftaTableName)
	copy(msg[24:], name)
	if syscall.Sendto(fd, msg, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}) != nil {
		return false
	}
	buf := make([]byte, 8192)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return false
		}
		replies, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return false
		}
		// The table itself, if any, comes first; the acknowledgement ends the answer.
		for _, m := range replies {
			if m.Header.Type == syscall.NLMSG_ERROR && m.Header.Seq == 1 {
				return len(m.Data) >= 4 && int32(binary.NativeEndian.Uint32(m.Data)) == -int32(syscall.ENOENT)
			}
		}
	}
}
