package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJudge(t *testing.T) {
	domainList := newDomainList()
	router := newDomainRouter(domainList)
	options := RouteOptions{ParentAvailable: true}

	domainList.Domain["com.cn"] = domainTypeDirect
	domainList.Domain["edu.cn"] = domainTypeDirect
	domainList.Domain["baidu.com"] = domainTypeDirect

	g, _ := ParseRequestURI("gtemp.com")
	if router.Route(context.Background(), g, options) != domainTypeProxy {
		t.Error("never visited site should be considered using proxy when judgeByIP is disabled")
	}

	directDomains := []string{
		"baidu.com",
		"www.baidu.com",
		"www.ahut.edu.cn",
	}
	for _, domain := range directDomains {
		url, _ := ParseRequestURI(domain)
		if router.Route(context.Background(), url, options) != domainTypeDirect {
			t.Errorf("domain %s in direct list should be considered using direct, host: %s", domain, url.Host)
		}
	}

}

func TestRouterHandlesEmptyDNSAnswer(t *testing.T) {
	domainList := newDomainList()
	router := newDomainRouter(domainList)
	router.lookupIP = func(context.Context, string) ([]net.IP, error) { return nil, nil }
	url, err := ParseRequestURI("empty.example")
	if err != nil {
		t.Fatal(err)
	}
	got := router.Route(context.Background(), url, RouteOptions{ParentAvailable: true, JudgeByIP: true})
	if got != domainTypeProxy {
		t.Fatalf("route = %v, want proxy after an empty DNS answer", got)
	}
}

