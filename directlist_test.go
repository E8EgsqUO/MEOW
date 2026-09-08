package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
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
	domainList.add("chatgpt.com", domainTypeProxy)
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
