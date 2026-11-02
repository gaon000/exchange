package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/telemetry"
)

// Bound memory even if a faulty Executor ignores its deadline. Normal plans are
// at most one hour plus one minute drain (36,600 intervals at the minimum rate).
const maxProcessSamples = 40000

type processSample struct {
	Stage            string            `json:"stage"`
	Scheduled        time.Duration     `json:"scheduledNanos"`
	Started          time.Duration     `json:"startedNanos"`
	Finished         time.Duration     `json:"finishedNanos"`
	StartedUTC       time.Time         `json:"startedUTC"`
	FinishedUTC      time.Time         `json:"finishedUTC"`
	SkippedIntervals int64             `json:"skippedIntervals"`
	Process          telemetry.Process `json:"process"`
}

type diagnosticReport struct {
	Interval       time.Duration `json:"intervalNanos"`
	OriginUTC      time.Time     `json:"originUTC"`
	ProcessRecords int           `json:"processRecords"`
	LimitReached   bool          `json:"limitReached"`
}

type processSampler struct {
	origin   time.Time
	interval time.Duration
	limit    int
	read     func() telemetry.Process
	records  []processSample // only collector goroutine writes after initial
	stop     chan struct{}
	done     chan struct{}
	limited  bool
}

func validateDiagnosticInterval(interval time.Duration) error {
	if interval != 0 && (interval < 100*time.Millisecond || interval > time.Minute) {
		return fmt.Errorf("diagnostics-interval must be 0 (off) or 100ms to 1m")
	}
	return nil
}

func startProcessSampler(interval time.Duration) *processSampler {
	return newProcessSampler(interval, maxProcessSamples, telemetry.ReadProcess)
}

func newProcessSampler(interval time.Duration, limit int, read func() telemetry.Process) *processSampler {
	s := &processSampler{origin: time.Now(), interval: interval, limit: limit, read: read,
		records: make([]processSample, 0, min(limit+2, 128)), stop: make(chan struct{}), done: make(chan struct{})}
	s.capture("initial", 0, 0)
	go s.collect()
	return s
}

func (s *processSampler) capture(stage string, scheduled time.Duration, skipped int64) {
	start := time.Now()
	if stage == "periodic" {
		skipped = max(int64(0), int64((start.Sub(s.origin)-scheduled)/s.interval))
	}
	p := s.read()
	end := time.Now()
	s.records = append(s.records, processSample{stage, scheduled, start.Sub(s.origin), end.Sub(s.origin),
		start.UTC(), end.UTC(), skipped, p})
}

func (s *processSampler) collect() {
	defer close(s.done)
	next := s.interval
	for {
		timer := time.NewTimer(max(0, next-time.Since(s.origin)))
		select {
		case <-s.stop:
			timer.Stop()
			s.capture("final", -1, 0)
			return
		case <-timer.C:
		}
		if len(s.records)-1 >= s.limit {
			s.limited = true
			<-s.stop
			s.capture("final", -1, 0)
			return
		}
		// Skip missed periods instead of fabricating catch-up samples. Collection
		// delay and wall/monotonic clock differences remain observable in each row.
		s.capture("periodic", next, 0)
		next += time.Duration(s.records[len(s.records)-1].SkippedIntervals+1) * s.interval
	}
}

func (s *processSampler) finish() diagnosticReport {
	close(s.stop)
	<-s.done
	return diagnosticReport{s.interval, s.origin.UTC(), len(s.records), s.limited}
}

func writeDiagnostics(a *artifacts, result bench.RunResult, sampler *processSampler) error {
	if err := a.write("dispatch.jsonl", func(w io.Writer) error {
		e := json.NewEncoder(w)
		for _, sample := range result.DispatchSamples {
			if err := e.Encode(sample); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return a.write("generator-metrics.jsonl", func(w io.Writer) error {
		e := json.NewEncoder(w)
		for _, record := range sampler.records {
			if err := e.Encode(record); err != nil {
				return err
			}
		}
		return nil
	})
}
