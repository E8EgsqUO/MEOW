package main

import (
	"net"
	"net/netip"
	"os"
	"testing"
)

func TestIPShouldDirect(t *testing.T) {
	initCNIPData()

	tests := []struct {
		ip     string
		policy IPv6Policy
		direct bool
		why    string
	}{
		{"114.114.114.114", ipv6PolicyJudge, true, "domestic public resolver"},
		{"223.5.5.5", ipv6PolicyJudge, true, "domestic public resolver"},
		{"180.101.50.242", ipv6PolicyJudge, true, "baidu"},
		{"202.38.64.1", ipv6PolicyJudge, true, "edu.cn"},
		{"39.156.66.10", ipv6PolicyJudge, true, "domestic CDN"},

		{"8.8.8.8", ipv6PolicyJudge, false, "google dns"},
		{"1.1.1.1", ipv6PolicyJudge, false, "cloudflare dns"},
		{"104.244.42.1", ipv6PolicyJudge, false, "twitter"},
		{"93.184.216.34", ipv6PolicyJudge, false, "example.com"},

		{"127.0.0.1", ipv6PolicyJudge, true, "loopback"},
		{"10.0.0.1", ipv6PolicyJudge, true, "private"},
		{"192.168.1.1", ipv6PolicyJudge, true, "private"},
		{"172.16.0.1", ipv6PolicyJudge, true, "private"},
		{"172.32.0.1", ipv6PolicyJudge, false, "just outside the private 172.16/12 block"},
		{"169.254.1.1", ipv6PolicyJudge, true, "link-local"},
		{"100.64.0.1", ipv6PolicyJudge, true, "carrier grade NAT"},
		{"0.0.0.0", ipv6PolicyJudge, true, "unspecified"},

		// IPv6 now carries a real verdict instead of being waved through.
		{"240e::1", ipv6PolicyJudge, true, "China Telecom IPv6"},
		{"2400:3200::1", ipv6PolicyJudge, true, "AliDNS IPv6"},
		{"2606:4700:4700::1111", ipv6PolicyJudge, false, "cloudflare IPv6"},
		{"2001:4860:4860::8888", ipv6PolicyJudge, false, "google IPv6"},
		{"::1", ipv6PolicyJudge, true, "IPv6 loopback"},
		{"fd00::1", ipv6PolicyJudge, true, "unique local address"},
		{"fe80::1", ipv6PolicyJudge, true, "IPv6 link-local"},

		// The legacy and opt-out policies still work.
		{"2606:4700:4700::1111", ipv6PolicyDirect, true, "legacy policy sends all IPv6 direct"},
		{"240e::1", ipv6PolicyProxy, false, "proxy policy keeps IPv6 off the direct path"},
		{"223.5.5.5", ipv6PolicyProxy, true, "proxy policy does not affect IPv4"},

		{"not an ip", ipv6PolicyJudge, false, "unparsable input must not be treated as domestic"},
	}

	for _, tc := range tests {
		if got := ipShouldDirect(tc.ip, tc.policy); got != tc.direct {
			t.Errorf("ipShouldDirect(%q, %v) = %v, want %v (%s)", tc.ip, tc.policy, got, tc.direct, tc.why)
		}
	}
}

// TestCNIPPrefixBoundaries walks every prefix in the built-in table and checks
// both edges plus the address just past the end. The old uint32 range check
// compared against start+num instead of start+num-1 and so pulled the first
// address after each block into the China set.
func TestCNIPPrefixBoundaries(t *testing.T) {
	initCNIPData()

	check := func(name string, data string) {
		prefixes := parsePrefixString(data, name)
		if len(prefixes) == 0 {
			t.Fatalf("%s: no prefixes parsed", name)
		}
		set := newIPRangeSet(prefixes)
		for _, p := range prefixes {
			p = p.Masked()
			first, last := p.Addr(), prefixLast(p)
			if !set.contains(first) {
				t.Errorf("%s %s: first address %s not in set", name, p, first)
			}
			if !set.contains(last) {
				t.Errorf("%s %s: last address %s not in set", name, p, last)
			}
			// Only meaningful where the merged set really ends, otherwise the
			// next address legitimately belongs to an adjacent range.
			if past := last.Next(); past.IsValid() && !set.contains(past) {
				if cnIPSet.contains(past) != set.contains(past) {
					t.Errorf("%s %s: disagreement past the end at %s", name, p, past)
				}
			}
		}
	}
	check("IPv4", cnIPv4Data)
	check("IPv6", cnIPv6Data)
}

