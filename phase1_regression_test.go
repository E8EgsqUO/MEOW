package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingParent struct{}

func (failingParent) connect(context.Context, *URL) (net.Conn, error) {
	return nil, errors.New("failed")
}
func (failingParent) getServer() string { return "failing-parent" }
func (failingParent) genConfig() string { return "" }

func TestAuthTimeoutUsesConfiguredDuration(t *testing.T) {
	originalConfig := config
	originalAuth := auth
	defer func() {
		config = originalConfig
		auth = originalAuth
	}()

	config.UserPasswd = "user:password"
	config.UserPasswdFile = ""
	config.AllowedClient = ""
	config.AuthTimeout = 2 * time.Hour
	auth.required = false
	auth.user = nil
	auth.allowedClient = nil
	auth.authed = nil
	initAuth()
	if auth.authed == nil || auth.authed.timeout != config.AuthTimeout {
		t.Fatalf("authentication timeout = %v, want %v", auth.authed.timeout, config.AuthTimeout)
	}
}

func TestDomainListConcurrentReadWrite(t *testing.T) {
	dl := newDomainList()
	router := newDomainRouter(dl)
	url, err := ParseRequestURI("example.com")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				dl.add("example.com", domainTypeDirect)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = dl.GetDomainList()
				_ = router.Route(context.Background(), url, RouteOptions{ParentAvailable: true})
			}
		}()
	}
	wg.Wait()
}

func TestParseResponseRejectsShortProtocol(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()

	go func() {
		_, _ = peer.Write([]byte("X 200 OK\r\n\r\n"))
	}()
	sv := newServerConn(server, "example.com:80")
	sv.initBuf()
	r := &Request{Method: "GET", URL: &URL{HostPort: "example.com:80"}}
	var rp Response
	if err := parseResponse(sv, r, &rp); err == nil {
		t.Fatal("short response protocol must be rejected")
	}
}

func TestHTTP10ResponseIsNotPersistentByDefault(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	go func() {
		_ = writeFull(peer, []byte("HTTP/1.0 200 OK\r\nContent-Length: 0\r\n\r\n"))
	}()
	sv := newServerConn(server, "example.com:80")
	sv.initBuf()
	r := &Request{Method: "GET", URL: &URL{HostPort: "example.com:80"}}
	var response Response
	defer response.releaseBuf()
	if err := parseResponse(sv, r, &response); err != nil {
		t.Fatal(err)
	}
	if response.ConnectionKeepAlive {
		t.Fatal("HTTP/1.0 response without keep-alive was marked reusable")
	}
}

func TestParseHeaderRejectsConflictingContentLength(t *testing.T) {
	var h Header
	raw := strings.NewReader("Content-Length: 1\r\nContent-Length: 2\r\n\r\n")
	if err := h.parseHeader(bufio.NewReader(raw), new(bytes.Buffer), nil); err == nil {
		t.Fatal("conflicting Content-Length headers must be rejected")
	}
}

func TestParseRequestRejectsContentLengthWithTransferEncoding(t *testing.T) {
	proxySide, clientSide := net.Pipe()
	defer proxySide.Close()
	defer clientSide.Close()

	c := newClientConn(context.Background(), proxySide, newHttpProxy("127.0.0.1:4411", "", "http"))
	defer c.releaseBuf()
	go func() {
		_, _ = clientSide.Write([]byte("POST http://example.com/ HTTP/1.1\r\n" +
			"Content-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n"))
	}()
	var r Request
	defer r.releaseBuf()
	if err := parseRequest(c, &r); err == nil {
		t.Fatal("request with both Content-Length and Transfer-Encoding must be rejected")
	}
}

func TestParseRequestRejectsUnsupportedHTTPVersion(t *testing.T) {
	proxySide, clientSide := net.Pipe()
	defer proxySide.Close()
	defer clientSide.Close()

	c := newClientConn(context.Background(), proxySide, newHttpProxy("127.0.0.1:4411", "", "http"))
	defer c.releaseBuf()
	go func() {
		_, _ = clientSide.Write([]byte("CONNECT example.com:443 HTTP/2.0\r\nHost: example.com:443\r\n\r\n"))
	}()
	var r Request
	defer r.releaseBuf()
	if err := parseRequest(c, &r); err == nil {
		t.Fatal("unsupported request protocol must be rejected")
	}
}

func TestSplitHeaderRejectsAmbiguousSyntax(t *testing.T) {
	for _, line := range [][]byte{
		[]byte("Bad Header: value\r\n"),
		[]byte("Header\x00: value\r\n"),
		[]byte("Header: value\x00more\r\n"),
	} {
		if _, _, err := splitHeader(line); err == nil {
			t.Fatalf("splitHeader(%q) accepted invalid syntax", line)
		}
	}
}

func TestParseResponseRejectsInvalidStatusCode(t *testing.T) {
	for _, raw := range []string{
		"HTTP/1.1 20 OK\r\nContent-Length: 0\r\n\r\n",
		"HTTP/1.1 1000 Nope\r\nContent-Length: 0\r\n\r\n",
	} {
		sv := &serverConn{bufRd: bufio.NewReader(strings.NewReader(raw))}
		r := &Request{Method: "GET", URL: &URL{HostPort: "example.com:80"}}
		var response Response
		if err := parseResponse(sv, r, &response); err == nil {
			t.Fatalf("parseResponse accepted invalid status line %q", raw)
		}
		response.releaseBuf()
	}
}

func TestParentFailureCounterConcurrent(t *testing.T) {
	p := ParentWithFail{ParentProxy: failingParent{}}
	url := &URL{HostPort: "example.com:80", Host: "example.com", Port: "80"}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.connect(context.Background(), url)
		}()
	}
	wg.Wait()
}

func TestCanceledParentAttemptDoesNotCountAsFailure(t *testing.T) {
	p := ParentWithFail{ParentProxy: failingParent{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = p.connect(ctx, &URL{HostPort: "example.com:80"})
	if p.fail != 0 {
		t.Fatalf("canceled attempt increased failure count to %d", p.fail)
	}
}

func TestServeClientStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	proxySide, peer := net.Pipe()
	defer peer.Close()
	c := newClientConn(ctx, proxySide, newHttpProxy("127.0.0.1:4411", "", "http"))
	done := make(chan struct{})
	go func() {
		serveClient(ctx, c)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client handler did not stop after cancellation")
	}
}

func TestServerReadTimeout(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	sv := newServerConn(conn, "example.com:80")
	sv.readTimeout = 10 * time.Millisecond
	started := time.Now()
	_, err := sv.Read(make([]byte, 1))
	if err == nil || !isErrTimeout(err) {
		t.Fatalf("read error = %v, want timeout", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("configured read timeout was not applied")
	}
	_ = sv.Close()
}

func FuzzParseRequestURI(f *testing.F) {
	for _, seed := range []string{"", "/", "example.com", "http://[::1]/", "://"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = ParseRequestURI(raw)
	})
}

func FuzzParseResponse(f *testing.F) {
	for _, seed := range []string{
		"X 200 OK\r\n\r\n",
		"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n",
		"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Length: 1\r\n\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		sv := &serverConn{bufRd: bufio.NewReader(strings.NewReader(raw))}
		r := &Request{Method: "GET", URL: &URL{HostPort: "example.com:80"}}
		var rp Response
		_ = parseResponse(sv, r, &rp)
		rp.releaseBuf()
	})
}
