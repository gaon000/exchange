package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/telemetry"
)

func verifyGeneratorDiagnostics(dir, runID string, workload settings, interval time.Duration, run bench.RunResult) error {
	path := func(name string) string { return filepath.Join(dir, "loadgen", name) }
	p, err := bench.Build(runID, workload.Workload)
	if err != nil {
		return err
	}
	if run.Options != workload.Execution {
		return fmt.Errorf("generator execution settings mismatch")
	}
	if err := jsonLines(path("samples.jsonl"), func(b []byte) error {
		var s bench.Sample
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if len(run.Samples) >= p.Count() {
			return fmt.Errorf("too many samples")
		}
		run.Samples = append(run.Samples, s)
		return nil
	}); err != nil {
		return err
	}
	if err := jsonLines(path("dispatch.jsonl"), func(b []byte) error {
		var s bench.DispatchSample
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if len(run.DispatchSamples) >= p.Count() {
			return fmt.Errorf("too many dispatch samples")
		}
		run.DispatchSamples = append(run.DispatchSamples, s)
		return nil
	}); err != nil {
		return err
	}
	if err := bench.ValidateDispatch(p, run); err != nil {
		return err
	}
	var report struct {
		Diagnostics struct {
			Interval       time.Duration `json:"intervalNanos"`
			OriginUTC      time.Time     `json:"originUTC"`
			ProcessRecords int           `json:"processRecords"`
			LimitReached   bool          `json:"limitReached"`
		} `json:"diagnostics"`
	}
	if err := readJSON(path("run.json"), &report); err != nil {
		return err
	}
	d := report.Diagnostics
	if d.Interval != interval || d.OriginUTC.IsZero() || d.ProcessRecords < 2 || d.ProcessRecords > 40002 || d.LimitReached {
		return fmt.Errorf("invalid generator process diagnostic report")
	}
	count, final := 0, false
	var lastEnd time.Duration
	next := interval
	err = jsonLines(path("generator-metrics.jsonl"), func(b []byte) error {
		var r struct {
			Stage            string             `json:"stage"`
			Scheduled        time.Duration      `json:"scheduledNanos"`
			Started          time.Duration      `json:"startedNanos"`
			Finished         time.Duration      `json:"finishedNanos"`
			StartedUTC       time.Time          `json:"startedUTC"`
			FinishedUTC      time.Time          `json:"finishedUTC"`
			SkippedIntervals int64              `json:"skippedIntervals"`
			Process          *telemetry.Process `json:"process"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		if count >= d.ProcessRecords || final || r.Started < lastEnd || r.Finished < r.Started || r.StartedUTC.IsZero() || r.FinishedUTC.IsZero() || r.Process == nil {
			return fmt.Errorf("invalid generator process sample sequence")
		}
		switch r.Stage {
		case "initial":
			if count != 0 || r.Scheduled != 0 || r.SkippedIntervals != 0 {
				return fmt.Errorf("invalid generator initial sample")
			}
		case "periodic":
			if count == 0 || r.Scheduled != next || r.Started < next || r.SkippedIntervals != int64((r.Started-next)/interval) {
				return fmt.Errorf("invalid generator sampling gap")
			}
			next += time.Duration(r.SkippedIntervals+1) * interval
		case "final":
			if count == 0 || r.Scheduled != -1 || r.SkippedIntervals != 0 || r.Started < run.Elapsed {
				return fmt.Errorf("invalid generator final sample")
			}
			final = true
		default:
			return fmt.Errorf("invalid generator sample stage")
		}
		lastEnd = r.Finished
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if !final || count != d.ProcessRecords {
		return fmt.Errorf("incomplete generator process diagnostics")
	}
	return nil
}
