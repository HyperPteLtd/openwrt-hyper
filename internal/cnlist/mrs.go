// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package cnlist

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"

	"github.com/klauspost/compress/zstd"
)

// Limits on a decoded list: both the Zstandard window and the decoded size are bounded, so
// a hostile download cannot exhaust a router's memory.
const (
	maxExpanded = 16 << 20
	maxEntries  = 250000
)

var errMRS = errors.New("invalid MRS rule set")

func decoder(data []byte) (*zstd.Decoder, error) {
	return zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxExpanded))
}

// mrsEntries checks the framing of a mihomo MRS rule set of behavior (domain or ipcidr)
// end to end and returns its entry count.
func mrsEntries(data []byte, behavior string) (int, error) {
	expected, ok := map[string]byte{"domain": 0, "ipcidr": 1}[behavior]
	if !ok {
		return 0, errMRS
	}
	dec, err := decoder(data)
	if err != nil {
		return 0, errMRS
	}
	defer dec.Close()
	in := &io.LimitedReader{R: dec, N: maxExpanded + 1}
	var hdr [21]byte
	if _, err = io.ReadFull(in, hdr[:]); err != nil || string(hdr[:4]) != "MRS\x01" || hdr[4] != expected {
		return 0, errMRS
	}
	count := binary.BigEndian.Uint64(hdr[5:13])
	extra := binary.BigEndian.Uint64(hdr[13:21])
	if count > maxEntries || extra > uint64(in.N) {
		return 0, errMRS
	}
	if _, err = io.CopyN(io.Discard, in, int64(extra)); err != nil {
		return 0, errMRS
	}
	var version [1]byte
	if _, err = io.ReadFull(in, version[:]); err != nil || version[0] != 1 {
		return 0, errMRS
	}
	sizes := []uint64{32} // ipcidr: one [from, to] pair of 16-byte addresses per range
	if expected == 0 {
		sizes = []uint64{8, 8, 1} // domain: the succinct trie's leaves, label bitmap and labels
	}
	for _, size := range sizes {
		var n uint64
		if binary.Read(in, binary.BigEndian, &n) != nil || n == 0 || n > uint64(in.N)/size {
			return 0, errMRS
		}
		if _, err = io.CopyN(io.Discard, in, int64(n*size)); err != nil {
			return 0, errMRS
		}
	}
	var trailer [1]byte
	if _, err = in.Read(trailer[:]); err != io.EOF || in.N == 0 {
		return 0, errMRS
	}
	return int(count), nil
}

// mrsRanges lists the address ranges of an ipcidr MRS rule set, IPv4 ones unmapped.
func mrsRanges(data []byte) ([][2]netip.Addr, error) {
	if _, err := mrsEntries(data, "ipcidr"); err != nil {
		return nil, err
	}
	dec, err := decoder(data)
	if err != nil {
		return nil, errMRS
	}
	defer dec.Close()
	in := io.LimitReader(dec, maxExpanded)
	var hdr [21]byte
	if _, err = io.ReadFull(in, hdr[:]); err != nil {
		return nil, errMRS
	}
	// The extra block, then the format version.
	if _, err = io.CopyN(io.Discard, in, int64(binary.BigEndian.Uint64(hdr[13:21]))+1); err != nil {
		return nil, errMRS
	}
	var n uint64
	if binary.Read(in, binary.BigEndian, &n) != nil || n == 0 || n > maxExpanded/32 {
		return nil, errMRS
	}
	raw := make([]byte, n*32)
	if _, err = io.ReadFull(in, raw); err != nil {
		return nil, errMRS
	}
	out := make([][2]netip.Addr, 0, n)
	for i := 0; i+32 <= len(raw); i += 32 {
		from := netip.AddrFrom16([16]byte(raw[i : i+16]))
		to := netip.AddrFrom16([16]byte(raw[i+16 : i+32]))
		if from.Is4In6() != to.Is4In6() || to.Less(from) {
			continue
		}
		out = append(out, [2]netip.Addr{from.Unmap(), to.Unmap()})
	}
	return out, nil
}
