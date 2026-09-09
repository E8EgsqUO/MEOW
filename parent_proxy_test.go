package main

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
)

type latencyTestParent struct {
	server string
	err    error
	calls  atomic.Int32
}

func (p *latencyTestParent) connect(context.Context, *URL) (net.Conn, error) {
	p.calls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	return &partialWriteConn{maxWrite: 4096}, nil
}

func (p *latencyTestParent) getServer() string { return p.server }
func (p *latencyTestParent) genConfig() string { return "" }

func TestLatencyPoolRemembersFailedParent(t *testing.T) {
	failing := &latencyTestParent{server: "failed.example:1", err: errors.New("unreachable")}
	healthy := &latencyTestParent{server: "healthy.example:2"}
	pool := newLatencyParentPool([]ParentWithFail{{ParentProxy: failing}, {ParentProxy: healthy}})
	url := mustURL(t, "target.example:80")

	for i := 0; i < 2; i++ {
		conn, err := pool.connect(context.Background(), url)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
	if got := failing.calls.Load(); got != 1 {
		t.Fatalf("failed parent tried %d times, want once before being deprioritized", got)
	}
	if got := healthy.calls.Load(); got != 2 {
		t.Fatalf("healthy parent tried %d times, want 2", got)
	}
}
