package main

import (
	"net"
	"strings"
)

func ipShouldDirect(ip string) (direct bool) {
	if strings.Contains(ip, ":") {
		// IPv6 addresses are connected directly
		return true
	}
	if _, isPrivate := hostIsIP(ip); isPrivate {
		return true
	}
	ipLong, err := ip2long(ip)
	if err != nil {
		return false
	}
	if ipLong == 0 {
		return true
	}
	return cnIPContains(ipLong)
}

// cnIPContains reports whether ipLong falls inside one of the China IP blocks.
func cnIPContains(ipLong uint32) bool {
	r := CNIPDataRange[ipLong>>24]
	if r.end == 0 {
		return false
	}
	// searchRange returns the first block starting after ipLong, so the only
	// block that can contain ipLong is the one before it.
	i := searchRange(r.start, r.end, func(i int) bool {
		return CNIPDataStart[i] > ipLong
	}) - 1
	if i < r.start {
		// ipLong sits below every block recorded for this first byte.
		return false
	}
	// CNIPDataNum holds the number of addresses in the block, so the last
	// address of the block is start+num-1. Comparing against start+num used to
	// pull the first address of the following block into the China set.
	return uint(ipLong-CNIPDataStart[i]) < CNIPDataNum[i]
}

// ipsShouldDirect decides whether a host that resolved to addrs can be reached
// directly. IPv4 answers decide on their own whenever the host has any: a DNS
// reply routinely mixes A and AAAA records in an unspecified order, so looking
// only at the first address sent every host with an AAAA record down the direct
// path once IPv6 became common.
//
// A host is treated as domestic only when every IPv4 answer is domestic. The
// asymmetry is deliberate: routing a domestic site through the parent proxy
// merely costs latency, while routing a blocked site directly fails outright.
func ipsShouldDirect(addrs []net.IP) bool {
	sawIPv4 := false
	allIPv4Direct := true
	for _, addr := range addrs {
		v4 := addr.To4()
		if v4 == nil {
			continue
		}
		sawIPv4 = true
		if !ipShouldDirect(v4.String()) {
			allIPv4Direct = false
		}
	}
	if sawIPv4 {
		return allIPv4Direct
	}

	// IPv6-only host: fall back to whatever policy ipShouldDirect applies.
	if len(addrs) == 0 {
		return false
	}
	for _, addr := range addrs {
		if !ipShouldDirect(addr.String()) {
			return false
		}
	}
	return true
}
