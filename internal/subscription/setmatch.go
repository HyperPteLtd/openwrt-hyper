package subscription

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"net/netip"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/openweir/subconv"
)

// ErrSetQuery reports a lookup that is neither a domain name nor an IP address.
var ErrSetQuery = errors.New("invalid_set_query")

// SetMatch reports whether query, a domain name or an IP address, is in set.
// Classical sets only answer for their DOMAIN, DOMAIN-SUFFIX, DOMAIN-KEYWORD and
// IP-CIDR lines; other rule types never match.
func SetMatch(set BinarySet, query string) (bool, error) {
	q, err := parseSetQuery(query)
	if err != nil {
		return false, err
	}
	if set.Format == "mrs" {
		return mrsMatch(set.Data, set.Behavior, q)
	}
	// Sets stored before subconv converted them may still be YAML payloads.
	content, err := subconv.ConvertRuleSet(set.Data, set.Behavior, set.Format)
	if errors.Is(err, subconv.ErrNoRules) {
		return false, nil
	}
	if err != nil {
		return false, errRulePayload
	}
	for line := range strings.Lines(string(content.Body)) {
		if lineMatch(content.Behavior, line, q) {
			return true, nil
		}
	}
	return false, nil
}

type setQuery struct {
	domain string
	ip     netip.Addr
}

func parseSetQuery(s string) (setQuery, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if ip, err := netip.ParseAddr(s); err == nil {
		return setQuery{ip: ip.Unmap()}, nil
	}
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " /:@*+,") {
		return setQuery{}, ErrSetQuery
	}
	return setQuery{domain: s}, nil
}

func domainMatch(entry, domain string) bool {
	entry = strings.ToLower(entry)
	switch {
	case strings.HasPrefix(entry, "+."):
		base := entry[2:]
		return domain == base || strings.HasSuffix(domain, "."+base)
	case strings.HasPrefix(entry, "."):
		return strings.HasSuffix(domain, entry)
	case strings.HasPrefix(entry, "*."):
		rest, ok := strings.CutSuffix(domain, entry[1:])
		return ok && rest != "" && !strings.Contains(rest, ".")
	}
	return domain == entry
}

func prefixMatch(entry string, ip netip.Addr) bool {
	p, err := netip.ParsePrefix(entry)
	if err != nil {
		a, e := netip.ParseAddr(entry)
		return e == nil && a.Unmap() == ip
	}
	return p.Masked().Contains(ip) || (p.Addr().Is4In6() && ip.Is4() && p.Bits() >= 96 &&
		netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked().Contains(ip))
}

func lineMatch(behavior, line string, q setQuery) bool {
	line = strings.TrimSpace(line)
	switch behavior {
	case "domain":
		return q.domain != "" && domainMatch(line, q.domain)
	case "ipcidr":
		return q.ip.IsValid() && prefixMatch(line, q.ip)
	}
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return false
	}
	value := strings.TrimSpace(parts[1])
	switch strings.ToUpper(strings.TrimSpace(parts[0])) {
	case "DOMAIN":
		return q.domain != "" && q.domain == strings.ToLower(value)
	case "DOMAIN-SUFFIX":
		v := strings.ToLower(value)
		return q.domain != "" && (q.domain == v || strings.HasSuffix(q.domain, "."+v))
	case "DOMAIN-KEYWORD":
		return q.domain != "" && strings.Contains(q.domain, strings.ToLower(value))
	case "IP-CIDR", "IP-CIDR6":
		return q.ip.IsValid() && prefixMatch(value, q.ip)
	}
	return false
}

// mrsMatch reads mihomo's binary rule set: a Zstandard stream holding "MRS\x01",
// the behavior byte, the entry count, an extra block, then the payload. A domain
// payload is a succinct trie over reversed names; an ipcidr payload is a list of
// 16-byte from/to ranges.
func mrsMatch(data []byte, behavior string, q setQuery) (bool, error) {
	if _, err := MRSEntries(data, behavior); err != nil {
		return false, err
	}
	dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxExpandedSet))
	if err != nil {
		return false, errRulePayload
	}
	defer dec.Close()
	in := io.LimitReader(dec, maxExpandedSet)
	var hdr [21]byte
	if _, err := io.ReadFull(in, hdr[:]); err != nil {
		return false, errRulePayload
	}
	if _, err := io.CopyN(io.Discard, in, int64(binary.BigEndian.Uint64(hdr[13:21]))+1); err != nil {
		return false, errRulePayload
	}
	if behavior == "ipcidr" {
		if !q.ip.IsValid() {
			return false, nil
		}
		ranges, err := readBlock(in, 32)
		if err != nil {
			return false, err
		}
		// As16 maps IPv4 into ::ffff:0:0/96, as mihomo stores it.
		want := q.ip.As16()
		for i := 0; i+32 <= len(ranges); i += 32 {
			if bytes.Compare(ranges[i:i+16], want[:]) <= 0 && bytes.Compare(want[:], ranges[i+16:i+32]) <= 0 {
				return true, nil
			}
		}
		return false, nil
	}
	if q.domain == "" {
		return false, nil
	}
	var words [2][]uint64
	for i := range words {
		raw, err := readBlock(in, 8)
		if err != nil {
			return false, err
		}
		words[i] = make([]uint64, len(raw)/8)
		for j := range words[i] {
			words[i][j] = binary.BigEndian.Uint64(raw[j*8:])
		}
	}
	labels, err := readBlock(in, 1)
	if err != nil {
		return false, err
	}
	return newTrie(words[0], words[1], labels).has(q.domain), nil
}

