package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// dnsVerifyPolicy selects which local DNS verdicts are worth a second opinion
// from the trusted resolver.
type dnsVerifyPolicy byte

const (
	// dnsVerifyDomestic re-checks answers that look domestic. This is the
	// direction interference actually takes: a forged reply that points a
	// blocked host at a domestic address turns into a direct connection that
	// cannot work. Answers that already look foreign take the parent proxy,
	// which is the safe path, so they need no confirmation.
	dnsVerifyDomestic dnsVerifyPolicy = iota
	// dnsVerifyForeign re-checks answers that look foreign. This corrects
	// domestic sites wrongly sent through the parent rather than defending
	// against interference.
	dnsVerifyForeign
	// dnsVerifyOff never consults the trusted resolver.
	dnsVerifyOff
)

func (p dnsVerifyPolicy) String() string {
	switch p {
	case dnsVerifyForeign:
		return "foreign"
	case dnsVerifyOff:
		return "off"
	default:
		return "domestic"
	}
}

const defaultDNSPort = "53"

// trustedResolver is the resolver used to confirm a routing verdict. It stays
// nil until dnsServer is configured, in which case the local resolver decides
// on its own exactly as before.
var trustedResolver *net.Resolver

// parseDNSServer canonicalises a dnsServer value into host:port.
func parseDNSServer(val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", errors.New("empty dns server")
	}
	host, port, err := net.SplitHostPort(val)
	if err != nil {
		// No port given, or an IPv6 literal written without brackets.
		host, port = val, defaultDNSPort
		if strings.Count(val, ":") > 1 {
			host = strings.Trim(val, "[]")
		}
	}
	if host == "" {
		return "", fmt.Errorf("dns server %q has no host", val)
	}
	return net.JoinHostPort(host, port), nil
}

// initDNS resolves the configured DNS server once and builds the resolver that
// talks to it.
//
// The address is resolved here, with the system resolver, rather than on every
// query. That keeps a hostname working -- MEOW's own server is often named in
// /etc/hosts or reachable only over an overlay network -- without the trusted
// resolver having to resolve its own address through itself.
func initDNS() {
	trustedResolver = nil
	if config.DNSServer == "" {
		return
	}

	hostPort, err := parseDNSServer(config.DNSServer)
	if err != nil {
		errl.Printf("dnsServer %q ignored: %v", config.DNSServer, err)
		return
	}

	host, port, _ := net.SplitHostPort(hostPort)
	addr := hostPort
	if net.ParseIP(host) == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, lookupErr := net.DefaultResolver.LookupIP(ctx, "ip", host)
		cancel()
		if lookupErr != nil || len(ips) == 0 {
			errl.Printf("cannot resolve dnsServer %q: %v; routing will use the local resolver only",
				config.DNSServer, lookupErr)
			return
		}
		addr = net.JoinHostPort(ips[0].String(), port)
	}

	trustedResolver = newTrustedResolver(addr)
	info.Printf("trusted DNS server %s (%s), verifying %s answers\n",
		config.DNSServer, addr, config.DNSVerify)
}

func newTrustedResolver(addr string) *net.Resolver {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &net.Resolver{
		// Go's own DNS client is required: the cgo resolver cannot be pointed
		// at a specific server. It falls back from UDP to TCP on a truncated
		// reply by itself.
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			switch network {
			case "udp", "udp4", "udp6":
				network = "udp"
			default:
				network = "tcp"
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
}

func lookupWithTrustedResolver(ctx context.Context, host string) ([]net.IP, error) {
	if trustedResolver == nil {
		return nil, errors.New("no trusted resolver configured")
	}
	return trustedResolver.LookupIP(ctx, "ip", host)
}
