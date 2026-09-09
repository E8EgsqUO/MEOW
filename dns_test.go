package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestParseDNSServer(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"v6:53", "v6:53", false},
		{"v6", "v6:53", false},
		{"10.0.0.1", "10.0.0.1:53", false},
		{"10.0.0.1:5353", "10.0.0.1:5353", false},
		{"  v6  ", "v6:53", false},
		{"[2001:db8::1]:53", "[2001:db8::1]:53", false},
		{"2001:db8::1", "[2001:db8::1]:53", false},
		{"", "", true},
	}
	for _, tc := range tests {
		got, err := parseDNSServer(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseDNSServer(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("parseDNSServer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func ipList(t *testing.T, s ...string) []net.IP {
	t.Helper()
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

// The interference case this feature exists for: a forged reply points a
// blocked host at a domestic address, which would send it down the direct path.
func TestConfirmCatchesAForgedDomesticAnswer(t *testing.T) {
	initCNIPData()

	router := newDomainRouter(newDomainList())
	router.lookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "114.114.114.114"), nil // forged
	}
	trustedCalls := 0
	router.trustedLookupIP = func(context.Context, string) ([]net.IP, error) {
		trustedCalls++
		return ipList(t, "104.244.42.1"), nil // the real answer
	}

	options := RouteOptions{
		ParentAvailable: true,
		JudgeByIP:       true,
		TrustedDNS:      true,
		DNSVerify:       dnsVerifyDomestic,
	}
	url := mustURL(t, "blocked.example")
	if got := router.Route(context.Background(), url, options); got != domainTypeProxy {
		t.Errorf("route = %v, want proxy: the trusted answer must win", got)
	}
	if trustedCalls != 1 {
		t.Errorf("trusted resolver called %d times, want 1", trustedCalls)
	}
}

// A genuinely domestic host must survive the extra check.
func TestConfirmKeepsAGenuineDomesticAnswer(t *testing.T) {
	initCNIPData()

	router := newDomainRouter(newDomainList())
	router.lookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "223.5.5.5"), nil
	}
	router.trustedLookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "223.5.5.5"), nil
	}

	options := RouteOptions{ParentAvailable: true, JudgeByIP: true, TrustedDNS: true, DNSVerify: dnsVerifyDomestic}
	if got := router.Route(context.Background(), mustURL(t, "domestic.example"), options); got != domainTypeDirect {
		t.Errorf("route = %v, want direct", got)
	}
}

// A foreign verdict already takes the safe path, so the domestic policy must
// not spend a query on it.
func TestConfirmSkipsForeignAnswersUnderDomesticPolicy(t *testing.T) {
	initCNIPData()

	router := newDomainRouter(newDomainList())
	router.lookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "104.244.42.1"), nil
	}
	trustedCalls := 0
	router.trustedLookupIP = func(context.Context, string) ([]net.IP, error) {
		trustedCalls++
		return ipList(t, "104.244.42.1"), nil
	}

	options := RouteOptions{ParentAvailable: true, JudgeByIP: true, TrustedDNS: true, DNSVerify: dnsVerifyDomestic}
	if got := router.Route(context.Background(), mustURL(t, "foreign.example"), options); got != domainTypeProxy {
		t.Errorf("route = %v, want proxy", got)
	}
	if trustedCalls != 0 {
		t.Errorf("trusted resolver called %d times for a foreign answer, want 0", trustedCalls)
	}
}

func TestConfirmForeignPolicyChecksTheOtherDirection(t *testing.T) {
	initCNIPData()

	router := newDomainRouter(newDomainList())
	router.lookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "104.244.42.1"), nil
	}
	router.trustedLookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "223.5.5.5"), nil
	}

	options := RouteOptions{ParentAvailable: true, JudgeByIP: true, TrustedDNS: true, DNSVerify: dnsVerifyForeign}
	if got := router.Route(context.Background(), mustURL(t, "cdn.example"), options); got != domainTypeDirect {
		t.Errorf("route = %v, want direct: the trusted answer said domestic", got)
	}
}

