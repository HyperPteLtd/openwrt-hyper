package subscription

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/openweir/subconv"
)

// The router's limits on a rule set, as subconv's; the core reads each line of a set.
const (
	maxClashSetEntries    = 250000
	maxSetMaterialization = 32 << 20
	maxClashRuleLine      = 8192
)

var errRulePayload = errors.New("invalid_rule_provider")

// MRSEntries validates the MRS framing and its allocation lengths before the exact
// original bytes reach the core, and returns the entry count. Both the Zstandard
// window and the decoded size are limited.
func MRSEntries(data []byte, behavior string) (int, error) {
	expected, ok := map[string]byte{"domain": 0, "ipcidr": 1}[behavior]
	if !ok {
		return 0, errRulePayload
	}
	dec, e := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxExpandedSet))
	if e != nil {
		return 0, errRulePayload
	}
	defer dec.Close()
	in := &io.LimitedReader{R: dec, N: maxExpandedSet + 1}
	var hdr [21]byte
	if _, e = io.ReadFull(in, hdr[:]); e != nil || string(hdr[:4]) != "MRS\x01" || hdr[4] != expected {
		return 0, errRulePayload
	}
	count := binary.BigEndian.Uint64(hdr[5:13])
	extra := binary.BigEndian.Uint64(hdr[13:21])
	if count > maxClashSetEntries || extra > uint64(in.N) {
		return 0, errRulePayload
	}
	if _, e = io.CopyN(io.Discard, in, int64(extra)); e != nil {
		return 0, errRulePayload
	}
	var version [1]byte
	if _, e = io.ReadFull(in, version[:]); e != nil || version[0] != 1 {
		return 0, errRulePayload
	}
	sizes := []uint64{32}
	if expected == 0 {
		sizes = []uint64{8, 8, 1}
	}
	for _, size := range sizes {
		var n uint64
		if binary.Read(in, binary.BigEndian, &n) != nil || n == 0 || n > uint64(in.N)/size {
			return 0, errRulePayload
		}
		if _, e = io.CopyN(io.Discard, in, int64(n*size)); e != nil {
			return 0, errRulePayload
		}
	}
	var trailer [1]byte
	if _, e = in.Read(trailer[:]); e != io.EOF || in.N == 0 {
		return 0, errRulePayload
	}
	return int(count), nil
}

// ruleSet converts a rule provider's content into what the core reads: subconv turns lists of
// any client into mihomo's text, and the behavior the entries need may differ from the declared
// one. MRS passes through after MRSEntries checks it.
func ruleSet(tag, format, behavior string, data []byte) (BinarySet, error) {
	if !safeLabel(tag) || len(data) == 0 || len(data) > MaxBody {
		return BinarySet{}, errRulePayload
	}
	content, err := subconv.ConvertRuleSet(data, behavior, format)
	switch {
	case errors.Is(err, subconv.ErrNoRules):
		// An empty list, or one of comments only, matches nothing, as its provider meant: the
		// rules that use it stay.
		if behavior != "domain" && behavior != "ipcidr" {
			behavior = "classical"
		}
		content = &subconv.RuleSet{Behavior: behavior, Format: "text", Body: []byte{}}
	case errors.Is(err, subconv.ErrRuleSetTooLarge):
		return BinarySet{}, errors.New("provider_size_limit")
	case err != nil:
		return BinarySet{}, errRulePayload
	}
	count := content.Entries
	if content.Format == "mrs" {
		if count, err = MRSEntries(content.Body, content.Behavior); err != nil {
			return BinarySet{}, err
		}
	}
	if count > maxClashSetEntries || len(content.Body) > MaxBody {
		return BinarySet{}, errors.New("provider_size_limit")
	}
	sum := sha256.Sum256(content.Body)
	return BinarySet{Tag: tag, Format: content.Format, Behavior: content.Behavior, Entries: count, SHA256: hex.EncodeToString(sum[:]), Data: content.Body}, nil
}

func GeoRules(p Profile) []string {
	var results []string
	seen := map[string]bool{}
	for _, s := range p.BinarySets {
		if s.Behavior != "classical" {
			continue
		}
		upper := bytes.ToUpper(s.Data)
		for _, key := range []string{"GEOIP", "GEOSITE", "SRC-GEOIP", "IP-ASN", "SRC-IP-ASN"} {
			if !seen[key] && bytes.Contains(upper, []byte(key+",")) {
				results = append(results, key+",_")
				seen[key] = true
			}
		}
	}
	return results
}