// TestIPRangeSetExcludesGaps builds a set with a deliberate hole and checks the
// binary search does not bleed across it.
func TestIPRangeSetExcludesGaps(t *testing.T) {
	set := newIPRangeSet([]netip.Prefix{
		netip.MustParsePrefix("1.0.0.0/24"),
		netip.MustParsePrefix("1.0.2.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
	})

	in := []string{"1.0.0.0", "1.0.0.255", "1.0.2.0", "1.0.2.255", "2001:db8::", "2001:db8:ffff:ffff::1"}
	out := []string{"0.255.255.255", "1.0.1.0", "1.0.1.255", "1.0.3.0", "2001:db7::1", "2001:db9::1"}

	for _, s := range in {
		if !set.contains(netip.MustParseAddr(s)) {
			t.Errorf("%s should be in the set", s)
		}
	}
	for _, s := range out {
		if set.contains(netip.MustParseAddr(s)) {
			t.Errorf("%s should not be in the set", s)
		}
	}
}

// TestIPRangeSetMergesOverlaps covers the hand-maintained-list case: entries
// that overlap or sit next to each other must collapse without losing coverage.
func TestIPRangeSetMergesOverlaps(t *testing.T) {
	set := newIPRangeSet([]netip.Prefix{
		netip.MustParsePrefix("10.1.0.0/16"),
		netip.MustParsePrefix("10.1.5.0/24"), // nested
		netip.MustParsePrefix("10.2.0.0/16"), // adjacent
		netip.MustParsePrefix("10.0.0.0/24"), // out of order
	})

	if len(set.v4) != 2 {
		t.Errorf("expected 2 merged ranges, got %d: %v", len(set.v4), set.v4)
	}
	for _, s := range []string{"10.0.0.1", "10.1.5.1", "10.1.255.255", "10.2.0.0", "10.2.255.255"} {
		if !set.contains(netip.MustParseAddr(s)) {
			t.Errorf("%s should be in the merged set", s)
		}
	}
	if set.contains(netip.MustParseAddr("10.3.0.0")) {
		t.Error("10.3.0.0 should not be in the merged set")
	}
}

func TestIPsShouldDirect(t *testing.T) {
	initCNIPData()

	ips := func(s ...string) []net.IP {
		out := make([]net.IP, 0, len(s))
		for _, v := range s {
			ip := net.ParseIP(v)
			if ip == nil {
				t.Fatalf("bad test IP %q", v)
			}
			out = append(out, ip)
		}
		return out
	}

	tests := []struct {
		name   string
		addrs  []net.IP
		policy IPv6Policy
		direct bool
	}{
		{"all domestic IPv4", ips("114.114.114.114", "223.5.5.5"), ipv6PolicyJudge, true},
		{"all foreign IPv4", ips("8.8.8.8", "1.1.1.1"), ipv6PolicyJudge, false},
		{"mixed IPv4 stays on the proxy", ips("114.114.114.114", "8.8.8.8"), ipv6PolicyJudge, false},

		// The regression this guards: a foreign host whose AAAA record was
		// listed first used to be judged by that AAAA alone and go direct.
		{"AAAA first, foreign A", ips("2606:4700:4700::1111", "1.1.1.1"), ipv6PolicyJudge, false},
		{"AAAA first, domestic A", ips("2400:3200::1", "223.5.5.5"), ipv6PolicyJudge, true},
		{"domestic A, foreign AAAA", ips("223.5.5.5", "2606:4700:4700::1111"), ipv6PolicyJudge, false},

		{"foreign IPv6 only", ips("2606:4700:4700::1111"), ipv6PolicyJudge, false},
		{"domestic IPv6 only", ips("240e::1"), ipv6PolicyJudge, true},

		// Under the legacy policy IPv6 must not outvote the IPv4 answers.
		{"legacy: foreign AAAA does not override foreign A", ips("2606:4700:4700::1111", "1.1.1.1"), ipv6PolicyDirect, false},
		{"legacy: IPv6 only goes direct", ips("2606:4700:4700::1111"), ipv6PolicyDirect, true},

		{"proxy policy: IPv4 still decides", ips("2400:3200::1", "223.5.5.5"), ipv6PolicyProxy, true},
		{"proxy policy: IPv6 only uses the proxy", ips("240e::1"), ipv6PolicyProxy, false},

		{"no answers", nil, ipv6PolicyJudge, false},
	}

	for _, tc := range tests {
		if got := ipsShouldDirect(tc.addrs, tc.policy); got != tc.direct {
			t.Errorf("%s: ipsShouldDirect = %v, want %v", tc.name, got, tc.direct)
		}
	}
}

// TestCNIPFileOverride checks that a user supplied list replaces the built-in
// table, and that a malformed or empty one falls back instead of crashing --
// the old importCNIPFile panicked on the first bad line.
func TestCNIPFileOverride(t *testing.T) {
	dir := t.TempDir()
	saved := config.CNIPFile
	t.Cleanup(func() {
		config.CNIPFile = saved
		initCNIPData()
	})

	write := func(name, content string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	config.CNIPFile = write("good", "# a comment\n\n203.0.113.0/24\n2001:db8::/32\n")
	initCNIPData()
	if !ipShouldDirect("203.0.113.7", ipv6PolicyJudge) {
		t.Error("address from the override file should be domestic")
	}
	if !ipShouldDirect("2001:db8::1", ipv6PolicyJudge) {
		t.Error("IPv6 address from the override file should be domestic")
	}
	if ipShouldDirect("223.5.5.5", ipv6PolicyJudge) {
		t.Error("override file should replace the built-in table, not extend it")
	}

	config.CNIPFile = write("garbage", "not a cidr\n1.2.3.4\n\n")
	initCNIPData()
	if !ipShouldDirect("223.5.5.5", ipv6PolicyJudge) {
		t.Error("an unusable override file should fall back to the built-in table")
	}

	config.CNIPFile = dir + "/does-not-exist"
	initCNIPData()
	if !ipShouldDirect("223.5.5.5", ipv6PolicyJudge) {
		t.Error("a missing override file should fall back to the built-in table")
	}
}
