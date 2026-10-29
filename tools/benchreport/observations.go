package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/telemetry"
)

type metric struct {
	Version     int               `json:"version"`
	Stage       string            `json:"stage"`
	RunID       string            `json:"runId"`
	StartedUTC  time.Time         `json:"startedUTC"`
	FinishedUTC time.Time         `json:"finishedUTC"`
	Runtime     rt.Statistics     `json:"runtime"`
	Process     telemetry.Process `json:"process"`
	WALBytes    int64             `json:"walBytes,string"`
}

func readMetrics(path string, r *caseReport, epoch time.Time) error {
	var last metric
	count := 0
	err := lines(path, func(b []byte) error {
		var v metric
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if v.Version != 1 || v.RunID != r.RunID || v.StartedUTC.IsZero() || v.FinishedUTC.Before(v.StartedUTC) || (!last.StartedUTC.IsZero() && v.StartedUTC.Before(last.StartedUTC)) || last.Stage == "final" || count == 0 && v.Stage != "initial" || count > 0 && v.Stage == "initial" || v.Runtime.CommandQueueDepth < 0 {
			return fmt.Errorf("invalid metric record")
		}
		if v.Stage != "initial" && v.Stage != "periodic" && v.Stage != "final" {
			return fmt.Errorf("unknown metric stage")
		}
		count++
		last = v
		for i := range r.Windows {
			w := &r.Windows[i]
			at := v.StartedUTC.Sub(epoch)
			if at >= w.Start && at < w.End {
				w.MetricSamples++
				w.QueueMean += float64(v.Runtime.CommandQueueDepth)
				w.QueueMax = max(w.QueueMax, v.Runtime.CommandQueueDepth)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count < 2 || last.Stage != "final" {
		return fmt.Errorf("missing final metrics")
	}
	for i := range r.Windows {
		w := &r.Windows[i]
		if w.MetricSamples > 0 {
			w.QueueMean /= float64(w.MetricSamples)
		}
	}
	r.WALBytes, r.PeakRSSBytes = last.WALBytes, last.Process.PeakRSSBytes
	r.ServerCPUSeconds = float64(last.Process.UserCPUNanos+last.Process.SystemCPUNanos) / 1e9
	r.SnapshotCaptureNanos = last.Runtime.SnapshotCapture.SumNanos
	r.SnapshotPublishNanos = last.Runtime.SnapshotPublish.SumNanos
	if last.Runtime.Failures != 0 || last.Runtime.SnapshotErrors != 0 || !last.Process.OSAvailable || len(last.Process.RuntimeUnavailable) != 0 {
		r.Reasons = append(r.Reasons, "SERVER_FAILURE_OR_MISSING_RESOURCES")
	}
	return nil
}
func checkScenario(dir string, r *caseReport, m manifest, run bench.RunResult, c criteria) error {
	withSnapshot := r.Scenario == "snapshot" || r.Scenario == "snapshot-profile"
	withProfile := r.Scenario == "profile" || r.Scenario == "snapshot-profile"
	if !withSnapshot && !withProfile && r.Scenario != "normal" {
		return fmt.Errorf("unknown scenario")
	}
	s := m.Server.Experiments
	if withSnapshot {
		if s.SnapshotInterval != c.SnapshotInterval.String() || s.SnapshotLog == "" {
			r.Reasons = append(r.Reasons, "SNAPSHOT_SETTINGS_MISMATCH")
		}
		start := run.ScheduleEpoch.Add(m.Settings.Workload.Warmup)
		end := start.Add(m.Settings.Workload.Duration)
		err := lines(filepath.Join(dir, "snapshot-events.jsonl"), func(b []byte) error {
			var event struct {
				RunID       string    `json:"runId"`
				Outcome     string    `json:"outcome"`
				StartedUTC  time.Time `json:"startedUTC"`
				FinishedUTC time.Time `json:"finishedUTC"`
			}
			if err := json.Unmarshal(b, &event); err != nil {
				return err
			}
			if event.RunID != r.RunID || event.StartedUTC.IsZero() || event.FinishedUTC.Before(event.StartedUTC) {
				return fmt.Errorf("invalid snapshot event")
			}
			if event.Outcome == "COMPLETED" && event.StartedUTC.Before(end) && event.FinishedUTC.After(start) {
				r.SnapshotOverlaps++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if r.SnapshotOverlaps < c.MinimumSnapshotOverlaps {
			r.Reasons = append(r.Reasons, "INSUFFICIENT_SNAPSHOT_OVERLAP")
		}
	} else if s.SnapshotInterval != "0s" || s.SnapshotLog != "" {
		r.Reasons = append(r.Reasons, "UNEXPECTED_SNAPSHOT")
	}
	if withProfile {
		if s.ProfileDir == "" {
			r.Reasons = append(r.Reasons, "MISSING_PROFILE_SETTING")
		}
		var p struct {
			StartedUTC     time.Time `json:"startedUTC"`
			CPUStoppedUTC  time.Time `json:"cpuStoppedUTC"`
			FinishedUTC    time.Time `json:"finishedUTC"`
			MemProfileRate int       `json:"memProfileRate"`
			PostDrainGC    bool      `json:"postDrainGC"`
		}
		if err := readJSON(filepath.Join(dir, "profiles/profile.json"), &p); err != nil {
			return err
		}
		if p.StartedUTC.IsZero() || p.StartedUTC.After(run.ScheduleEpoch) || p.CPUStoppedUTC.Before(run.FinishedUTC) || p.FinishedUTC.Before(p.CPUStoppedUTC) || p.MemProfileRate != s.MemProfileRate || !p.PostDrainGC {
			return fmt.Errorf("profile scope mismatch")
		}
		for _, name := range []string{"cpu.pprof", "heap.pprof", "allocs.pprof"} {
			f, err := os.Open(filepath.Join(dir, "profiles", name))
			if err != nil {
				return err
			}
			g, err := gzip.NewReader(f)
			if err != nil {
				_ = f.Close()
				return err
			}
			n, err := io.Copy(io.Discard, io.LimitReader(g, (128<<20)+1))
			err = errors.Join(err, g.Close(), f.Close())
			if err != nil {
				return err
			}
			if n == 0 || n > 128<<20 {
				return fmt.Errorf("empty/oversized profile")
			}
		}
	} else if s.ProfileDir != "" {
		r.Reasons = append(r.Reasons, "UNEXPECTED_PROFILE")
	}
	return nil
}

func groupCases(cases []caseReport, minimum int) []groupReport {
	// Duplicate identities invalidate both copies, even if they were presented
	// using different directories or in different apparent comparison groups.
	seen := map[string]int{}
	for i := range cases {
		r := &cases[i]
		if r.RunID == "" {
			continue
		}
		if j, ok := seen[r.RunID]; ok {
			r.Pass = false
			r.Reasons = append(r.Reasons, "DUPLICATE_RUN")
			cases[j].Pass = false
			cases[j].Reasons = append(cases[j].Reasons, "DUPLICATE_RUN")
		} else {
			seen[r.RunID] = i
		}
	}
	groups := map[string]*groupReport{}
	for _, r := range cases {
		if r.Series == "" {
			continue
		}
		key := fmt.Sprintf("%s:%d", r.Series, r.Rate)
		g := groups[key]
		if g == nil {
			g = &groupReport{Series: r.Series, Scenario: r.Scenario, Rate: r.Rate, MinNewOrdersPerSecond: r.Summary.Measurement.NewOrdersPerSecond}
			groups[key] = g
		}
		g.Runs++
		if r.Pass {
			g.Passed++
		}
		g.MinNewOrdersPerSecond = min(g.MinNewOrdersPerSecond, r.Summary.Measurement.NewOrdersPerSecond)
		g.MaxNewOrdersPerSecond = max(g.MaxNewOrdersPerSecond, r.Summary.Measurement.NewOrdersPerSecond)
		g.WorstRequestP99 = max(g.WorstRequestP99, r.Summary.Measurement.RequestLatency.P99)
		g.WorstScheduledP99 = max(g.WorstScheduledP99, r.Summary.Measurement.UniqueNewOrderScheduledLatency.P99)
	}
	out := make([]groupReport, 0, len(groups))
	for _, g := range groups {
		g.Qualified = g.Runs >= minimum && g.Passed == g.Runs
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Series != out[j].Series {
			return out[i].Series < out[j].Series
		}
		return out[i].Rate < out[j].Rate
	})
	return out
}
