package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/telemetry"
)

func TestProcessDiagnosticsJoinAndReportSamplingGaps(t *testing.T) {
	entered, release, resumed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	s := newProcessSampler(5*time.Millisecond, 100, func() telemetry.Process {
		calls++
		if calls == 2 {
			close(entered)
			<-release
		}
		if calls == 3 {
			close(resumed)
		}
		return telemetry.Process{GoGCCycles: uint64(calls)}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		s.finish()
		t.Fatal("collector did not start")
	}
	// Deliberately block the sampler, without blocking any request workers.
	time.Sleep(30 * time.Millisecond)
	close(release)
	select {
	case <-resumed:
	case <-time.After(5 * time.Second):
		s.finish()
		t.Fatal("collector did not resume after delayed read")
	}
	report := s.finish()
	if report.LimitReached || report.ProcessRecords != len(s.records) || len(s.records) < 4 || s.records[0].Stage != "initial" || s.records[len(s.records)-1].Stage != "final" {
		t.Fatal("incomplete sampler lifecycle", report)
	}
	var last time.Duration
	var skipped int64
	for _, r := range s.records {
		if r.Started < last || r.Finished < r.Started || r.StartedUTC.IsZero() || r.FinishedUTC.IsZero() {
			t.Fatal("unordered sample", r)
		}
		last = r.Finished
		skipped += r.SkippedIntervals
	}
	if skipped < 4 {
		t.Fatal("missed collection periods hidden", s.records)
	}
}

func TestProcessDiagnosticsBoundedAndOutputFailurePreserved(t *testing.T) {
	s := newProcessSampler(time.Millisecond, 1, func() telemetry.Process { return telemetry.Process{} })
	time.Sleep(50 * time.Millisecond)
	report := s.finish()
	if !report.LimitReached || len(s.records) != 3 || s.records[2].Stage != "final" {
		t.Fatal("sampling did not stop at the cap and preserve final", report)
	}
	a := artifacts{dir: t.TempDir(), hashes: map[string]string{}}
	if err := os.Mkdir(filepath.Join(a.dir, "generator-metrics.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	err := writeDiagnostics(&a, bench.RunResult{DispatchSamples: []bench.DispatchSample{{Index: 0, DispatchedAt: -1, WorkerAt: -1}}}, s)
	if !errors.Is(err, os.ErrExist) || len(a.hashes) != 1 || a.hashes["dispatch.jsonl"] == "" {
		t.Fatal("lost partial diagnostics on output failure", err, a.hashes)
	}
	if _, err := os.Stat(filepath.Join(a.dir, "dispatch.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticIntervalValidation(t *testing.T) {
	for _, interval := range []time.Duration{0, 100 * time.Millisecond, time.Second, time.Minute} {
		if err := validateDiagnosticInterval(interval); err != nil {
			t.Fatal(err)
		}
	}
	for _, interval := range []time.Duration{-1, time.Millisecond, time.Minute + 1} {
		if err := validateDiagnosticInterval(interval); err == nil {
			t.Fatal("accepted out-of-bounds interval", interval)
		}
	}
}
