//go:generate go run -tags generate chinaip_gen.go

package main

import (
	"bufio"
	"encoding/binary"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// ipRange is a closed address interval. Prefixes are normalised into ranges so
// that overlapping or adjacent entries -- common in hand-maintained lists --
// collapse into a set that a single binary search can answer exactly.
type ipRange struct {
	lo, hi netip.Addr
}

// ipRangeSet answers "is this address in the set" in O(log n). IPv4 and IPv6
// are kept apart because their addresses are not mutually comparable.
type ipRangeSet struct {
	v4 []ipRange
	v6 []ipRange
}

func (s *ipRangeSet) contains(addr netip.Addr) bool {
	if s == nil {
		return false
	}
	addr = addr.Unmap()
	ranges := s.v6
	if addr.Is4() {
		ranges = s.v4
	}
	// The last range starting at or before addr is the only one that can hold
	// it, since the ranges are sorted, merged and therefore disjoint.
	i := sort.Search(len(ranges), func(i int) bool {
		return ranges[i].lo.Compare(addr) > 0
	}) - 1
	if i < 0 {
		return false
	}
	return ranges[i].hi.Compare(addr) >= 0
}

func (s *ipRangeSet) empty() bool {
	return s == nil || (len(s.v4) == 0 && len(s.v6) == 0)
}

// prefixLast returns the highest address covered by p.
func prefixLast(p netip.Prefix) netip.Addr {
	p = p.Masked()
	addr := p.Addr()
	bits := p.Bits()

	if addr.Is4() {
		b := addr.As4()
		v := binary.BigEndian.Uint32(b[:]) | ^uint32(0)>>uint(bits)
		binary.BigEndian.PutUint32(b[:], v)
		return netip.AddrFrom4(b)
	}

	b := addr.As16()
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	if bits <= 64 {
		hi |= ^uint64(0) >> uint(bits)
		lo = ^uint64(0)
	} else {
		lo |= ^uint64(0) >> uint(bits-64)
	}
	binary.BigEndian.PutUint64(b[:8], hi)
	binary.BigEndian.PutUint64(b[8:], lo)
	return netip.AddrFrom16(b)
}

// newIPRangeSet normalises prefixes into a sorted, merged range set.
func newIPRangeSet(prefixes []netip.Prefix) *ipRangeSet {
	set := &ipRangeSet{}
	var v4, v6 []ipRange
	for _, p := range prefixes {
		if !p.IsValid() {
			continue
		}
		p = p.Masked()
		r := ipRange{lo: p.Addr(), hi: prefixLast(p)}
		if p.Addr().Is4() {
			v4 = append(v4, r)
		} else {
			v6 = append(v6, r)
		}
	}
	set.v4 = mergeRanges(v4)
	set.v6 = mergeRanges(v6)
	return set
}

func mergeRanges(ranges []ipRange) []ipRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		if c := ranges[i].lo.Compare(ranges[j].lo); c != 0 {
			return c < 0
		}
		return ranges[i].hi.Compare(ranges[j].hi) < 0
	})

	out := ranges[:1]
	for _, r := range ranges[1:] {
		last := &out[len(out)-1]
		// Merge when r starts inside the previous range or immediately after
		// it. Next() is invalid only at the maximum address, where nothing can
		// follow anyway.
		adjacent := last.hi.Next()
		if r.lo.Compare(last.hi) <= 0 || (adjacent.IsValid() && r.lo.Compare(adjacent) == 0) {
			if r.hi.Compare(last.hi) > 0 {
				last.hi = r.hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// parsePrefixList reads newline separated CIDR prefixes, skipping blank lines
// and "#" comments. Malformed entries are reported and skipped rather than
// aborting: a typo in a user maintained list must not stop the proxy starting.
func parsePrefixList(r *bufio.Scanner, origin string) []netip.Prefix {
	var out []netip.Prefix
	bad := 0
	for r.Scan() {
		line := strings.TrimSpace(r.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := netip.ParsePrefix(line)
		if err != nil {
			bad++
			if bad <= 5 {
				errl.Printf("%s: ignoring malformed CIDR %q: %v", origin, line, err)
			}
			continue
		}
		out = append(out, p)
	}
	if err := r.Err(); err != nil {
		errl.Printf("%s: read error: %v", origin, err)
	}
	if bad > 5 {
		errl.Printf("%s: %d malformed CIDR entries ignored in total", origin, bad)
	}
	return out
}

func parsePrefixString(data, origin string) []netip.Prefix {
	return parsePrefixList(bufio.NewScanner(strings.NewReader(data)), origin)
}

// cnIPSet holds the China address ranges consulted by the IP based routing
// decision. It stays empty until initCNIPData runs, which makes every address
// look foreign; that is the safe direction, since a foreign verdict routes
// through the parent proxy instead of failing on a blocked direct connection.
var cnIPSet = &ipRangeSet{}

func initCNIPData() {
	if set := loadCNIPFile(config.CNIPFile); set != nil {
		cnIPSet = set
		return
	}
	cnIPSet = newIPRangeSet(append(
		parsePrefixString(cnIPv4Data, "built-in China IPv4 data"),
		parsePrefixString(cnIPv6Data, "built-in China IPv6 data")...,
	))
	debug.Printf("loaded built-in China IP data (%s, generated %s): %d IPv4 ranges, %d IPv6 ranges",
		cnIPDataSource, cnIPDataGenerated, len(cnIPSet.v4), len(cnIPSet.v6))
}

// loadCNIPFile returns the user supplied China IP set, or nil to fall back to
// the built-in table. An unreadable or empty file falls back rather than
// leaving the proxy with no routing data at all.
func loadCNIPFile(path string) *ipRangeSet {
	if path == "" {
		return nil
	}
	if err := isFileExists(path); err != nil {
		debug.Printf("china ip list unavailable: %s: %v", path, err)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		errl.Println("Error opening china ip list:", err)
		return nil
	}
	defer f.Close()

	set := newIPRangeSet(parsePrefixList(bufio.NewScanner(f), path))
	if set.empty() {
		errl.Printf("china ip list %s contained no usable CIDR entries, using built-in data", path)
		return nil
	}
	debug.Printf("loaded china ip list from %s: %d IPv4 ranges, %d IPv6 ranges",
		path, len(set.v4), len(set.v6))
	return set
}
