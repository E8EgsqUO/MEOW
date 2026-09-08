package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func readExactly(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %q, want %q", got, want)
	}
}

func waitForTunnel(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && err != io.EOF && !errors.Is(err, io.ErrClosedPipe) && !isErrConnReset(err) {
			t.Fatalf("tunnel returned unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tunnel did not stop after one side closed")
	}
}

func TestConnectTunnelStopsWhenUpstreamCloses(t *testing.T) {
	proxyClient, browser := net.Pipe()
	proxyServer, upstream := net.Pipe()
	defer browser.Close()
	defer upstream.Close()

	c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http")}
	sv := newServerConn(directConn{proxyServer}, "example.com:443")
	r := &Request{Method: "CONNECT", URL: &URL{HostPort: "example.com:443"}, isConnect: true}
	done := make(chan error, 1)
	go func() { done <- sv.doConnect(r, c) }()

	readExactly(t, browser, connEstablished)
	go func() { _, _ = browser.Write([]byte("client data")) }()
	readExactly(t, upstream, []byte("client data"))
	go func() { _, _ = upstream.Write([]byte("server data")) }()
	readExactly(t, browser, []byte("server data"))

	_ = upstream.Close()
	waitForTunnel(t, done)
}

func TestRelayForwardsBufferedPayloadAndStops(t *testing.T) {
	proxyClient, relayClient := net.Pipe()
	targetConn, targetPeer := net.Pipe()
	defer proxyClient.Close()
	defer targetPeer.Close()

	p := newRelayProxy("127.0.0.1:2048?dial=1s")
	p.dial = func(network, address string) (net.Conn, error) {
		if network != "tcp" || address != "example.com:443" {
			t.Fatalf("dial(%q, %q)", network, address)
		}
		return targetConn, nil
	}
	done := make(chan error, 1)
	go func() {
		p.handleConn(context.Background(), relayClient)
		done <- nil
	}()

	go func() { _, _ = proxyClient.Write([]byte("example.com:443\nbuffered payload")) }()
	readExactly(t, proxyClient, []byte("OK\n"))
	readExactly(t, targetPeer, []byte("buffered payload"))
	go func() { _, _ = targetPeer.Write([]byte("response")) }()
	readExactly(t, proxyClient, []byte("response"))

	_ = proxyClient.Close()
	waitForTunnel(t, done)
}

func TestRelayConfigParameters(t *testing.T) {
	p := newRelayParent("127.0.0.1:2048?ack=3s")
	if p.addr != "127.0.0.1:2048" || p.ackTimeout != 3*time.Second {
		t.Fatalf("relay parent parsed as addr=%q timeout=%v", p.addr, p.ackTimeout)
	}
	l := newRelayProxy("127.0.0.1:2048?dial=4s")
	if l.addr != "127.0.0.1:2048" || l.dialTimeout != 4*time.Second {
		t.Fatalf("relay listener parsed as addr=%q timeout=%v", l.addr, l.dialTimeout)
	}
}