func readBlock(in io.Reader, size int) ([]byte, error) {
	var n uint64
	if binary.Read(in, binary.BigEndian, &n) != nil || n == 0 || n > maxExpandedSet/uint64(size) {
		return nil, errRulePayload
	}
	buf := make([]byte, n*uint64(size))
	if _, err := io.ReadFull(in, buf); err != nil {
		return nil, errRulePayload
	}
	return buf, nil
}

// trie is mihomo's DomainSet: a level-order succinct trie. Every node's children
// are a run of labels ended by a 1 bit in labelBitmap; leaves marks nodes that end
// a stored name.
type trie struct {
	leaves, bitmap []uint64
	labels         []byte
	ones           []int // position of each 1 bit in bitmap
	zeros          []int // number of 0 bits before each word
}

func newTrie(leaves, bitmap []uint64, labels []byte) *trie {
	t := &trie{leaves: leaves, bitmap: bitmap, labels: labels, zeros: make([]int, len(bitmap)+1)}
	for w, word := range bitmap {
		t.zeros[w+1] = t.zeros[w] + 64 - bits.OnesCount64(word)
		for b := word; b != 0; b &= b - 1 {
			t.ones = append(t.ones, w*64+bits.TrailingZeros64(b))
		}
	}
	return t
}

func bit(words []uint64, i int) bool {
	return i>>6 < len(words) && words[i>>6]&(1<<(uint(i)&63)) != 0
}

// countZeros returns the number of 0 bits in bitmap[0:i).
func (t *trie) countZeros(i int) int {
	w := i >> 6
	if w >= len(t.bitmap) {
		return t.zeros[len(t.bitmap)]
	}
	return t.zeros[w] + bits.OnesCount64(^t.bitmap[w]&(1<<(uint(i)&63)-1))
}

func (t *trie) label(bm, node int) (byte, bool) {
	i := bm - node
	if i < 0 || i >= len(t.labels) {
		return 0, false
	}
	return t.labels[i], true
}

// child moves from the label at bitmap position bm to the first label position of
// its child node.
func (t *trie) child(bm int) (node, first int, ok bool) {
	node = t.countZeros(bm + 1)
	if node < 1 || node-1 >= len(t.ones) {
		return 0, 0, false
	}
	return node, t.ones[node-1] + 1, true
}

const (
	trieStep     = '.'
	trieSuffix   = '+' // "+.example.com": the name and every subdomain
	trieWildcard = '*' // "*.example.com": exactly one more label
)

func (t *trie) has(domain string) bool {
	key := []byte(domain)
	for i, j := 0, len(key)-1; i < j; i, j = i+1, j-1 {
		key[i], key[j] = key[j], key[i]
	}
	return t.walk(key, 0, 0, 0)
}

func (t *trie) walk(key []byte, i, node, bm int) bool {
	if i == len(key) {
		return bit(t.leaves, node)
	}
	for ; !bit(t.bitmap, bm); bm++ {
		c, ok := t.label(bm, node)
		if !ok {
			return false
		}
		switch c {
		case trieSuffix:
			return true
		case trieWildcard:
			// Consume one whole label of the key, then continue below the wildcard.
			j := i
			for j < len(key) && key[j] != trieStep {
				j++
			}
			if n, first, ok := t.child(bm); ok && t.walk(key, j, n, first) {
				return true
			}
		case key[i]:
			n, first, ok := t.child(bm)
			return ok && t.walk(key, i+1, n, first)
		}
	}
	return false
}

// InlineSetMatch reports whether query matches one of the expanded entries of the
// inline rule set tag in rules.
func InlineSetMatch(rules []Rule, tag, query string) (bool, error) {
	q, err := parseSetQuery(query)
	if err != nil {
		return false, err
	}
	kinds := map[string]string{"domain": "DOMAIN", "domain_suffix": "DOMAIN-SUFFIX", "domain_keyword": "DOMAIN-KEYWORD", "ip_cidr": "IP-CIDR"}
	for _, r := range rules {
		if r.RuleSet != tag {
			continue
		}
		for key, kind := range kinds {
			values, _ := textlist(r.Match[key])
			for _, v := range values {
				if lineMatch("classical", kind+","+v, q) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
