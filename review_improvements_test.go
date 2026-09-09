package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
)

// --- copyN clean EOF at the exact content-length boundary (#3) ------------------

// eofWithLastBytesReader hands back the final bytes together with io.EOF, the
// way a TLS record layer or bytes.Reader does.
type eofWithLastBytesReader struct{ data []byte }

func (r *eofWithLastBytesReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func TestCopyNAcceptsEOFDeliveredWithFinalBytes(t *testing.T) {
	const body = "the quick brown fox"
	src := bufio.NewReader(&eofWithLastBytesReader{data: []byte(body)})
	var dst bytes.Buffer
	if err := copyN(&dst, src, int64(len(body)), 8); err != nil {
		t.Fatalf("copyN returned %v for a complete body", err)
	}
	if dst.String() != body {
		t.Fatalf("copied %q, want %q", dst.String(), body)
	}
}

func TestCopyNStillReportsATruncatedBody(t *testing.T) {
	src := bufio.NewReader(strings.NewReader("short"))
	if err := copyN(io.Discard, src, 64, 8); err != io.ErrUnexpectedEOF {
		t.Fatalf("copyN error = %v, want ErrUnexpectedEOF", err)
	}
}

// --- adaptive tunnel buffer (#4) ----------------------------------------------

func TestTunnelBufGrowsOnlyAfterSustainedFullReads(t *testing.T) {
	tb := newTunnelBuf()
	defer tb.release()
	if len(tb.buf) != connectBufSmallSize {
		t.Fatalf("initial buffer %d, want %d", len(tb.buf), connectBufSmallSize)
	}
	for i := 0; i < tunnelGrowThreshold-1; i++ {
		tb.observe(len(tb.buf))
	}
	if tb.large {
		t.Fatal("buffer grew before the threshold")
	}
	tb.observe(len(tb.buf))
	if !tb.large || len(tb.buf) != connectBufLargeSize {
		t.Fatalf("buffer did not grow: large=%v size=%d", tb.large, len(tb.buf))
	}
}

func TestTunnelBufStaysSmallForBurstyTraffic(t *testing.T) {
	tb := newTunnelBuf()
	defer tb.release()
	for i := 0; i < 100; i++ {
		tb.observe(len(tb.buf)) // a full read
		tb.observe(1)           // followed by a trickle resets the streak
	}
	if tb.large {
		t.Fatal("buffer grew for traffic that never sustained full reads")
	}
}

// --- /status routing, forget and reload (#2) --------------------------------

func TestIsStatusPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/status", true},
		{"/status?x=1", true},
		{"/status/forget?host=a.example", true},
		{"/status/reload", true},
		{"/statuses", false},
		{"/pac", false},
	} {
		if got := isStatusPath(tc.path); got != tc.want {
			t.Errorf("isStatusPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestStatusForgetDropsALearnedRoute(t *testing.T) {
	saved := domainList
	domainList = newDomainList()
	defer func() { domainList = saved }()

	domainList.add("blocked.example", domainTypeProxy)
	if msg := statusForget("host=Blocked.Example."); !strings.Contains(msg, "blocked.example") {
		t.Fatalf("unexpected message: %q", msg)
	}
	if domainList.learnedCount() != 0 {
		t.Fatal("learned route was not dropped")
	}
	if msg := statusForget("host=blocked.example"); !strings.Contains(msg, "没有") {
		t.Fatalf("second forget message: %q", msg)
	}
	if msg := statusForget(""); !strings.Contains(msg, "未指定") {
		t.Fatalf("empty-host message: %q", msg)
	}
}

func TestStatusReloadClearsLearnedRoutes(t *testing.T) {
	savedList, savedConfig := domainList, config
	domainList = newDomainList()
	config = Config{}
	defer func() { domainList, config = savedList, savedConfig }()

	domainList.add("a.example", domainTypeProxy)
	domainList.add("b.example", domainTypeDirect)
	statusReload()
	if domainList.learnedCount() != 0 {
		t.Fatalf("reload left %d learned routes", domainList.learnedCount())
	}
}

// --- Expect: 100-continue relay (#9) ----------------------------------------

func newExpectBodyRequest(body string) *Request {
	r := new(Request)
	r.reset()
	r.Method = "POST"
	r.URL = &URL{Host: "example.com", Port: "80", HostPort: "example.com:80", Path: "/upload"}
	r.raw.WriteString("POST http://example.com/upload HTTP/1.1\r\n")
	r.reqLnStart = r.raw.Len()
	r.genRequestLine()
	r.headStart = r.raw.Len()
	r.raw.WriteString("Host: example.com\r\nExpect: 100-continue\r\nConnection: keep-alive\r\n\r\n")
	r.bodyStart = r.raw.Len()
	r.ContLen = int64(len(body))
	r.ExpectContinue = true
	return r
}

// runExpectExchange drives doRequest for an "Expect: 100-continue" POST. The
// upstream goroutine reads the request header, then sends interimResponse
// (which may be empty) followed by finalResponse. It returns what reached the
// client, the bytes the upstream received after its header, and doRequest's
// error.
func runExpectExchange(t *testing.T, body, interimResponse, finalResponse string) (clientGot, upstreamBody string, err error) {
	t.Helper()
	proxyUpstream, upstream := net.Pipe()
	proxyClient, browser := net.Pipe()
	defer upstream.Close()
	defer browser.Close()

	sv := newServerConn(proxyUpstream, "example.com:80")
	c := &clientConn{
		Conn:  proxyClient,
		bufRd: bufio.NewReader(strings.NewReader(body)),
		ctx:   context.Background(),
	}
	r := newExpectBodyRequest(body)
	defer r.releaseBuf()

	clientCh := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		io.Copy(&b, browser)
		clientCh <- b.String()
	}()

	upCh := make(chan string, 1)
	go func() {
		up := bufio.NewReader(upstream)
		for {
			line, e := up.ReadString('\n')
			if e != nil {
				upCh <- "read error: " + e.Error()
				return
			}
			if line == "\r\n" {
				break
			}
		}
		if interimResponse == "" {
			// The origin answers before it has read the body.
			io.WriteString(upstream, finalResponse)
			upstream.Close()
			upCh <- ""
			return
		}
		io.WriteString(upstream, interimResponse)
		got := make([]byte, len(body))
		n, _ := io.ReadFull(up, got)
		io.WriteString(upstream, finalResponse)
		upstream.Close()
		upCh <- string(got[:n])
	}()

	done := make(chan error, 1)
	var rp Response
	go func() { done <- sv.doRequest(c, r, &rp) }()

	err = <-done
	sv.Close() // unblock the upstream reader if the body was never forwarded
	browser.Close()
	return <-clientCh, <-upCh, err
}

func TestDoRequestRelaysContinueThenSendsBody(t *testing.T) {
	const body = "payload-bytes"
	clientGot, upstreamBody, err := runExpectExchange(t, body,
		"HTTP/1.1 100 Continue\r\n\r\n",
		"HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	if upstreamBody != body {
		t.Fatalf("upstream received body %q, want %q", upstreamBody, body)
	}
	if !strings.Contains(clientGot, "HTTP/1.1 100 Continue\r\n\r\n") {
		t.Fatalf("client never saw 100 Continue: %q", clientGot)
	}
	if !strings.Contains(clientGot, "204 No Content") {
		t.Fatalf("client never saw the final response: %q", clientGot)
	}
}

func TestDoRequestRelaysFinalResponseWithoutBody(t *testing.T) {
	const body = "payload-bytes"
	clientGot, upstreamBody, err := runExpectExchange(t, body,
		"", "HTTP/1.1 417 Expectation Failed\r\nContent-Length: 0\r\n\r\n")
	if err != errExpectRejected {
		t.Fatalf("doRequest error = %v, want errExpectRejected", err)
	}
	if upstreamBody != "" {
		t.Fatalf("upstream received body %q despite the rejection", upstreamBody)
	}
	if !strings.HasPrefix(clientGot, "HTTP/1.1 417") {
		t.Fatalf("client response = %q, want the 417 relayed", clientGot)
	}
}
