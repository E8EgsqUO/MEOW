package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func socksReplyServer(t *testing.T, conn net.Conn, reply byte) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		defer conn.Close()
		greeting := make([]byte, len(socksMsgVerMethodSelection))
		if _, err := io.ReadFull(conn, greeting); err != nil {
			done <- err
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			done <- err
			return
		}
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			done <- err
			return
		}
		var addressLength int
		switch header[3] {
		case 1:
			addressLength = net.IPv4len
		case 4:
			addressLength = net.IPv6len
		case 3:
			var length [1]byte
			if _, err := io.ReadFull(conn, length[:]); err != nil {
				done <- err
				return
			}
			addressLength = int(length[0])
		default:
			done <- errors.New("unexpected SOCKS address type")
			return
		}
		if _, err := io.CopyN(io.Discard, conn, int64(addressLength+2)); err != nil {
			done <- err
			return
		}
		_, err := conn.Write([]byte{5, reply, 0, 1})
		done <- err
	}()
	return done
}

func TestHTTPSParentTLSDefaults(t *testing.T) {
	original := config.ProxyTLSInsecureSkipVerify
	defer func() { config.ProxyTLSInsecureSkipVerify = original }()
	parent := newHttpsParent("proxy.example:443")

	config.ProxyTLSInsecureSkipVerify = false
	tlsConfig := parent.tlsConfig()
	if tlsConfig.InsecureSkipVerify {
		t.Fatal("HTTPS parent certificate verification is disabled by default")
	}
	if tlsConfig.MinVersion != tls.VersionTLS12 || tlsConfig.ServerName != "proxy.example" {
		t.Fatalf("unexpected TLS config: min=%x serverName=%q", tlsConfig.MinVersion, tlsConfig.ServerName)
	}

	config.ProxyTLSInsecureSkipVerify = true
	if !parent.tlsConfig().InsecureSkipVerify {
		t.Fatal("explicit insecure compatibility option was ignored")
	}
}

func TestSOCKS5IPv6RequestAndFragmentedIPv6Reply(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	serverErr := make(chan error, 1)
	go func() {
		defer clientSide.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(clientSide, greeting); err != nil {
			serverErr <- err
			return
		}
		for _, b := range []byte{5, 0} {
			if _, err := clientSide.Write([]byte{b}); err != nil {
				serverErr <- err
				return
			}
		}
		header := make([]byte, 4)
		if _, err := io.ReadFull(clientSide, header); err != nil {
			serverErr <- err
			return
		}
		if header[3] != 4 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		addressPort := make([]byte, net.IPv6len+2)
		if _, err := io.ReadFull(clientSide, addressPort); err != nil {
			serverErr <- err
			return
		}
		if !net.IP(addressPort[:net.IPv6len]).Equal(net.ParseIP("::1")) {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		reply := append([]byte{5, 0, 0, 4}, make([]byte, net.IPv6len+2)...)
		for _, b := range reply {
			if _, err := clientSide.Write([]byte{b}); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	parent := newSocksParent("unused:1080")
	parent.dial = func(context.Context, string, string) (net.Conn, error) {
		return serverSide, nil
	}
	conn, err := parent.connect(context.Background(), &URL{Host: "::1", Port: "443", HostPort: "[::1]:443"})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err = <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5HandshakeStopsWhenContextIsCanceled(t *testing.T) {
	original := config.ReadTimeout
	config.ReadTimeout = 0
	defer func() { config.ReadTimeout = original }()
	client, server := net.Pipe()
	defer server.Close()
	parent := newSocksParent("unused:1080")
	parent.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	url := mustURL(t, "example.com:443")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := parent.connect(ctx, url)
		done <- err
	}()
	greeting := make([]byte, len(socksMsgVerMethodSelection))
	if _, err := io.ReadFull(server, greeting); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt SOCKS5 handshake")
	}
}

func TestSOCKS5TargetFailureDoesNotPenalizeParent(t *testing.T) {
	proxySide, serverSide := net.Pipe()
	serverDone := socksReplyServer(t, proxySide, 4)

	parent := newSocksParent("[2001:db8::1]:1080")
	parent.dial = func(context.Context, string, string) (net.Conn, error) {
		return serverSide, nil
	}
	withFail := ParentWithFail{ParentProxy: parent, fail: 7}
	url := &URL{Host: "beacon.example", Port: "443", HostPort: "beacon.example:443"}
	_, err := withFail.connect(context.Background(), url)
	if err == nil {
		t.Fatal("SOCKS target rejection unexpectedly succeeded")
	}
	var replyErr *parentReplyError
	if !errors.As(err, &replyErr) || replyErr.reply != 4 {
		t.Fatalf("error = %v, want typed SOCKS reply 4", err)
	}
	if !errors.Is(err, socksProtocolErr) {
		t.Fatalf("error = %v, want compatibility with socksProtocolErr", err)
	}
	if got := atomic.LoadInt32(&withFail.fail); got != 0 {
		t.Fatalf("valid SOCKS response left parent failure score at %d, want 0", got)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestConnectTunnelCarriesHTTP2TLS(t *testing.T) {
	proxyClient, browser := net.Pipe()
	proxyTarget, target := net.Pipe()
	c := &clientConn{Conn: proxyClient, proxy: newHttpProxy("127.0.0.1:4411", "", "http")}
	sv := newServerConn(directConn{proxyTarget}, "localhost:443")
	r := &Request{Method: "CONNECT", URL: &URL{HostPort: "localhost:443"}, isConnect: true}
	tunnelDone := make(chan error, 1)
	go func() { tunnelDone <- sv.doConnect(r, c) }()
	readExactly(t, browser, connEstablished)

	certificate := testCertificate(t)
	serverDone := make(chan error, 1)
	go func() {
		defer target.Close()
		tlsServer := tls.Server(target, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			NextProtos:   []string{"h2"},
			MinVersion:   tls.VersionTLS12,
		})
		if err := tlsServer.Handshake(); err != nil {
			serverDone <- err
			return
		}
		payload := make([]byte, 4)
		if _, err := io.ReadFull(tlsServer, payload); err != nil {
			serverDone <- err
			return
		}
		_, err := tlsServer.Write([]byte("pong"))
		serverDone <- err
	}()

	tlsClient := tls.Client(browser, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2", "http/1.1"},
		MinVersion:         tls.VersionTLS12,
	})
	if err := tlsClient.Handshake(); err != nil {
		t.Fatal(err)
	}
	if got := tlsClient.ConnectionState().NegotiatedProtocol; got != "h2" {
		t.Fatalf("negotiated protocol = %q, want h2", got)
	}
	if _, err := tlsClient.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(tlsClient, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "pong" {
		t.Fatalf("tunnel response = %q", response)
	}
	_ = tlsClient.Close()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	waitForTunnel(t, tunnelDone)
}
