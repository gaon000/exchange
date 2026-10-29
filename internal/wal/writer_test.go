package wal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"syscall"
	"testing"
)

type memorySink struct {
	bytes.Buffer
	chunk     int
	failAfter int
	writeErr  error
	syncErr   error
	syncs     int
	interrupt bool
}

func (s *memorySink) Write(b []byte) (int, error) {
	if s.interrupt {
		s.interrupt = false
		return 0, syscall.EINTR
	}
	n := len(b)
	if s.chunk > 0 && n > s.chunk {
		n = s.chunk
	}
	if s.writeErr != nil && s.Len()+n >= s.failAfter {
		n = s.failAfter - s.Len()
		if n < 0 {
			n = 0
		}
		_, _ = s.Buffer.Write(b[:n])
		return n, s.writeErr
	}
	return s.Buffer.Write(b[:n])
}
func (s *memorySink) Sync() error { s.syncs++; return s.syncErr }

func TestWriterShortWritesAndEveryFailureBoundary(t *testing.T) {
	g := testGenesis(t)
	r := place(1, "alice", "a")
	want := group(t, r)
	for _, chunk := range []int{1, 3, len(want)} {
		s := &memorySink{chunk: chunk, interrupt: true}
		w := writer{sink: s, validator: newValidator(g)}
		if err := w.append([]Record{r}); err != nil || w.last != 1 || w.failed || !bytes.Equal(s.Bytes(), want) || s.syncs != 1 {
			t.Fatalf("chunk %d: %+v %v", chunk, w, err)
		}
	}
	failure := errors.New("injected disk error")
	for cut := 0; cut <= len(want); cut++ {
		s := &memorySink{chunk: 3, failAfter: cut, writeErr: failure}
		w := writer{sink: s, validator: newValidator(g)}
		if err := w.append([]Record{r}); !errors.Is(err, failure) || w.last != 0 || !w.failed || s.syncs != 0 {
			t.Fatalf("cut %d: %+v %v", cut, w, err)
		}
		before := bytes.Clone(s.Bytes())
		if err := w.append([]Record{r}); !errors.Is(err, ErrPoisoned) || !bytes.Equal(before, s.Bytes()) {
			t.Fatalf("cut %d reused failed writer", cut)
		}
		info, records, err := scanBytes(append(header(g), s.Bytes()...), g)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		if cut < len(want) && (info.LastSeq != 0 || len(records) != 0 || info.TornTail != (cut > 0)) {
			t.Fatalf("cut %d replay: %+v", cut, info)
		}
		if cut == len(want) && (info.LastSeq != 1 || len(records) != 1) {
			t.Fatal("complete unacknowledged group lost")
		}
	}
}

func TestSyncFailureDoesNotAdvanceDurableSequence(t *testing.T) {
	g := testGenesis(t)
	s := &memorySink{}
	w := writer{sink: s, validator: newValidator(g)}
	if err := w.append([]Record{place(1, "alice", "a")}); err != nil {
		t.Fatal(err)
	}
	s.syncErr = errors.New("sync failure")
	if err := w.append([]Record{place(2, "bob", "b")}); !errors.Is(err, s.syncErr) || w.last != 1 || !w.failed || s.syncs != 2 {
		t.Fatalf("%+v %v", w, err)
	}
	if w.validator.seen[place(2, "bob", "b").Command.Key()] {
		t.Fatal("failed sync marked committed")
	}
	info, records, err := scanBytes(append(header(g), s.Bytes()...), g)
	if err != nil || info.LastSeq != 2 || len(records) != 2 {
		t.Fatalf("complete bytes must recover: %+v %v", info, err)
	}
}

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(b []byte) (int, error) { return f(b) }
func TestWriteAllProgressAndErrors(t *testing.T) {
	for name, f := range map[string]writeFunc{
		"zero":     func([]byte) (int, error) { return 0, nil },
		"negative": func([]byte) (int, error) { return -1, nil },
		"too many": func(b []byte) (int, error) { return len(b) + 1, nil },
	} {
		t.Run(name, func(t *testing.T) {
			if err := writeAll(f, []byte("abc")); err == nil {
				t.Fatal("invalid writer accepted")
			}
		})
	}
	var got bytes.Buffer
	err := writeAll(writeFunc(func(b []byte) (int, error) { _, _ = got.Write(b[:1]); return 1, syscall.EINTR }), []byte("abc"))
	if err != nil || got.String() != "abc" {
		t.Fatalf("progress+EINTR: %q %v", got.String(), err)
	}
	if err := writeAll(writeFunc(func([]byte) (int, error) { return 0, nil }), nil); err != nil {
		t.Fatal(err)
	}
	if err := writeAll(writeFunc(func([]byte) (int, error) { return 0, io.ErrClosedPipe }), []byte("a")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestWriterPrevalidationNeverWritesOrPoisons(t *testing.T) {
	g := testGenesis(t)
	badAccount := place(1, "unknown", "a")
	badRun := place(1, "alice", "a")
	badRun.Command.RunID = "00000000-0000-4000-8000-000000000002"
	badPrice := place(1, "alice", "a")
	badPrice.Command.Place.PriceTicks = math.MaxInt64
	tooMany := make([]Record, 257)
	for i := range tooMany {
		tooMany[i] = place(1, "alice", fmt.Sprint(i))
	}
	for _, rs := range [][]Record{nil, {place(0, "alice", "a")}, {place(2, "alice", "a")}, {badAccount}, {badRun}, {badPrice}, {place(1, "alice", "a"), place(2, "alice", "a")}, tooMany} {
		s := &memorySink{}
		w := writer{sink: s, validator: newValidator(g)}
		if err := w.append(rs); err == nil || w.failed || w.last != 0 || s.Len() != 0 || s.syncs != 0 {
			t.Fatalf("invalid group mutated: %+v %v", w, err)
		}
		if err := w.append([]Record{place(1, "alice", "good")}); err != nil || w.last != 1 {
			t.Fatal(err)
		}
	}
	s := &memorySink{}
	w := writer{sink: s, validator: newValidator(g)}
	if err := w.append([]Record{place(1, "alice", "a")}); err != nil {
		t.Fatal(err)
	}
	before := s.Len()
	if err := w.append([]Record{place(2, "alice", "a")}); err == nil || s.Len() != before || w.last != 1 {
		t.Fatal("duplicate key appended")
	}
	g.Config.MaxCommands = 1
	w.validator = newValidator(g)
	if err := w.append([]Record{place(2, "alice", "b")}); err == nil || s.Len() != before || w.last != 1 {
		t.Fatal("run limit appended")
	}
}
