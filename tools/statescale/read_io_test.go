package main

import (
	"bytes"
	"io"
	"testing"
)

func TestReadIOPhaseHistograms(t *testing.T) {
	const payload = 700001
	data := make([]byte, payload+52)
	for _, buffer := range []int{32768, 65536, 131072, 262144} {
		r := &observedReader{source: bytes.NewReader(data), footer: payload + 20, phases: map[string]*readCounts{}}
		read := func(off, n int) {
			t.Helper()
			if _, e := r.ReadAt(make([]byte, n), int64(off)); e != nil {
				t.Fatal(e)
			}
		}
		read(0, 20)
		for off := 0; off < payload+20; {
			n := min(32768, payload+20-off)
			read(off, n)
			off += n
		}
		read(payload+20, 32)
		for off := 0; off < payload; {
			n := min(buffer, payload-off)
			read(20+off, n)
			off += n
		}
		if e := verifyReadCounts(r.phases, payload, buffer); e != nil {
			t.Fatal(buffer, e)
		}
		r.phases["decode"].Sizes[1]++
		if e := verifyReadCounts(r.phases, payload, buffer); e == nil {
			t.Fatal("wrong histogram accepted")
		}
	}
	r := &observedReader{source: bytes.NewReader(nil), phases: map[string]*readCounts{}}
	if n, e := r.ReadAt(make([]byte, 20), 0); n != 0 || e != io.EOF {
		t.Fatal("reader error changed", n, e)
	}
}
