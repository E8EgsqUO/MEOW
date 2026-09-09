package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

/*
=========================

	小工具：拆 addr 与查询参数
	例: "1.2.3.4:2048?ack=12s&dial=8s"
	=========================
*/
func splitAddrParams(raw string) (addr string, params map[string]string) {
	addr = raw
	params = make(map[string]string)
	if i := strings.Index(raw, "?"); i >= 0 {
		addr = raw[:i]
		qs := raw[i+1:]
		for _, kv := range strings.Split(qs, "&") {
			if kv == "" {
				continue
			}
			p := strings.SplitN(kv, "=", 2)
			if len(p) == 2 {
				params[p[0]] = p[1]
			}
		}
	}
	return
}

/* =========================
   Parent（proxy = relay://ip:port?ack=12s）
   - 发 "host:port\n"
   - 等 ACK: "OK\n"/"ERR\n"
   ========================= */

type relayParent struct {
	rawAddr    string
	addr       string
	ackTimeout time.Duration
}

func newRelayParent(raw string) *relayParent {
	addr, pm := splitAddrParams(raw)
	ack := 12 * time.Second // 建议：ack >= dial + 2s
	if v, ok := pm["ack"]; ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ack = d
		}
	}
	return &relayParent{
		rawAddr:    raw,
		addr:       addr,
		ackTimeout: ack,
	}
}

func (p *relayParent) genConfig() string { return "proxy = relay://" + p.rawAddr }
func (p *relayParent) getServer() string { return p.addr }

// URL -> host:port（兼容多种 Port 类型）
func urlHostPort(u *URL) (string, error) {
	host := u.Host
	if host == "" {
		return "", fmt.Errorf("empty host in URL")
	}
	portStr := u.Port
	if portStr == "" || portStr == "0" {
		return "", fmt.Errorf("invalid port in URL")
	}
	return net.JoinHostPort(host, portStr), nil
}

func (p *relayParent) connect(ctx context.Context, u *URL) (net.Conn, error) {
	// 1) 连接 relay 服务器
	c, err := dialContext(ctx, "tcp", p.addr)
	if err != nil {
		return nil, err
	}
	stopWatching := watchConnContext(ctx, c)
	defer stopWatching()
	// 开启 keepalive（客户端到 relay）
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}

	// 2) 发送目标
	dst, err := urlHostPort(u)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if _, err = io.WriteString(c, dst+"\n"); err != nil {
		_ = c.Close()
		return nil, err
	}

	// 3) 等 ACK
	_ = c.SetReadDeadline(time.Now().Add(p.ackTimeout))
	br := bufio.NewReader(c)
	ack, err := br.ReadString('\n')
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("relay ack read: %v", err)
	}
	switch strings.TrimSpace(ack) {
	case "OK":
		return c, nil
	case "ERR":
		_ = c.Close()
		return nil, fmt.Errorf("relay remote dial failed")
	default:
		_ = c.Close()
		return nil, fmt.Errorf("relay bad ack: %q", strings.TrimSpace(ack))
	}
}

/* =========================
   Server（listen = relay://ip:port?dial=8s）
   - 读 "host:port"
   - 拨号；成功回 "OK\n"，失败回 "ERR\n"
   - 双向转发（两端启用 keepalive）
   ========================= */

type relayProxy struct {
	rawAddr        string
	addr           string
	dialTimeout    time.Duration
	headerMaxBytes int
	dial           func(network, address string) (net.Conn, error)
}

func newRelayProxy(raw string) *relayProxy {
	addr, pm := splitAddrParams(raw)
	dial := 8 * time.Second // 让失败尽快暴露给上游 backup
	if v, ok := pm["dial"]; ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			dial = d
		}
	}
	return &relayProxy{
		rawAddr:        raw,
		addr:           addr,
		dialTimeout:    dial,
		headerMaxBytes: 256,
	}
}

func (p *relayProxy) Addr() string      { return p.addr }
func (p *relayProxy) genConfig() string { return "listen = relay://" + p.rawAddr }

func (p *relayProxy) Serve(ctx context.Context, wg *sync.WaitGroup) {
	var clients sync.WaitGroup
	defer func() {
		clients.Wait()
		wg.Done()
	}()

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		criticalf("[relay] listen %s failed: %v", p.addr, err)
		return
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	log.Printf("[relay] listening on %s (dial=%s)", p.addr, p.dialTimeout)

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			p.handleConn(ctx, c)
		}()
	}
}

func (p *relayProxy) handleConn(ctx context.Context, cli net.Conn) {
	defer cli.Close()
	cancelDone := make(chan struct{})
	defer close(cancelDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = cli.Close()
		case <-cancelDone:
		}
	}()

	// 开启 keepalive（客户端到 server）
	if tc, ok := cli.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}

	// 1) 读首行目标
	_ = cli.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(cli)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	if len(line) > p.headerMaxBytes {
		return
	}
	dst := strings.TrimSpace(line)
	if dst == "" || !strings.Contains(dst, ":") {
		return
	}
	_ = cli.SetReadDeadline(time.Time{})

	// 2) 拨目标并回 ACK
	dial := p.dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: p.dialTimeout}
		dial = func(network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}
	}
	dstConn, err := dial("tcp", dst)
	if err != nil {
		_, _ = io.WriteString(cli, "ERR\n")
		return
	}
	// 开启 keepalive（server 到 目标）
	if tc, ok := dstConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
	// 目标连通，告知上游
	if _, err = io.WriteString(cli, "OK\n"); err != nil {
		_ = dstConn.Close()
		return
	}

	// 3) 首行之后的缓冲内容先写给目标
	if br.Buffered() > 0 {
		if _, err = io.CopyN(dstConn, br, int64(br.Buffered())); err != nil {
			_ = dstConn.Close()
			return
		}
	}

	// 4) 双向转发
	defer dstConn.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dstConn, cli); done <- struct{}{} }()
	go func() { _, _ = io.Copy(cli, dstConn); done <- struct{}{} }()
	<-done
	_ = dstConn.Close()
	_ = cli.Close()
	<-done
}
