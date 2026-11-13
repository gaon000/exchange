//go:build darwin || linux

package main

import (
	"errors"
	"io"
	"syscall"
	"testing"
)

func TestPreadCounterRetriesAndErrors(t *testing.T) {
	calls := 0
	r := &preadReader{call: func(p []byte, off int64) (int, error) {
		calls++
		if calls == 1 {
			return -1, syscall.EINTR
		}
		if off != int64((calls-2)*2) {
			t.Fatal("offset", off)
		}
		copy(p, "ab")
		return 2, nil
	}}
	if n, e := r.ReadAt(make([]byte, 4), 0); n != 4 || e != nil {
		t.Fatal(n, e)
	}
	if r.Counts.Calls != 3 || r.Counts.RequestedBytes != 10 || r.Counts.ReadBytes != 4 || r.Interrupted != 1 || r.ShortReads != 1 {
		t.Fatal(r)
	}
	boom := errors.New("pread failure")
	r = &preadReader{call: func([]byte, int64) (int, error) { return -1, boom }}
	if n, e := r.ReadAt(make([]byte, 4), 0); n != 0 || !errors.Is(e, boom) {
		t.Fatal(n, e)
	}
	r = &preadReader{call: func([]byte, int64) (int, error) { return 0, nil }}
	if n, e := r.ReadAt(make([]byte, 4), 0); n != 0 || e != io.EOF || r.Counts.Calls != 1 {
		t.Fatal(n, e)
	}
}
