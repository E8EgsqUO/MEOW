package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func newHTTPParentConnectRequest(t *testing.T) *Request {
	t.Helper()
	r := new(Request)
	r.reset()
	r.Method = "CONNECT"
	r.URL = &URL{Host: "example.com", Port: "443", HostPort: "example.com:443"}
	r.isConnect = true
	r.raw.WriteString("CONNECT example.com:443 HTTP/1.1\r\n")
	r.reqLnStart = r.raw.Len()
	r.headStart = r.raw.Len()
	r.raw.WriteString("Host: example.com:443\r\nConnection: keep-alive\r\n\r\n")
	r.bodyStart = r.raw.Len()
	return r
}

func newHTTPParentGETRequest(t *testing.T) *Request {
	t.Helper()
	r := new(Request)
	r.reset()
	r.Method = "GET"
	r.URL = &URL{Host: "example.com", Port: "80", HostPort: "example.com:80", Path: "/video"}
	r.raw.WriteString("GET http://example.com/video HTTP/1.1\r\n")
	r.reqLnStart = r.raw.Len()
	r.genRequestLine()
	r.headStart = r.raw.Len()
	r.raw.WriteString("Host: example.com\r\nConnection: keep-alive\r\n\r\n")
	r.bodyStart = r.raw.Len()
	return r
}

func readRawHTTPHeader(conn net.Conn) ([]byte, error) {
	var raw []byte
	one := make([]byte, 1)
	for len(raw) <= 64<<10 {
		if _, err := io.ReadFull(conn, one); err != nil {
			return nil, err
		}
		raw = append(raw, one[0])
		if bytes.HasSuffix(raw, []byte("\r\n\r\n")) {
			return raw, nil
		}
	}
	return nil, errors.New("header too large")
}

func TestHTTPUpstreamConnectFragmentedSuccessPreservesBufferedTunnelData(t *testing.T) {
	proxyClient, browser := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	defer browser.Close()
	defer upstream.Close()

	parent := newHttpParent("proxy.example:8080")
	parent.initAuth("user:password")
	sv := newServerConn(httpConn{Conn: proxyUpstream, parent: parent}, "example.com:443")
	c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http"), ctx: context.Background()}
	r := newHTTPParentConnectRequest(t)
	done := make(chan error, 1)
	go func() { done <- sv.doConnect(r, c) }()

	serverDone := make(chan error, 1)
	go func() {
		defer upstream.Close()
		header, err := readRawHTTPHeader(upstream)
		if err != nil {
			serverDone <- err
			return
		}
		if !bytes.Contains(bytes.ToLower(header), []byte("proxy-authorization: basic dxnlcjpwyxnzd29yza==\r\n")) {
			serverDone <- fmt.Errorf("missing proxy authorization header: %q", header)
			return
		}
		if err = upstream.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			serverDone <- err
			return
		}
		var early [1]byte
		if _, readErr := upstream.Read(early[:]); readErr == nil || !isErrTimeout(readErr) {
			serverDone <- fmt.Errorf("client tunnel bytes reached upstream before CONNECT 2xx: %v", readErr)
			return
		}
		_ = upstream.SetReadDeadline(time.Time{})
		for _, fragment := range [][]byte{
			[]byte("HTTP/1.1 200"),
			[]byte(" Connection established\r\nX-Test: fragmented\r\n"),
			[]byte("\r\nserver hello"),
		} {
			if err = writeFull(upstream, fragment); err != nil {
				serverDone <- err
				return
			}
		}
		payload := make([]byte, len("early client hello"))
		if _, err = io.ReadFull(upstream, payload); err != nil {
			serverDone <- err
			return
		}
		if string(payload) != "early client hello" {
			serverDone <- fmt.Errorf("upstream payload = %q", payload)
			return
		}
		serverDone <- nil
	}()

	clientWrite := make(chan error, 1)
	go func() { clientWrite <- writeFull(browser, []byte("early client hello")) }()
	readExactly(t, browser, []byte("HTTP/1.1 200 Connection established\r\nX-Test: fragmented\r\n\r\n"))
	readExactly(t, browser, []byte("server hello"))
	if err := <-clientWrite; err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	_ = upstream.Close()
	waitForTunnel(t, done)
}

