package main

import (
	"bufio"
	"io"
	"sync"
)

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

type bufferedReaderPool struct {
	size int
	pool sync.Pool
}

func newBufferedReaderPool(size int) *bufferedReaderPool {
	p := &bufferedReaderPool{size: size}
	p.pool.New = func() any { return bufio.NewReaderSize(eofReader{}, size) }
	return p
}

func (p *bufferedReaderPool) Get(r io.Reader) *bufio.Reader {
	br := p.pool.Get().(*bufio.Reader)
	br.Reset(r)
	return br
}

func (p *bufferedReaderPool) Put(br *bufio.Reader) {
	if br == nil {
		return
	}
	if br.Size() != p.size {
		panic("invalid buffered reader size returned to pool")
	}
	br.Reset(eofReader{})
	p.pool.Put(br)
}

type byteBufferPool struct {
	size int
	pool sync.Pool
}

func newByteBufferPool(size int) *byteBufferPool {
	p := &byteBufferPool{size: size}
	p.pool.New = func() any { return make([]byte, size) }
	return p
}

func (p *byteBufferPool) Get() []byte { return p.pool.Get().([]byte) }

func (p *byteBufferPool) Put(buf []byte) {
	if len(buf) != p.size {
		panic("invalid buffer size returned to pool")
	}
	p.pool.Put(buf)
}
