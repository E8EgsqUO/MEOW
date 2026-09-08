package main

import (
	"io"
	"strings"
	"testing"
)

func TestBufferedReaderPoolReset(t *testing.T) {
	p := newBufferedReaderPool(32)
	br := p.Get(strings.NewReader("old buffered data"))
	if _, err := br.Peek(3); err != nil {
		t.Fatal(err)
	}
	p.Put(br)

	br = p.Get(strings.NewReader("new"))
	defer p.Put(br)
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("pooled reader retained old data: %q", got)
	}
}

func TestByteBufferPoolRejectsWrongSize(t *testing.T) {
	p := newByteBufferPool(32)
	defer func() {
		if recover() == nil {
			t.Fatal("putting a wrong-sized buffer did not panic")
		}
	}()
	p.Put(make([]byte, 31))
}

func BenchmarkBufferedReaderPool(b *testing.B) {
	p := newBufferedReaderPool(httpBufSize)
	input := strings.Repeat("x", httpBufSize)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		br := p.Get(strings.NewReader(input))
		_, _ = br.Peek(1)
		p.Put(br)
	}
}