// An unreachable trusted resolver must not take routing down with it.
func TestConfirmFallsBackToTheLocalVerdictOnError(t *testing.T) {
	initCNIPData()

	for _, tc := range []struct {
		name    string
		trusted func(context.Context, string) ([]net.IP, error)
	}{
		{"lookup error", func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("i/o timeout")
		}},
		{"empty answer", func(context.Context, string) ([]net.IP, error) {
			return nil, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDomainRouter(newDomainList())
			router.lookupIP = func(context.Context, string) ([]net.IP, error) {
				return ipList(t, "223.5.5.5"), nil
			}
			router.trustedLookupIP = tc.trusted

			options := RouteOptions{ParentAvailable: true, JudgeByIP: true, TrustedDNS: true, DNSVerify: dnsVerifyDomestic}
			if got := router.Route(context.Background(), mustURL(t, "domestic.example"), options); got != domainTypeDirect {
				t.Errorf("route = %v, want the local verdict to stand", got)
			}
		})
	}
}

func TestConfirmIsSkippedWithoutATrustedResolver(t *testing.T) {
	initCNIPData()

	router := newDomainRouter(newDomainList())
	router.lookupIP = func(context.Context, string) ([]net.IP, error) {
		return ipList(t, "114.114.114.114"), nil
	}
	trustedCalls := 0
	router.trustedLookupIP = func(context.Context, string) ([]net.IP, error) {
		trustedCalls++
		return ipList(t, "104.244.42.1"), nil
	}

	for _, options := range []RouteOptions{
		{ParentAvailable: true, JudgeByIP: true, TrustedDNS: false, DNSVerify: dnsVerifyDomestic},
		{ParentAvailable: true, JudgeByIP: true, TrustedDNS: true, DNSVerify: dnsVerifyOff},
	} {
		router.domains = newDomainList()
		if got := router.Route(context.Background(), mustURL(t, "domestic.example"), options); got != domainTypeDirect {
			t.Errorf("route = %v, want the unverified local verdict", got)
		}
	}
	if trustedCalls != 0 {
		t.Errorf("trusted resolver called %d times, want 0", trustedCalls)
	}
}

// The resolver must send its queries to the configured address rather than to
// whatever the system is configured to use. Exercising resolver.Dial directly
// keeps this on MEOW's own code: driving a full lookup would test the standard
// library's retry state machine against a server that never answers.
func TestTrustedResolverDialsTheConfiguredServer(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	addr := udp.LocalAddr().String()

	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	go func() {
		for {
			c, err := tcp.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	r := newTrustedResolver(addr)

	// Every network the standard library may ask for must land on the
	// configured server, and the address it passes must be ignored.
	for _, network := range []string{"udp", "udp4", "udp6", "tcp", "tcp4", "tcp6"} {
		conn, err := r.Dial(context.Background(), network, "8.8.8.8:53")
		if err != nil {
			t.Errorf("Dial(%q) failed: %v", network, err)
			continue
		}
		if got := conn.RemoteAddr().String(); got != addr {
			t.Errorf("Dial(%q) reached %s, want the configured server %s", network, got, addr)
		}
		want := "tcp"
		if strings.HasPrefix(network, "udp") {
			want = "udp"
		}
		if got := conn.RemoteAddr().Network(); got != want {
			t.Errorf("Dial(%q) used %s, want %s", network, got, want)
		}
		conn.Close()
	}
}

func TestInitDNSLeavesTheResolverUnsetWhenUnconfigured(t *testing.T) {
	saved, savedResolver := config.DNSServer, trustedResolver
	t.Cleanup(func() { config.DNSServer, trustedResolver = saved, savedResolver })

	config.DNSServer = ""
	trustedResolver = newTrustedResolver("127.0.0.1:53")
	initDNS()
	if trustedResolver != nil {
		t.Error("no dnsServer configured, the trusted resolver must stay unset")
	}
	if _, err := lookupWithTrustedResolver(context.Background(), "example.test"); err == nil {
		t.Error("a lookup without a configured resolver must report an error")
	}
}

// A hostname that cannot be resolved must leave routing on the local resolver
// rather than failing startup.
func TestInitDNSKeepsGoingWhenTheServerNameCannotBeResolved(t *testing.T) {
	saved, savedResolver := config.DNSServer, trustedResolver
	t.Cleanup(func() { config.DNSServer, trustedResolver = saved, savedResolver })

	config.DNSServer = "no-such-host.invalid:53"
	trustedResolver = nil
	initDNS()
	if trustedResolver != nil {
		t.Error("an unresolvable dnsServer must not produce a resolver")
	}
}
