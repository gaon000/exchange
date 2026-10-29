package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

var metricsFailure = errors.New("metrics output failed")

type metricsWriter struct {
	mu sync.Mutex
	bytes.Buffer
	writes, failOn, syncs, closes int
	notices                       chan struct{}
}

func (w *metricsWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.notices != nil {
		select {
		case w.notices <- struct{}{}:
		default:
		}
	}
	if w.writes == w.failOn {
		return 0, metricsFailure
	}
	return w.Buffer.Write(b)
}
func (w *metricsWriter) Sync() error  { w.mu.Lock(); defer w.mu.Unlock(); w.syncs++; return nil }
func (w *metricsWriter) Close() error { w.mu.Lock(); defer w.mu.Unlock(); w.closes++; return nil }

func TestMetricsSamplerWritesInitialPeriodicFinalAndJoins(t *testing.T) {
	w := &metricsWriter{notices: make(chan struct{}, 8)}
	collect := func(stage string) (metricsRecord, error) { return metricsRecord{Version: 1, Stage: stage}, nil }
	s, err := startMetrics(w, time.Millisecond, collect, func() { t.Error("unexpected cancellation") })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-w.notices:
		case <-time.After(time.Second):
			t.Fatal("no sample")
		}
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	var stages []string
	decoder := json.NewDecoder(bytes.NewReader(w.Bytes()))
	for {
		var r metricsRecord
		err := decoder.Decode(&r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		stages = append(stages, r.Stage)
	}
	if len(stages) < 3 || stages[0] != "initial" || stages[len(stages)-1] != "final" || w.syncs != 1 || w.closes != 1 {
		t.Fatalf("%v sync=%d close=%d", stages, w.syncs, w.closes)
	}
}

func TestMetricsSamplerFailuresCloseAndRequestDrain(t *testing.T) {
	for _, failOn := range []int{1, 2} {
		t.Run(string(rune('0'+failOn)), func(t *testing.T) {
			w := &metricsWriter{failOn: failOn}
			canceled := make(chan struct{}, 1)
			s, err := startMetrics(w, time.Millisecond, func(stage string) (metricsRecord, error) { return metricsRecord{Stage: stage}, nil }, func() { canceled <- struct{}{} })
			if failOn == 1 {
				if !errors.Is(err, metricsFailure) || s != nil || w.closes != 1 {
					t.Fatalf("initial failure: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("failure did not request shutdown")
			}
			if err := s.Finish(); !errors.Is(err, metricsFailure) || w.closes != 1 {
				t.Fatalf("periodic failure: %v", err)
			}
		})
	}
	w := &metricsWriter{}
	s, err := startMetrics(w, time.Second, func(string) (metricsRecord, error) { return metricsRecord{}, metricsFailure }, func() {})
	if s != nil || !errors.Is(err, metricsFailure) || w.closes != 1 {
		t.Fatalf("collection failure: %v", err)
	}
}
