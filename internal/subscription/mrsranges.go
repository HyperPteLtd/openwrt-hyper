package subscription

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/netip"

	"github.com/klauspost/compress/zstd"
)

// MRSRanges lists the address ranges of an ipcidr MRS rule set. IPv4 ranges are
// returned unmapped; a range that crosses the IPv4-mapped block is dropped.
func MRSRanges(data []byte) ([][2]netip.Addr, error) {
	if _, err := MRSEntries(data, "ipcidr"); err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxExpandedSet))
	if err != nil {
		return nil, errRulePayload
	}
	defer dec.Close()
	in := io.LimitReader(dec, maxExpandedSet)
	var hdr [21]byte
	if _, err := io.ReadFull(in, hdr[:]); err != nil {
		return nil, errRulePayload
	}
	if _, err := io.CopyN(io.Discard, in, int64(binary.BigEndian.Uint64(hdr[13:21]))+1); err != nil {
		return nil, errRulePayload
	}
	raw, err := readBlock(in, 32)
	if err != nil {
		return nil, err
	}
	out := make([][2]netip.Addr, 0, len(raw)/32)
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
