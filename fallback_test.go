package main

import (
	"context"
	"errors"
	"net"
	"testing"
)

// stubParentPool stands in for the real parent pool so a test can decide
// whether the fallback connection succeeds without needing a live upstream.
type stubParentPool struct {
	conns   int
	targets []string
	err     error
}

func (p *stubParentPool) add(ParentProxy) {}

func (p *stubParentPool) empty() bool { return false }

func (p *stubParentPool) connect(_ context.Context, url *URL) (net.Conn, error) {
	p.conns++
	p.targets = append(p.targets, url.HostPort)
	if p.err != nil {
		return nil, p.err
	}
	client, server := net.Pipe()
	go func() { _, _ = server.Read(make([]byte, 1)) }()
	return client, nil
}

type emptyParentPool struct{}

func (emptyParentPool) add(ParentProxy) {}
func (emptyParentPool) empty() bool     { return true }
func (emptyParentPool) connect(context.Context, *URL) (net.Conn, error) {
	return nil, errors.New("no parent")
}

func withParentPool(t *testing.T, pool ParentPool) {
	t.Helper()
	saved := parentProxy
	parentProxy = pool
	t.Cleanup(func() { parentProxy = saved })
}

func withDirectFallback(t *testing.T, enabled bool) {
	t.Helper()
	saved := config.DirectFallback
	config.DirectFallback = enabled
	t.Cleanup(func() { config.DirectFallback = saved })
}

func mustURL(t *testing.T, raw string) *URL {
	t.Helper()
	url, err := ParseRequestURI(raw)
	if err != nil {
		t.Fatalf("ParseRequestURI(%q): %v", raw, err)
	}
	return url
}

func TestCanFallBackToParent(t *testing.T) {
	withDirectFallback(t, true)

	t.Run("public host falls back", func(t *testing.T) {
		withParentPool(t, &stubParentPool{})
		if !canFallBackToParent(mustURL(t, "example.com")) {
			t.Error("a public host should fall back to the parent")
		}
	})

	t.Run("public literal address falls back", func(t *testing.T) {
		withParentPool(t, &stubParentPool{})
		if !canFallBackToParent(mustURL(t, "93.184.216.34")) {
			t.Error("a public literal address should fall back to the parent")
		}
	})

	// A LAN host that does not answer is switched off, not blocked. Sending it
	// upstream cannot help and would leak an internal address.
	for _, host := range []string{"192.168.1.10", "10.0.0.5", "127.0.0.1", "intranet"} {
		t.Run("local host "+host+" does not fall back", func(t *testing.T) {
			withParentPool(t, &stubParentPool{})
			if canFallBackToParent(mustURL(t, host)) {
				t.Errorf("%s should not fall back to the parent", host)
			}
		})
	}

	t.Run("no parent means no fallback", func(t *testing.T) {
		withParentPool(t, emptyParentPool{})
		if canFallBackToParent(mustURL(t, "example.com")) {
			t.Error("fallback needs a parent proxy")
		}
	})

	t.Run("disabled by config", func(t *testing.T) {
		withParentPool(t, &stubParentPool{})
		withDirectFallback(t, false)
		if canFallBackToParent(mustURL(t, "example.com")) {
			t.Error("directFallback = false must disable the fallback")
		}
	})
}

func newTestClientConn() *clientConn {
	client, server := net.Pipe()
	go func() { _, _ = server.Read(make([]byte, 1)) }()
	return &clientConn{Conn: client, ctx: context.Background()}
}

func TestFallBackToParentRemembersTheHost(t *testing.T) {
	withDirectFallback(t, true)
	pool := &stubParentPool{}
	withParentPool(t, pool)

	saved := domainList
	domainList = newDomainList()
	t.Cleanup(func() { domainList = saved })

	c := newTestClientConn()
	defer c.Conn.Close()
	r := &Request{URL: mustURL(t, "blocked.example:443")}

	directErr := errors.New("connection reset by peer")
	conn, err := c.fallBackToParent(r, directErr)
	if err != nil {
		t.Fatalf("fallBackToParent returned %v, want success", err)
	}
	defer conn.Close()

	if pool.conns != 1 {
		t.Errorf("parent connect called %d times, want 1", pool.conns)
	}
	if len(pool.targets) != 1 || pool.targets[0] != "blocked.example:443" {
		t.Errorf("parent asked for %v, want [blocked.example:443]", pool.targets)
	}

	domainList.RLock()
	got := domainList.Domain["blocked.example"]
	domainList.RUnlock()
	if got != domainTypeProxy {
		t.Errorf("host verdict = %v, want proxy", got)
	}
}

// A site that is merely down fails both ways. Pinning it to the proxy would
// keep sending it upstream for the rest of the run for no reason.
func TestFallBackToParentForgetsWhenTheParentAlsoFails(t *testing.T) {
	withDirectFallback(t, true)
	pool := &stubParentPool{err: errors.New("upstream refused")}
	withParentPool(t, pool)

	saved := domainList
	domainList = newDomainList()
	t.Cleanup(func() { domainList = saved })

	c := newTestClientConn()
	defer c.Conn.Close()
	r := &Request{URL: mustURL(t, "down.example:80")}

	directErr := errors.New("connection refused")
	if _, err := c.fallBackToParent(r, directErr); !errors.Is(err, directErr) {
		t.Errorf("error = %v, want the original direct error surfaced to the client", err)
	}

	domainList.RLock()
	got := domainList.Domain["down.example"]
	domainList.RUnlock()
	if got != domainTypeUnknown {
		t.Errorf("host verdict = %v, want it left unrecorded", got)
	}
}

func TestFallBackToParentSkipsLocalHosts(t *testing.T) {
	withDirectFallback(t, true)
	pool := &stubParentPool{}
	withParentPool(t, pool)

	c := newTestClientConn()
	defer c.Conn.Close()
	r := &Request{URL: mustURL(t, "192.168.1.10:8080")}

	directErr := errors.New("connection refused")
	if _, err := c.fallBackToParent(r, directErr); !errors.Is(err, directErr) {
		t.Errorf("error = %v, want the original direct error", err)
	}
	if pool.conns != 0 {
		t.Errorf("parent connect called %d times for a LAN address, want 0", pool.conns)
	}
}

// End to end through clientConn.connect: the direct dial fails because nothing
// listens on the port, and the request must still come back with a usable
// connection from the parent.
func TestConnectFallsBackWhenDirectDialFails(t *testing.T) {
	withDirectFallback(t, true)
	pool := &stubParentPool{}
	withParentPool(t, pool)

	saved := domainList
	domainList = newDomainList()
	t.Cleanup(func() { domainList = saved })

	// Bind and immediately release a port so the dial is refused rather than
	// left hanging on a firewall.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c := newTestClientConn()
	defer c.Conn.Close()

	// Use a routable-looking hostname so the local-host guard does not fire,
	// while the port still points at nothing.
	_, port, _ := net.SplitHostPort(addr)
	r := &Request{URL: mustURL(t, "closed.example:"+port)}

	conn, err := c.connect(r, true)
	if err != nil {
		t.Fatalf("connect returned %v, want the parent connection", err)
	}
	defer conn.Close()
	if pool.conns != 1 {
		t.Errorf("parent connect called %d times, want 1", pool.conns)
	}
}
