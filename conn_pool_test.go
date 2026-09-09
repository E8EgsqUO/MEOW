package main

import (
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type poolLogWriter func([]byte) (int, error)

func (w poolLogWriter) Write(p []byte) (int, error) { return w(p) }

func TestPoolCleanupDoesNotBlockWhenSlotIsRefilled(t *testing.T) {
	cp := newTestConnPool()
	ch := make(chan *serverConn, 1)
	sv, counted := newTestServerConn(t, "example.com:80", time.Now().Add(time.Minute))
	sv.Conn = meowConn{Conn: counted}
	replacement, _ := newTestServerConn(t, "example.com:80", time.Now().Add(time.Minute))
	defer replacement.Close()
	ch <- sv
	savedDebug, savedLog := debug, debugLog
	savedCounts := status.srvConnCnt
	debug = true
	initStat()
	defer func() { status.srvConnCnt = savedCounts }()
	// Refill precisely after cleanup receives sv, while mayBeClosed checks it.
	// This reproduces a concurrent Put without relying on scheduler timing.
	debugLog = log.New(poolLogWriter(func(p []byte) (int, error) {
		if strings.Contains(string(p), "meow parent would keep alive") {
			ch <- replacement
		}
		return len(p), nil
	}), "", 0)
	defer func() { debug, debugLog = savedDebug, savedLog }()
	done := make(chan struct{})
	go func() { cp.closeServerConn(ch, muxConnHostPort, false); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		<-ch // Unblock the old implementation before restoring global logging.
		<-done
		t.Fatal("cleanup blocked while returning a connection to a full channel")
	}
	if counted.closes.Load() != 1 {
		t.Fatal("connection displaced by concurrent Put was not closed")
	}
	if got := <-ch; got != replacement {
		t.Fatal("cleanup displaced the newly pooled connection")
	}
}

type countedConn struct {
	net.Conn
	closes atomic.Int32
}

func (c *countedConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func newTestServerConn(t *testing.T, hostPort string, closeOn time.Time) (*serverConn, *countedConn) {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	counted := &countedConn{Conn: conn}
	return &serverConn{Conn: counted, hostPort: hostPort, willCloseOn: closeOn}, counted
}

func newTestConnPool() *ConnPool {
	return &ConnPool{
		idleConn: make(map[string]chan *serverConn),
		muxConn:  make(chan *serverConn, maxServerConnCnt*2),
	}
}

func TestGetFromEmptyPool(t *testing.T) {
	cp := newTestConnPool()
	if sv := cp.Get("foo", true); sv != nil {
		t.Error("get non nil server conn from empty conn pool")
	}
}

func TestConnPool(t *testing.T) {
	cp := newTestConnPool()
	closeOn := time.Now().Add(10 * time.Second)
	hosts := []string{
		"example.com:80", "example.com:80", "example.com:80",
		"example.com:443", "google.com:443", "google.com:443", "www.google.com:80",
	}
	for _, host := range hosts {
		sv, _ := newTestServerConn(t, host, closeOn)
		cp.Put(sv)
	}
	t.Cleanup(cp.CloseAll)

	testData := []struct {
		hostPort string
		found    bool
	}{
		{"example.com", false},
		{"example.com:80", true},
		{"example.com:80", true},
		{"example.com:80", true},
		{"example.com:80", false},
		{"www.google.com:80", true},
	}

	for _, td := range testData {
		sv := cp.Get(td.hostPort, true)
		if td.found {
			if sv == nil {
				t.Error("should find conn for", td.hostPort)
			} else if sv.hostPort != td.hostPort {
				t.Errorf("hostPort should be: %s, got: %s", td.hostPort, sv.hostPort)
			}
		} else if sv != nil {
			t.Errorf("should NOT find conn for %s, got conn for: %s", td.hostPort, sv.hostPort)
		}
	}
}

func TestConnPoolConcurrentFirstPutDoesNotOrphanConnections(t *testing.T) {
	cp := newTestConnPool()
	const count = maxServerConnCnt
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		sv, _ := newTestServerConn(t, "example.com:80", time.Now().Add(time.Minute))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cp.Put(sv)
		}()
	}
	close(start)
	wg.Wait()
	t.Cleanup(cp.CloseAll)

	for i := 0; i < count; i++ {
		if sv := cp.Get("example.com:80", true); sv == nil {
			t.Fatalf("connection %d was orphaned", i)
		} else {
			_ = sv.Close()
		}
	}
}

func TestServerConnCloseIsIdempotent(t *testing.T) {
	sv, conn := newTestServerConn(t, "example.com:80", time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sv.Close()
		}()
	}
	wg.Wait()
	if got := conn.closes.Load(); got != 1 {
		t.Fatalf("underlying Close called %d times, want 1", got)
	}
}

func TestConnPoolRejectsAlreadyStaleHTTPParentConnection(t *testing.T) {
	cp := newTestConnPool()
	sv, counted := newTestServerConn(t, "example.com:80", time.Now().Add(-time.Second))
	sv.Conn = httpConn{Conn: counted, parent: newHttpParent("proxy.example:8080")}
	cp.Put(sv)
	if got := cp.Get("example.com:80", false); got != nil {
		t.Fatal("pool returned an already stale HTTP parent connection")
	}
	if got := counted.closes.Load(); got != 1 {
		t.Fatalf("stale underlying connection closed %d times, want 1", got)
	}
}
