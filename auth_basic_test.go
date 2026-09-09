package main

import (
	"encoding/base64"
	"testing"
)

func TestCheckProxyAuthorizationBasic(t *testing.T) {
	savedAuth := auth
	defer func() { auth = savedAuth }()
	auth.user = map[string]*authUser{"alice": {passwd: "secret"}}

	conn := &clientConn{Conn: &partialWriteConn{maxWrite: 4096}}

	ok := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if err := checkProxyAuthorization(conn, &Request{Header: Header{ProxyAuthorization: ok}}); err != nil {
		t.Fatalf("valid basic credentials rejected: %v", err)
	}

	bad := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:wrong"))
	if err := checkProxyAuthorization(conn, &Request{Header: Header{ProxyAuthorization: bad}}); err != errAuthRequired {
		t.Fatalf("wrong password error = %v, want errAuthRequired", err)
	}

	if err := checkProxyAuthorization(conn, &Request{Header: Header{ProxyAuthorization: "Bearer x"}}); err == nil {
		t.Fatal("unsupported scheme accepted")
	}
	if err := checkProxyAuthorization(conn, &Request{Header: Header{ProxyAuthorization: "garbage"}}); err == nil {
		t.Fatal("malformed header accepted")
	}
}
