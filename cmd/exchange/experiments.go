package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

// profileWriter retains asynchronous CPU-profile write errors; pprof itself
// does not return those errors from StopCPUProfile. It requests normal drain.
type profileWriter struct {
	file   *os.File
	mu     sync.Mutex
	err    error
	cancel func()
}

func (w *profileWriter) Write(b []byte) (int, error) {
	n, err := w.file.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.mu.Lock()
		w.err = errors.Join(w.err, err)
		w.mu.Unlock()
		w.cancel()
	}
	return n, err
}

type profiles struct {
	cpu                    *profileWriter
	heap, allocs, metadata *os.File
	startedUTC             time.Time
	active                 bool
	once                   sync.Once
	err                    error
}

// openProfiles reserves fresh output files before run recovery. No profiler is
// activated until Start. Failed or incomplete files are preserved for inspection.
func openProfiles(dir string, cancel func()) (*profiles, error) {
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	p := &profiles{}
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	f, err := open("cpu.pprof")
	if err != nil {
		return nil, err
	}
	p.cpu = &profileWriter{file: f, cancel: cancel}
	p.heap, err = open("heap.pprof")
	if err != nil {
		return nil, errors.Join(err, p.Finish())
	}
	p.allocs, err = open("allocs.pprof")
	if err != nil {
		return nil, errors.Join(err, p.Finish())
	}
	p.metadata, err = open("profile.json")
	if err != nil {
		return nil, errors.Join(err, p.Finish())
	}
	return p, nil
}
func (p *profiles) Start() error {
	p.startedUTC = time.Now().UTC()
	if err := pprof.StartCPUProfile(p.cpu); err != nil {
		return err
	}
	p.active = true
	return nil
}
func (p *profiles) Finish() error {
	p.once.Do(func() {
		if p.active {
			pprof.StopCPUProfile() // joins asynchronous writer before reading its error
			stoppedUTC := time.Now().UTC()
			p.active = false
			// A post-drain GC produces a final live-heap profile. It is outside
			// measured requests and outside the CPU profile, and is documented.
			runtime.GC()
			for _, item := range []struct {
				name string
				file *os.File
			}{{"heap", p.heap}, {"allocs", p.allocs}} {
				if item.file != nil {
					p.err = errors.Join(p.err, pprof.Lookup(item.name).WriteTo(item.file, 0))
				}
			}
			p.err = errors.Join(p.err, json.NewEncoder(p.metadata).Encode(struct {
				StartedUTC     time.Time `json:"startedUTC"`
				CPUStoppedUTC  time.Time `json:"cpuStoppedUTC"`
				FinishedUTC    time.Time `json:"finishedUTC"`
				MemProfileRate int       `json:"memProfileRate"`
				PostDrainGC    bool      `json:"postDrainGC"`
			}{p.startedUTC, stoppedUTC, time.Now().UTC(), runtime.MemProfileRate, true}))
		}
		if p.cpu != nil {
			p.cpu.mu.Lock()
			p.err = errors.Join(p.err, p.cpu.err)
			p.cpu.mu.Unlock()
		}
		var cpuFile *os.File
		if p.cpu != nil {
			cpuFile = p.cpu.file
		}
		for _, f := range []*os.File{cpuFile, p.heap, p.allocs, p.metadata} {
			if f != nil {
				p.err = errors.Join(p.err, f.Sync(), f.Close())
			}
		}
	})
	return p.err
}

type snapshotEvent struct {
	ScheduledUTC     time.Time `json:"scheduledUTC"`
	StartedUTC       time.Time `json:"startedUTC"`
	FinishedUTC      time.Time `json:"finishedUTC"`
	RunID            string    `json:"runId"`
	SnapshotSeq      d.Int     `json:"snapshotSeq"`
	Outcome          string    `json:"outcome"`
	Error            string    `json:"error,omitempty"`
	SkippedIntervals uint64    `json:"skippedIntervals,string"`
}
type snapshotSchedule struct {
	done chan struct{}
	err  error
}
type snapshotter interface {
	RunID() string
	Snapshot(context.Context) (rt.SnapshotResult, error)
}

// startSnapshots has no request backlog and waits for each publication. Its
// schedule is anchored to server startup, independently of loadgen's phases.
func startSnapshots(ctx context.Context, source snapshotter, interval time.Duration, w io.WriteCloser, cancel func()) *snapshotSchedule {
	s := &snapshotSchedule{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer func() {
			if f, ok := w.(interface{ Sync() error }); ok {
				s.err = errors.Join(s.err, f.Sync())
			}
			s.err = errors.Join(s.err, w.Close())
		}()
		next := time.Now().Add(interval)
		for count := 0; ; count++ {
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if ctx.Err() != nil {
				return
			}
			if count >= 1000 {
				s.err = fmt.Errorf("snapshot experiment exceeds 1000 publications")
				cancel()
				return
			}
			event := snapshotEvent{ScheduledUTC: next.UTC(), StartedUTC: time.Now().UTC(), RunID: source.RunID()}
			result, err := source.Snapshot(ctx)
			event.FinishedUTC = time.Now().UTC()
			event.Outcome = "COMPLETED"
			event.SnapshotSeq = result.SnapshotSeq
			if err != nil {
				event.Outcome = "UNKNOWN"
				event.Error = err.Error()
				var failure *rt.Failure
				if errors.As(err, &failure) {
					event.Outcome = string(failure.Outcome)
				}
			}
			next = next.Add(interval)
			if now := time.Now(); !now.Before(next) {
				skipped := uint64(now.Sub(next)/interval) + 1
				event.SkippedIntervals = skipped
				next = next.Add(time.Duration(skipped) * interval)
			}
			if writeErr := json.NewEncoder(w).Encode(event); writeErr != nil {
				s.err = writeErr
				cancel()
				return
			}
			if err != nil {
				if ctx.Err() == nil {
					s.err = err
					cancel()
				}
				return
			}
		}
	}()
	return s
}
func (s *snapshotSchedule) Wait() error { <-s.done; return s.err }
