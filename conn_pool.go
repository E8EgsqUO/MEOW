// Share server connections between different clients.

package main

import (
	"context"
	"sync"
	"time"
)

// Maximum number of connections to a server.
const maxServerConnCnt = 5

// Store each server's connections in separate channels, getting
// connections for different servers can be done in parallel.
type ConnPool struct {
	idleConn map[string]chan *serverConn
	muxConn  chan *serverConn // connections support multiplexing
	sync.RWMutex
}

var connPool = &ConnPool{
	idleConn: map[string]chan *serverConn{},
	muxConn:  make(chan *serverConn, maxServerConnCnt*2),
}

const muxConnHostPort = "@muxConn"

func (cp *ConnPool) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cp.closeStale()
			}
		}
	}()
}

func getConnFromChan(ch chan *serverConn) (sv *serverConn) {
	for {
		select {
		case sv = <-ch:
			if sv.mayBeClosed() {
				sv.Close()
				continue
			}
			return sv
		default:
			return nil
		}
	}
}

func putConnToChan(sv *serverConn, ch chan *serverConn, chname string) {
	select {
	case ch <- sv:
		debug.Printf("connPool channel %s: put conn\n", chname)
		return
	default:
		// Simply close the connection if can't put into channel immediately.
		// A better solution would remove old connections from the channel and
		// add the new one. But's it's more complicated and this should happen
		// rarely.
		debug.Printf("connPool channel %s: full", chname)
		sv.Close()
	}
}

func (cp *ConnPool) Get(hostPort string, direct bool) (sv *serverConn) {
	// Get from site specific connection first.
	// Direct connection are all site specific, so must use site specific
	// first to avoid using parent proxy for direct sites.
	cp.RLock()
	ch := cp.idleConn[hostPort]
	if ch != nil {
		sv = getConnFromChan(ch)
	}
	cp.RUnlock()
	if sv != nil {
		debug.Printf("connPool %s: get conn\n", hostPort)
		return sv
	}

	// All mulplexing connections are for blocked sites,
	// so for direct sites we should stop here.
	if direct {
		return nil
	}

	sv = getConnFromChan(cp.muxConn)
	if bool(debug) && sv != nil {
		debug.Println("connPool mux: get conn", hostPort)
	}
	return sv
}

func (cp *ConnPool) Put(sv *serverConn) {
	if sv.mayBeClosed() {
		sv.Close()
		return
	}
	// Multiplexing connections.
	switch sv.Conn.(type) {
	case httpConn, meowConn:
		putConnToChan(sv, cp.muxConn, "muxConn")
		return
	}

	// Site specific connections.
	cp.Lock()
	ch := cp.idleConn[sv.hostPort]
	if ch == nil {
		debug.Printf("connPool %s: new channel\n", sv.hostPort)
		ch = make(chan *serverConn, maxServerConnCnt)
		cp.idleConn[sv.hostPort] = ch
	}
	putConnToChan(sv, ch, sv.hostPort)
	cp.Unlock()
}

type chanInPool struct {
	hostPort string
	ch       chan *serverConn
}

func (cp *ConnPool) CloseAll() {
	debug.Println("connPool: close all server connections")

	// Because closeServerConn may acquire connPool.Lock, we first collect all
	// channel, and close server connection for each one.
	var connCh []chanInPool
	cp.RLock()
	for hostPort, ch := range cp.idleConn {
		connCh = append(connCh, chanInPool{hostPort, ch})
	}
	cp.RUnlock()

	for _, hc := range connCh {
		cp.closeServerConn(hc.ch, hc.hostPort, true)
	}

	cp.closeServerConn(cp.muxConn, muxConnHostPort, true)
}

func (cp *ConnPool) closeServerConn(ch chan *serverConn, hostPort string, force bool) (done bool) {
	// If force is true, close all idle connection even if it maybe open.
	lcnt := len(ch)
	if lcnt == 0 {
		// Execute the loop at least once.
		lcnt = 1
	}
	for i := 0; i < lcnt; i++ {
		select {
		case sv := <-ch:
			if force || sv.mayBeClosed() {
				debug.Printf("connPool channel %s: close one conn\n", hostPort)
				sv.Close()
			} else {
				// Another request may have filled the slot while we checked
				// this connection. Never block the pool's cleanup worker.
				putConnToChan(sv, ch, hostPort)
			}
		default:
			if hostPort != muxConnHostPort {
				// No more connection in this channel, remove the channel from
				// the map.
				debug.Printf("connPool channel %s: remove\n", hostPort)
				cp.Lock()
				if cp.idleConn[hostPort] == ch && len(ch) == 0 {
					delete(cp.idleConn, hostPort)
				}
				cp.Unlock()
			}
			return true
		}
	}
	return false
}

func (cp *ConnPool) closeStale() {
	cp.RLock()
	channels := make([]chanInPool, 0, len(cp.idleConn))
	for hostPort, ch := range cp.idleConn {
		channels = append(channels, chanInPool{hostPort, ch})
	}
	cp.RUnlock()
	for _, hc := range channels {
		cp.closeServerConn(hc.ch, hc.hostPort, false)
	}
	cp.closeServerConn(cp.muxConn, muxConnHostPort, false)
}
