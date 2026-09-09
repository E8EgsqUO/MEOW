package main

import (
	"bufio"
	"context"
	"net"
	"os"
	"strings"
	"sync"
)

type DomainList struct {
	Domain map[string]DomainType
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
		Domain: map[string]DomainType{},
	}
}

// RouteOptions contains runtime facts which affect a routing decision. Keeping
// them as inputs prevents the router from depending on the global proxy pool or
// configuration while preserving the existing policy.
type RouteOptions struct {
	ParentAvailable bool
	JudgeByIP       bool
}

// Router decides how a request should leave MEOW. It does not establish the
// outbound connection.
type Router interface {
	Route(context.Context, *URL, RouteOptions) DomainType
}

type domainRouter struct {
	domains  *DomainList
	lookupIP func(context.Context, string) ([]net.IP, error)
}

func newDomainRouter(domains *DomainList) *domainRouter {
	return &domainRouter{
		domains: domains,
		lookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
	}
}

func (router *domainRouter) Route(ctx context.Context, url *URL, options RouteOptions) (domainType DomainType) {
	debug.Printf("judging host: %s", url.Host)
	domainList := router.domains
	domainList.RLock()
	hostType := domainList.Domain[url.Host]
	domainType = domainList.Domain[url.Domain]
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

	if !options.JudgeByIP {
		return domainTypeProxy
	}
	debug.Printf("judging by ip")
	var shouldDirect bool
	isIP, isPrivate := hostIsIP(url.Host)
	if isIP {
		if isPrivate {
			domainList.add(url.Host, domainTypeDirect)
			return domainTypeDirect
		}
		shouldDirect = ipShouldDirect(url.Host)
	} else {
		hostIPs, err := router.lookupIP(ctx, url.Host)
		if err != nil {
			errl.Printf("error looking up host ip %s, err %s", url.Host, err)
			return domainTypeProxy
		}
		if len(hostIPs) == 0 {
			errl.Printf("host lookup returned no addresses for %s", url.Host)
			return domainTypeProxy
		}
		// Weigh every answer instead of only the first one; see ipsShouldDirect.
		shouldDirect = ipsShouldDirect(hostIPs)
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
	domainList.Domain[host] = domainType
}

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
		domain := strings.TrimSpace(scanner.Text())
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
