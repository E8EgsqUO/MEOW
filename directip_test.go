package main

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestIPShouldDirect(t *testing.T) {
	initCNIPData()

	tests := []struct {
		ip     string
		direct bool
		why    string
	}{
		{"114.114.114.114", true, "domestic public resolver"},
		{"223.5.5.5", true, "domestic public resolver"},
		{"180.101.50.242", true, "baidu"},
		{"202.38.64.1", true, "edu.cn"},
		{"39.156.66.10", true, "domestic CDN"},

		{"8.8.8.8", false, "google dns"},
		{"1.1.1.1", false, "cloudflare dns"},
		{"104.244.42.1", false, "twitter"},
		{"93.184.216.34", false, "example.com"},

		{"127.0.0.1", true, "loopback"},
		{"10.0.0.1", true, "private"},
		{"192.168.1.1", true, "private"},
		{"172.16.0.1", true, "private"},
		{"172.32.0.1", false, "just outside the private 172.16/12 block"},

		{"::1", true, "IPv6 is currently always direct"},
		{"not an ip", false, "unparsable input must not be treated as domestic"},
	}

	for _, tc := range tests {
		if got := ipShouldDirect(tc.ip); got != tc.direct {
			t.Errorf("ipShouldDirect(%q) = %v, want %v (%s)", tc.ip, got, tc.direct, tc.why)
		}
	}
}

func uint32ToIP(v uint32) net.IP {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return net.IP(b)
}

// TestCNIPBlockBoundaries walks every China block and checks both edges. The
// range check used to compare against start+num rather than start+num-1, which
// pulled the first address after each block into the China set -- 3651 of the
// 5451 blocks leaked one address that way, 1.1.1.0 among them.
func TestCNIPBlockBoundaries(t *testing.T) {
	initCNIPData()

	for i := range CNIPDataStart {
		start := CNIPDataStart[i]
		num := uint32(CNIPDataNum[i])
		last := start + num - 1

		if !ipShouldDirect(uint32ToIP(start).String()) {
			t.Errorf("block %s/%d: first address not judged domestic", uint32ToIP(start), num)
		}
		if !ipShouldDirect(uint32ToIP(last).String()) {
			t.Errorf("block %s/%d: last address not judged domestic", uint32ToIP(start), num)
		}

		// Only meaningful when the next block does not start right after this
		// one, otherwise the address legitimately belongs to China.
		past := start + num
		if i+1 < len(CNIPDataStart) && past < CNIPDataStart[i+1] {
			if ipShouldDirect(uint32ToIP(past).String()) {
				t.Errorf("block %s/%d: address %s past the block end judged domestic",
					uint32ToIP(start), num, uint32ToIP(past))
			}
		}
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
		direct bool
	}{
		{"all domestic IPv4", ips("114.114.114.114", "223.5.5.5"), true},
		{"all foreign IPv4", ips("8.8.8.8", "1.1.1.1"), false},
		{"mixed IPv4 stays on the proxy", ips("114.114.114.114", "8.8.8.8"), false},
		// The regression this guards: a foreign host with an AAAA record listed
		// first used to be judged by that AAAA alone and go direct.
		{"AAAA first, foreign A", ips("2606:4700:4700::1111", "1.1.1.1"), false},
		{"AAAA first, domestic A", ips("2400:3200::1", "223.5.5.5"), true},
		{"IPv6 only", ips("2606:4700:4700::1111"), true},
		{"no answers", nil, false},
	}

	for _, tc := range tests {
		if got := ipsShouldDirect(tc.addrs); got != tc.direct {
			t.Errorf("%s: ipsShouldDirect = %v, want %v", tc.name, got, tc.direct)
		}
	}
}