func TestHTTPUpstreamConnectClientDisconnectCancelsPendingResponse(t *testing.T) {
	proxyClient, browser := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	defer upstream.Close()

	parent := newHttpParent("proxy.example:8080")
	sv := newServerConn(httpConn{Conn: proxyUpstream, parent: parent}, "example.com:443")
	c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http"), ctx: context.Background()}
	r := newHTTPParentConnectRequest(t)
	defer r.releaseBuf()
	done := make(chan error, 1)
	go func() { done <- sv.doConnect(r, c) }()

	headerRead := make(chan error, 1)
	go func() {
		_, err := readRawHTTPHeader(upstream)
		headerRead <- err
	}()
	if err := <-headerRead; err != nil {
		t.Fatal(err)
	}
	_ = browser.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pending CONNECT unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("client disconnect did not cancel pending upstream CONNECT")
	}
}

func TestHTTPUpstreamConnectResponseTimeout(t *testing.T) {
	original := config.ReadTimeout
	config.ReadTimeout = 20 * time.Millisecond
	defer func() { config.ReadTimeout = original }()

	proxyClient, browser := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	defer browser.Close()
	defer upstream.Close()
	parent := newHttpParent("proxy.example:8080")
	sv := newServerConn(httpConn{Conn: proxyUpstream, parent: parent}, "example.com:443")
	c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http"), ctx: context.Background()}
	r := newHTTPParentConnectRequest(t)
	defer r.releaseBuf()
	done := make(chan error, 1)
	go func() { done <- sv.doConnect(r, c) }()

	headerRead := make(chan error, 1)
	go func() {
		_, err := readRawHTTPHeader(upstream)
		headerRead <- err
	}()
	if err := <-headerRead; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !isErrTimeout(err) {
			t.Fatalf("CONNECT response error = %v, want timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CONNECT response timeout was not enforced")
	}
}

func TestHTTPUpstreamConnectRejectionForwardsResponseWithoutStartingTunnel(t *testing.T) {
	tests := []struct {
		status   string
		response []byte
	}{
		{
			"407 Proxy Authentication Required",
			[]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 4\r\nConnection: keep-alive\r\n\r\ndeny"),
		},
		{
			"502 Bad Gateway",
			[]byte("HTTP/1.1 502 Bad Gateway\r\nTransfer-Encoding: chunked\r\n\r\n4\r\ndeny\r\n0\r\n\r\n"),
		},
		{
			"403 Forbidden",
			[]byte("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\ndeny"),
		},
	}
	for _, test := range tests {
		t.Run(test.status[:3], func(t *testing.T) {
			proxyClient, browser := net.Pipe()
			proxyUpstream, upstream := net.Pipe()
			defer browser.Close()
			defer upstream.Close()

			parent := newHttpParent("proxy.example:8080")
			sv := newServerConn(httpConn{Conn: proxyUpstream, parent: parent}, "example.com:443")
			c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http"), ctx: context.Background()}
			r := newHTTPParentConnectRequest(t)
			done := make(chan error, 1)
			go func() { done <- sv.doConnect(r, c) }()

			go func() {
				defer upstream.Close()
				_, _ = readRawHTTPHeader(upstream)
				_ = writeFull(upstream, test.response)
			}()
			readExactly(t, browser, test.response)
			select {
			case err := <-done:
				if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.status[:3])) {
					t.Fatalf("CONNECT error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("rejected CONNECT did not stop")
			}
		})
	}
}

