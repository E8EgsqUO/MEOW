package main

import (
	"io"
	"net"
	"testing"

	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
)

// overWriter reports more bytes written than it was given, like ss.Conn does
// on its first write (where the returned count includes the IV).
type overWriter struct{ extra int }

func (w overWriter) Write(p []byte) (int, error) { return len(p) + w.extra, nil }

func TestWriteFullRejectsInvalidWriteResult(t *testing.T) {
	if err := writeFull(overWriter{extra: 16}, make([]byte, 39)); err != errInvalidWrite {
		t.Errorf("writeFull with over-reporting writer: got %v, want %v", err, errInvalidWrite)
	}
}

// The first write on a shadowsocks connection prepends the IV, so the raw
// ss.Conn.Write returns len(b)+ivLen. ssConn must not leak that count to
// callers -- neither on the meow parent path nor on the meow listener path,
// where the same wrapper carries proxy responses back to the client.
func TestMeowConnWriteReportsPlaintextLen(t *testing.T) {
	cipher, err := ss.NewCipher("aes-128-cfb", "test-password")
	if err != nil {
		t.Fatal("create cipher:", err)
	}
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	go io.Copy(io.Discard, srv)

	sv := meowConn{ssConn{ss.NewConn(cli, cipher)}, nil}
	req := []byte("GET http://example.com/ HTTP/1.1\r\n\r\n")
	n, err := sv.Write(req)
	if err != nil {
		t.Fatal("write:", err)
	}
	if n != len(req) {
		t.Errorf("first write returned n=%d, want %d", n, len(req))
	}
	// The panic in issue: writeFull sliced p by an n larger than len(p).
	if err := writeFull(sv, req); err != nil {
		t.Errorf("writeFull over meowConn: %v", err)
	}
}
