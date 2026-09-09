package main

import (
	"bufio"
	"context"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
)

type DomainList struct {
	// Domain holds configured rules, which may also match subdomains.
	Domain map[string]DomainType
	// learned holds runtime verdicts for exact hosts only.
	learned map[string]DomainType
	sync.RWMutex
}

type DomainType byte

const (
	domainTypeUnknown DomainType = iota
	domainTypeDirect
	domainTypeProxy
	domainTypeReject
)

func newDomainList() *DomainList {
	return &DomainList{
		Domain:  map[string]DomainType{},
		learned: map[string]DomainType{},
	}
}

// RouteOptions contains runtime facts which affect a routing decision. Keeping
// them as inputs prevents the router from depending on the global proxy pool or
// configuration while preserving the existing policy.
type RouteOptions struct {
	ParentAvailable bool
	JudgeByIP       bool
	IPv6            IPv6Policy
	// TrustedDNS reports whether a resolver on an unforgeable path is
	// available to confirm a verdict.
	TrustedDNS bool
	DNSVerify  dnsVerifyPolicy
}

// Router decides how a request should leave MEOW. It does not establish the
// outbound connection.
type Router interface {
	Route(context.Context, *URL, RouteOptions) DomainType
}

type domainRouter struct {
	domains         *DomainList
	lookupIP        func(context.Context, string) ([]net.IP, error)
	trustedLookupIP func(context.Context, string) ([]net.IP, error)
	localLookups    lookupGroup
	trustedLookups  lookupGroup
}

type lookupCall struct {
	done  chan struct{}
	addrs []net.IP
	err   error
}

// lookupGroup coalesces concurrent lookups for the same host. Results live
// only until the lookup finishes; DomainList remains the routing cache.
type lookupGroup struct {
	sync.Mutex
	calls map[string]*lookupCall
}