func TestHTTPUpstreamConcurrentConnectTunnels(t *testing.T) {
	const tunnels = 64
	var wg sync.WaitGroup
	errs := make(chan error, tunnels)
	for i := 0; i < tunnels; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			proxyClient, browser := net.Pipe()
			proxyUpstream, upstream := net.Pipe()
			defer browser.Close()
			defer upstream.Close()
			parent := newHttpParent("proxy.example:8080")
			sv := newServerConn(httpConn{Conn: proxyUpstream, parent: parent}, "example.com:443")
			c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http"), ctx: context.Background()}
			r := newHTTPParentConnectRequest(t)
			done := make(chan error, 1)
			go func() { done <- sv.doConnect(r, c) }()
			go func() {
				_, _ = readRawHTTPHeader(upstream)
				_ = writeFull(upstream, []byte("HTTP/1.1 200 OK\r\n\r\n"))
				payload := make([]byte, 1)
				_, _ = io.ReadFull(upstream, payload)
				_, _ = upstream.Write(payload)
				_ = upstream.Close()
			}()
			if err := browser.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				errs <- err
				return
			}
			if _, err := io.ReadFull(browser, make([]byte, len("HTTP/1.1 200 OK\r\n\r\n"))); err != nil {
				errs <- fmt.Errorf("tunnel %d response: %w", id, err)
				return
			}
			if err := writeFull(browser, []byte{byte(id)}); err != nil {
				errs <- err
				return
			}
			payload := make([]byte, 1)
			if _, err := io.ReadFull(browser, payload); err != nil || payload[0] != byte(id) {
				errs <- fmt.Errorf("tunnel %d echo: %x %v", id, payload, err)
				return
			}
			_ = browser.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				errs <- fmt.Errorf("tunnel %d leaked", id)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHTTPUpstreamRegularKeepAliveResponse(t *testing.T) {
	proxyClient, browser := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	defer browser.Close()
	defer upstream.Close()
	parent := newHttpParent("proxy.example:8080")
	sv := newServerConn(httpConn{Conn: proxyUpstream, parent: parent}, "example.com:80")
	c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http"), ctx: context.Background()}
	r := newHTTPParentGETRequest(t)
	defer r.releaseBuf()
	var response Response
	done := make(chan error, 1)
	go func() { done <- sv.doRequest(c, r, &response) }()

	serverDone := make(chan error, 1)
	go func() {
		header, err := readRawHTTPHeader(upstream)
		if err != nil {
			serverDone <- err
			return
		}
		if !bytes.HasPrefix(header, []byte("GET http://example.com/video HTTP/1.1\r\n")) {
			serverDone <- fmt.Errorf("unexpected proxy request: %q", header)
			return
		}
		for _, fragment := range [][]byte{
			[]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n"),
			[]byte("Connection: keep-alive\r\nKeep-Alive: timeout=30\r\n\r\nhe"),
			[]byte("llo"),
		} {
			if err = writeFull(upstream, fragment); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	want := []byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: keep-alive\r\nKeep-Alive: timeout=5\r\n\r\nhello")
	readExactly(t, browser, want)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if !response.ConnectionKeepAlive || response.KeepAlive != 30*time.Second {
		t.Fatalf("upstream keep-alive parsed as enabled=%v timeout=%s", response.ConnectionKeepAlive, response.KeepAlive)
	}
	if string(response.Reason) != "OK" {
		t.Fatalf("response reason was overwritten: %q", response.Reason)
	}
	_ = sv.Close()
}

func TestInformationalResponseDoesNotReplaceFinalResponse(t *testing.T) {
	upstream := &fallbackResponseConn{reader: strings.NewReader(
		"HTTP/1.1 103 Early Hints\r\nLink: </a.css>; rel=preload\r\n\r\n" +
			"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")}
	sv := newServerConn(upstream, "example.com:80")
	defer sv.Close()
	client := &partialWriteConn{maxWrite: 4096}
	c := &clientConn{Conn: client, ctx: context.Background()}
	r := newHTTPParentGETRequest(t)
	defer r.releaseBuf()
	var response Response
	if err := c.readResponse(sv, r, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != 200 || !bytes.HasPrefix(client.Bytes(), []byte("HTTP/1.1 200 OK")) {
		t.Fatalf("final response status = %d, output = %q", response.Status, client.Bytes())
	}
}
