package main

import (
	"net"
	"net/netip"
)

// IPv6Policy selects how IPv6 answers take part in the routing decision.
type IPv6Policy byte

const (
	// ipv6PolicyJudge weighs IPv6 addresses against the China IPv6 ranges,
	// exactly as IPv4 addresses are weighed. It is the zero value so that a
	// caller which does not care gets the accurate behaviour.
	ipv6PolicyJudge IPv6Policy = iota
	// ipv6PolicyDirect restores the pre-1.7 behaviour of treating every IPv6
	// address as domestic. Useful on networks whose IPv6 path has no route to
	// the parent proxy, such as the campus networks the option was added for.
	ipv6PolicyDirect
	// ipv6PolicyProxy keeps IPv6 off the direct path entirely.
	ipv6PolicyProxy
)

func (p IPv6Policy) String() string {
	switch p {
	case ipv6PolicyDirect:
		return "direct"
	case ipv6PolicyProxy:
		return "proxy"
	default:
		return "judge"
	}
}

// Carrier grade NAT space. Addresses here belong to the subscriber's own ISP,
// so they are local in every sense that matters for routing.
var cgNATPrefix = netip.MustParsePrefix("100.64.0.0/10")

// addrIsLocal reports whether addr names something on the local network rather
// than a destination whose geography could matter.
func addrIsLocal(addr netip.Addr) bool {
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		cgNATPrefix.Contains(addr)
}

// addrShouldDirect reports whether addr can be reached without the parent proxy.
func addrShouldDirect(addr netip.Addr, policy IPv6Policy) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if addrIsLocal(addr) {
		return true
	}
	if addr.Is6() {
		switch policy {
		case ipv6PolicyDirect:
			return true
		case ipv6PolicyProxy:
			return false
		}
	}
	return currentCNIPSet().contains(addr)
}

func ipShouldDirect(ip string, policy IPv6Policy) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return addrShouldDirect(addr, policy)
}

// ipsShouldDirect decides whether a host that resolved to addrs can be reached
// directly. Every answer is weighed rather than just the first: a DNS reply
// mixes A and AAAA records in an unspecified order, so judging by addrs[0]
// alone made the verdict depend on resolver ordering.
//
// A host counts as domestic only when all of its usable answers are domestic.
// The asymmetry is deliberate: sending a domestic site through the parent proxy
// costs latency, while sending a blocked site direct fails outright.
func ipsShouldDirect(addrs []net.IP, policy IPv6Policy) bool {
	var v4, v6 []netip.Addr
	for _, a := range addrs {
		addr, ok := netip.AddrFromSlice(a)
		if !ok {
			continue
		}
		if addr = addr.Unmap(); addr.Is4() {
			v4 = append(v4, addr)
		} else {
			v6 = append(v6, addr)
		}
	}
	if len(v4) == 0 && len(v6) == 0 {
		return false
	}

	// Under the direct and proxy policies an IPv6 answer carries no verdict
	// about where the host actually is, so it must not outvote the IPv4
	// answers. Only when IPv6 is judged on its merits do both families count.
	if policy != ipv6PolicyJudge && len(v4) > 0 {
		return allShouldDirect(v4, policy)
	}
	return allShouldDirect(v4, policy) && allShouldDirect(v6, policy)
}

func allShouldDirect(addrs []netip.Addr, policy IPv6Policy) bool {
	for _, addr := range addrs {
		if !addrShouldDirect(addr, policy) {
			return false
		}
	}
	return true
}