func (g *lookupGroup) do(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IP, error)) ([]net.IP, error) {
	g.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*lookupCall)
	}
	if call := g.calls[host]; call != nil {
		g.Unlock()
		select {
		case <-call.done:
			return call.addrs, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &lookupCall{done: make(chan struct{})}
	g.calls[host] = call
	g.Unlock()

	call.addrs, call.err = lookup(ctx, host)
	g.Lock()
	delete(g.calls, host)
	close(call.done)
	g.Unlock()
	return call.addrs, call.err
}

func newDomainRouter(domains *DomainList) *domainRouter {
	return &domainRouter{
		domains: domains,
		lookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		trustedLookupIP: lookupWithTrustedResolver,
	}
}

// confirm asks the trusted resolver for a second opinion on a verdict the local
// resolver reached, when that verdict falls in the category the verify policy
// covers. The local verdict stands whenever the trusted lookup cannot answer:
// a resolver that is unreachable must not take routing down with it.
func (router *domainRouter) confirm(ctx context.Context, host string, local bool, options RouteOptions) bool {
	if !options.TrustedDNS || router.trustedLookupIP == nil {
		return local
	}
	switch options.DNSVerify {
	case dnsVerifyDomestic:
		if !local {
			return local
		}
	case dnsVerifyForeign:
		if local {
			return local
		}
	default:
		return local
	}

	addrs, err := router.trustedLookups.do(ctx, host, router.trustedLookupIP)
	if err != nil {
		debug.Printf("trusted DNS lookup for %s failed, keeping the local verdict: %v", host, err)
		return local
	}
	if len(addrs) == 0 {
		debug.Printf("trusted DNS returned no addresses for %s, keeping the local verdict", host)
		return local
	}

	trusted := ipsShouldDirect(addrs, options.IPv6)
	if trusted != local {
		info.Printf("%s: local DNS said %s, trusted DNS said %s\n",
			host, directOrProxy(local), directOrProxy(trusted))
	}
	return trusted
}

func directOrProxy(direct bool) string {
	if direct {
		return "direct"
	}
	return "proxy"
}

func (router *domainRouter) Route(ctx context.Context, url *URL, options RouteOptions) (domainType DomainType) {
	debug.Printf("judging host: %s", url.Host)
	domainList := router.domains
	domainList.RLock()
	hostType := domainList.Domain[url.Host]
	domainType = domainList.Domain[url.Domain]
	cachedType := domainList.learned[url.Host]
	domainList.RUnlock()

	if hostType == domainTypeReject || domainType == domainTypeReject {
		debug.Printf("host or domain should reject")
		return domainTypeReject
	}
	if !options.ParentAvailable { // no way to retry, so always visit directly
		return domainTypeDirect
	}
	if url.Domain == "" { // simple host or private ip
		return domainTypeDirect
	}
	if hostType == domainTypeDirect || domainType == domainTypeDirect {
		debug.Printf("host or domain should direct")
		return domainTypeDirect
	}
	if hostType == domainTypeProxy || domainType == domainTypeProxy {
		debug.Printf("host or domain should using proxy")
		return domainTypeProxy
	}
	if cachedType != domainTypeUnknown {
		return cachedType
	}

	if !options.JudgeByIP {
		return domainTypeProxy
	}
	debug.Printf("judging by ip")
	var shouldDirect bool
	if addr, err := netip.ParseAddr(url.Host); err == nil {
		// A literal address needs no lookup. netip understands IPv6 literals,
		// which the old dotted-quad check silently passed on to the resolver.
		if addrIsLocal(addr.Unmap()) {
			domainList.add(url.Host, domainTypeDirect)
			return domainTypeDirect
		}
		shouldDirect = addrShouldDirect(addr, options.IPv6)
	} else {
		hostIPs, err := router.localLookups.do(ctx, url.Host, router.lookupIP)
		if err != nil {
			errl.Printf("error looking up host ip %s, err %s", url.Host, err)
			return domainTypeProxy
		}
		if len(hostIPs) == 0 {
			errl.Printf("host lookup returned no addresses for %s", url.Host)
			return domainTypeProxy
		}
		// Weigh every answer instead of only the first one; see ipsShouldDirect.
		shouldDirect = ipsShouldDirect(hostIPs, options.IPv6)
		shouldDirect = router.confirm(ctx, url.Host, shouldDirect, options)
	}

	if shouldDirect {
		domainList.add(url.Host, domainTypeDirect)
		debug.Printf("host or domain should direct")
		return domainTypeDirect
	} else {
		domainList.add(url.Host, domainTypeProxy)
		debug.Printf("host or domain should using proxy")
		return domainTypeProxy
	}
}

func (domainList *DomainList) add(host string, domainType DomainType) {
	domainList.Lock()
	defer domainList.Unlock()
	domainList.learned[host] = domainType
}

// GetDomainList returns configured direct rules for PAC's suffix matching.
// Learned hosts stay behind MEOW so they cannot become subdomain-wide PAC rules.
func (domainList *DomainList) GetDomainList() []string {
	domainList.RLock()
	defer domainList.RUnlock()

	lst := make([]string, 0)
	for site, domainType := range domainList.Domain {
		if domainType == domainTypeDirect {
			lst = append(lst, site)
		}
	}
	return lst
}

var domainList = newDomainList()
var router Router = newDomainRouter(domainList)

func initDomainLists(domainList *DomainList, config Config) {
	initDomainList(domainList, config.DirectFile, domainTypeDirect)
	initDomainList(domainList, config.ProxyFile, domainTypeProxy)
	initDomainList(domainList, config.RejectFile, domainTypeReject)
}

func initDomainList(domainList *DomainList, domainListFile string, domainType DomainType) {
	var err error
	if err = isFileExists(domainListFile); err != nil {
		debug.Printf("routing rules file unavailable: %s: %v", domainListFile, err)
		return
	}
	f, err := os.Open(domainListFile)
	if err != nil {
		errl.Println("Error opening domain list:", err)
		return
	}
	defer f.Close()

	domainList.Lock()
	defer domainList.Unlock()
	loaded := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		domain := canonicalHost(strings.TrimSpace(scanner.Text()))
		if domain == "" {
			continue
		}
		domainList.Domain[domain] = domainType
		loaded++
	}
	if scanner.Err() != nil {
		errl.Printf("Error reading domain list %s: %v\n", domainListFile, scanner.Err())
	}
	debug.Printf("loaded %d routing rules from %s as type %v", loaded, domainListFile, domainType)
}
