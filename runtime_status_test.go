package main

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type statusTestConn struct {
	partialWriteConn
	remote net.Addr
}

func (c *statusTestConn) RemoteAddr() net.Addr {
	if c.remote != nil {
		return c.remote
	}
	return testNetAddr("127.0.0.1:12345")
}

func resetRuntimeStatusForTest() {
	runtimeStatus.Lock()
	runtimeStatus.started = time.Now()
	runtimeStatus.routes = [routeHistorySize]routeStatus{}
	runtimeStatus.routeNext = 0
	runtimeStatus.routeLen = 0
	runtimeStatus.routeTotals = [4]uint64{}
	runtimeStatus.errors = [errorHistorySize]errorStatus{}
	runtimeStatus.errorNext = 0
	runtimeStatus.errorLen = 0
	runtimeStatus.Unlock()
}

func TestRuntimeStatusRecordsRoutesAndAggregatesErrors(t *testing.T) {
	resetRuntimeStatusForTest()
	recordRouteStatus("example.com", domainTypeDirect)
	recordRouteStatus("example.com", domainTypeDirect)
	recordRouteStatus("blocked.example", domainTypeProxy)
	recordRouteStatusNamed("fallback.example", "PROXY (fallback)", domainTypeProxy)
	err := errors.New("parent rejected target")
	recordRuntimeError("blocked.example:443", err)
	recordRuntimeError("blocked.example:443", err)

	view := snapshotRuntimeStatus("")
	if view.DirectTotal != 2 || view.ProxyTotal != 2 || len(view.Routes) != 3 {
		t.Fatalf("unexpected route snapshot: %+v", view)
	}
	if view.Routes[2].Host != "example.com" || view.Routes[2].Count != 2 {
		t.Fatalf("consecutive route was not aggregated: %+v", view.Routes)
	}
	if len(view.Errors) != 1 || view.Errors[0].Count != 2 {
		t.Fatalf("errors were not aggregated: %+v", view.Errors)
	}
}

func TestRuntimeStatusTemplateEscapesHostnames(t *testing.T) {
	resetRuntimeStatusForTest()
	recordRouteStatus("<script>alert(1)</script>", domainTypeProxy)
	var out bytes.Buffer
	if err := runtimeStatusTemplate.Execute(&out, snapshotRuntimeStatus("")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>alert") || !strings.Contains(out.String(), "&lt;script&gt;") {
		t.Fatalf("status output did not escape hostname: %s", out.String())
	}
}

func TestAbsoluteStatusURLIsRecognizedAsSelfRequest(t *testing.T) {
	savedProxies, savedAddrs := listenProxy, selfListenAddr
	defer func() { listenProxy, selfListenAddr = savedProxies, savedAddrs }()
	listenProxy = []Proxy{newHttpProxy("127.0.0.1:4411", "", "http")}
	initSelfListenAddr()
	url := mustURL(t, "http://127.0.0.1:4411/status")
	if !isSelfRequest(&Request{URL: url}) {
		t.Fatal("absolute status URL sent through MEOW was not recognized as local")
	}
}

func TestRuntimeStatusPageIsServedToLoopback(t *testing.T) {
	resetRuntimeStatusForTest()
	conn := &statusTestConn{partialWriteConn: partialWriteConn{maxWrite: 4096}}
	client := &clientConn{Conn: conn}
	req := &Request{URL: mustURL(t, "http://127.0.0.1/status")}
	if err := sendRuntimeStatus(client, req); err != errPageSent {
		t.Fatalf("status response error = %v", err)
	}
	if !bytes.HasPrefix(conn.Bytes(), []byte("HTTP/1.1 200 OK\r\n")) || !bytes.Contains(conn.Bytes(), []byte("MEOW "+version)) {
		t.Fatalf("unexpected status response: %q", conn.Bytes())
	}
}

func TestRuntimeStatusPageRejectsNonLoopbackClient(t *testing.T) {
	resetRuntimeStatusForTest()
	recordRouteStatus("private.example", domainTypeProxy)
	conn := &statusTestConn{
		partialWriteConn: partialWriteConn{maxWrite: 4096},
		remote:           testNetAddr("192.0.2.10:12345"),
	}
	client := &clientConn{Conn: conn}
	req := &Request{URL: mustURL(t, "http://192.0.2.10/status")}
	if err := sendRuntimeStatus(client, req); err != errPageSent {
		t.Fatalf("status response error = %v", err)
	}
	if !bytes.HasPrefix(conn.Bytes(), []byte("HTTP/1.1 403 Forbidden\r\n")) {
		t.Fatalf("unexpected status response: %q", conn.Bytes())
	}
	if bytes.Contains(conn.Bytes(), []byte("private.example")) {
		t.Fatal("forbidden status response disclosed route history")
	}
}
