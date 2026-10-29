package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	rt "github.com/gaon000/exchange/internal/runtime"
)

type snapshotCall func(context.Context) (rt.SnapshotResult, error)

func (f snapshotCall) RunID() string                                           { return "test-run" }
func (f snapshotCall) Snapshot(ctx context.Context) (rt.SnapshotResult, error) { return f(ctx) }

func TestSnapshotScheduleSkipsBacklogAndJoinsCanceledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	calls := 0
	source := snapshotCall(func(ctx context.Context) (rt.SnapshotResult, error) {
		calls++
		entered <- struct{}{}
		if calls == 1 {
			<-release
			return rt.SnapshotResult{SnapshotSeq: 7}, nil
		}
		<-ctx.Done()
		return rt.SnapshotResult{}, ctx.Err()
	})
	w := &metricsWriter{notices: make(chan struct{}, 4)}
	s := startSnapshots(ctx, source, 5*time.Millisecond, w, cancel)
	<-entered
	// Hold one publication across multiple scheduled times. No other request
	// can start, and the next event records skipped times rather than a burst.
	select {
	case <-entered:
		t.Fatal("concurrent snapshot")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-w.notices
	<-entered
	cancel()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(w.Bytes()))
	var first, second snapshotEvent
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first.Outcome != "COMPLETED" || first.SnapshotSeq != 7 || first.SkippedIntervals < 1 || first.RunID != "test-run" || first.FinishedUTC.Before(first.StartedUTC) || !second.ScheduledUTC.After(first.FinishedUTC) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if second.Outcome != "UNKNOWN" || second.Error == "" || calls != 2 || w.closes != 1 || w.syncs != 1 {
		t.Fatal(second, calls, w.closes, w.syncs)
	}
}

func TestSnapshotScheduleFailureRequestsDrain(t *testing.T) {
	for _, outputFailure := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		w := &metricsWriter{}
		if outputFailure {
			w.failOn = 1
		}
		s := startSnapshots(ctx, snapshotCall(func(context.Context) (rt.SnapshotResult, error) {
			if outputFailure {
				return rt.SnapshotResult{}, nil
			}
			return rt.SnapshotResult{}, metricsFailure
		}), time.Millisecond, w, cancel)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			cancel()
			t.Fatal("failure did not cancel server")
		}
		if err := s.Wait(); !errors.Is(err, metricsFailure) || w.closes != 1 {
			t.Fatal(err, w.closes)
		}
		cancel()
	}
}

func TestProfileOutputCollisionAndAsynchronousFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := openProfiles(dir, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Finish()
	if _, err := openProfiles(dir, cancel); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if err := p.cpu.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.cpu.Write([]byte("profile")); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("profile failure did not request drain")
	}
	if err := p.Finish(); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if err := p.Finish(); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}