func TestRouterPassesCancellationToDNS(t *testing.T) {
	domainList := newDomainList()
	router := newDomainRouter(domainList)
	lookupCalled := false
	router.lookupIP = func(ctx context.Context, _ string) ([]net.IP, error) {
		lookupCalled = true
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	url, err := ParseRequestURI("canceled.example")
	if err != nil {
		t.Fatal(err)
	}
	got := router.Route(ctx, url, RouteOptions{ParentAvailable: true, JudgeByIP: true})
	if !lookupCalled {
		t.Fatal("router did not pass the request to its DNS resolver")
	}
	if got != domainTypeProxy {
		t.Fatalf("route = %v, want proxy after canceled DNS lookup", got)
	}
}

func TestRouterExplicitProxyRuleSkipsDNS(t *testing.T) {
	domainList := newDomainList()
	domainList.Domain["chatgpt.com"] = domainTypeProxy
	router := newDomainRouter(domainList)
	router.lookupIP = func(context.Context, string) ([]net.IP, error) {
		t.Fatal("DNS lookup must not run for an explicit proxy rule")
		return nil, nil
	}
	for _, host := range []string{"chatgpt.com", "ab.chatgpt.com"} {
		url, err := ParseRequestURI(host + ":443")
		if err != nil {
			t.Fatal(err)
		}
		got := router.Route(context.Background(), url, RouteOptions{
			ParentAvailable: true,
			JudgeByIP:       true,
		})
		if got != domainTypeProxy {
			t.Fatalf("route for %s = %v, want proxy", host, got)
		}
	}
}

func TestInitDomainListsUsesFinalConfiguredPaths(t *testing.T) {
	dir := t.TempDir()
	directFile := filepath.Join(dir, "my-direct")
	proxyFile := filepath.Join(dir, "my-proxy")
	rejectFile := filepath.Join(dir, "my-reject")
	for path, content := range map[string]string{
		directFile: "direct.example\n",
		proxyFile:  "chatgpt.com\n",
		rejectFile: "reject.example\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	domainList := newDomainList()
	initDomainLists(domainList, Config{
		DirectFile: directFile,
		ProxyFile:  proxyFile,
		RejectFile: rejectFile,
	})
	for domain, want := range map[string]DomainType{
		"direct.example": domainTypeDirect,
		"chatgpt.com":    domainTypeProxy,
		"reject.example": domainTypeReject,
	} {
		if got := domainList.Domain[domain]; got != want {
			t.Fatalf("loaded rule for %s = %v, want %v", domain, got, want)
		}
	}
}

func TestLearnedRoutesMatchOnlyExactHost(t *testing.T) {
	for _, apexDirect := range []bool{true, false} {
		t.Run(directOrProxy(apexDirect), func(t *testing.T) {
			router := newDomainRouter(newDomainList())
			lookups := make(map[string]int)
			router.lookupIP = func(_ context.Context, host string) ([]net.IP, error) {
				lookups[host]++
				if (host == "example.com") == apexDirect {
					return []net.IP{net.ParseIP("127.0.0.1")}, nil
				}
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			}
			opts := RouteOptions{ParentAvailable: true, JudgeByIP: true}
			for i := 0; i < 2; i++ {
				for _, host := range []string{"example.com", "www.example.com", "cdn.example.com"} {
					want := domainTypeProxy
					if (host == "example.com") == apexDirect {
						want = domainTypeDirect
					}
					if got := router.Route(context.Background(), mustURL(t, host), opts); got != want {
						t.Fatalf("route for %s = %v, want %v", host, got, want)
					}
				}
			}
			for host, count := range lookups {
				if count != 1 {
					t.Errorf("DNS lookups for %s = %d, want 1 (exact-host cache)", host, count)
				}
			}
		})
	}
}

func TestConfiguredRulesTakePrecedenceOverLearnedRoutes(t *testing.T) {
	for _, rule := range []DomainType{domainTypeDirect, domainTypeProxy, domainTypeReject} {
		dl := newDomainList()
		dl.Domain["example.com"] = rule
		cached := domainTypeDirect
		if rule == domainTypeDirect {
			cached = domainTypeProxy
		}
		dl.add("www.example.com", cached)
		router := newDomainRouter(dl)
		got := router.Route(context.Background(), mustURL(t, "www.example.com"), RouteOptions{ParentAvailable: true})
		if got != rule {
			t.Errorf("configured rule %v overridden by learned route %v", rule, got)
		}
	}
}

func TestPACRulesExcludeLearnedHosts(t *testing.T) {
	dl := newDomainList()
	dl.Domain["manual.example"] = domainTypeDirect
	dl.add("example.com", domainTypeDirect)
	dl.add("manual.example", domainTypeProxy)
	rules := dl.GetDomainList()
	if len(rules) != 1 || rules[0] != "manual.example" {
		t.Fatalf("PAC suffix rules = %v, want only configured direct rule", rules)
	}
}

func TestConfiguredRuleMatchesCanonicalHostname(t *testing.T) {
	dl := newDomainList()
	dl.Domain["example.com"] = domainTypeReject
	router := newDomainRouter(dl)
	for _, raw := range []string{"http://EXAMPLE.COM/", "http://example.com./"} {
		if got := router.Route(context.Background(), mustURL(t, raw), RouteOptions{ParentAvailable: true}); got != domainTypeReject {
			t.Errorf("route for %s = %v, want reject", raw, got)
		}
	}
}

func TestLoadedRulesAreCanonicalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "direct")
	if err := os.WriteFile(path, []byte("EXAMPLE.COM.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dl := newDomainList()
	initDomainList(dl, path, domainTypeDirect)
	if dl.Domain["example.com"] != domainTypeDirect {
		t.Fatalf("loaded rules = %v, want canonical example.com", dl.Domain)
	}
}

func TestLookupGroupCoalescesConcurrentQueries(t *testing.T) {
	var group lookupGroup
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	lookup := func(context.Context, string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return []net.IP{net.ParseIP("223.5.5.5")}, nil
	}

	const clients = 32
	start := make(chan struct{})
	results := make(chan []net.IP, clients)
	var ready sync.WaitGroup
	ready.Add(clients)
	for i := 0; i < clients; i++ {
		go func() {
			ready.Done()
			<-start
			addrs, err := group.do(context.Background(), "example.com", lookup)
			if err != nil {
				results <- nil
				return
			}
			results <- addrs
		}()
	}
	ready.Wait()
	close(start)
	<-entered
	// Give every released goroutine a chance to join the in-flight call.
	time.Sleep(10 * time.Millisecond)
	close(release)
	for i := 0; i < clients; i++ {
		if addrs := <-results; len(addrs) != 1 {
			t.Fatalf("lookup %d returned %v", i, addrs)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying lookups = %d, want 1", got)
	}
}

func TestLookupGroupWaiterHonorsCancellation(t *testing.T) {
	var group lookupGroup
	entered := make(chan struct{})
	release := make(chan struct{})
	lookup := func(context.Context, string) ([]net.IP, error) {
		close(entered)
		<-release
		return []net.IP{net.ParseIP("223.5.5.5")}, nil
	}
	leaderDone := make(chan struct{})
	go func() {
		_, _ = group.do(context.Background(), "example.com", lookup)
		close(leaderDone)
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := group.do(ctx, "example.com", lookup); err != context.Canceled {
		t.Fatalf("waiter error = %v, want context canceled", err)
	}
	close(release)
	<-leaderDone
}
