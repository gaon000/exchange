package main

import (
	"bufio"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gaon000/exchange/internal/bench"
)

type verification struct {
	RunID                           string    `json:"runId"`
	PlanDigest                      string    `json:"planDigest"`
	MeasurementStartUTC             time.Time `json:"measurementStartUTC"`
	MeasurementEndUTC               time.Time `json:"measurementEndUTC"`
	MetricRecords                   int       `json:"metricRecords"`
	SnapshotsOverlappingMeasurement int       `json:"snapshotsOverlappingMeasurement"`
	ProfileRecorded                 bool      `json:"profileRecorded"`
}

func jsonLines(path string, visit func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1<<20)
	for s.Scan() {
		if err := visit(s.Bytes()); err != nil {
			return err
		}
	}
	return s.Err()
}
func verifyEvidence(dir string, ready ready, workload settings, o options) (verification, error) {
	v := verification{RunID: ready.RunID}
	path := func(name string) string { return filepath.Join(dir, name) }
	var complete struct {
		Version    int               `json:"version"`
		PlanDigest string            `json:"planDigest"`
		Files      map[string]string `json:"sha256"`
	}
	if err := readJSON(path("loadgen/completed.json"), &complete); err != nil {
		return v, err
	}
	names := []string{"manifest.json", "plan.jsonl", "preparation.json", "samples.jsonl", "run.json", "summary.json"}
	digest, err := hex.DecodeString(complete.PlanDigest)
	if err != nil || len(digest) != 32 || complete.Version != bench.Version || len(complete.Files) != len(names) {
		return v, fmt.Errorf("invalid loadgen completion marker")
	}
	for _, name := range names {
		expected, ok := complete.Files[name]
		if !ok {
			return v, fmt.Errorf("missing loadgen artifact %s", name)
		}
		actual, err := hashFile(path("loadgen/" + name))
		if err != nil {
			return v, err
		}
		if actual != expected {
			return v, fmt.Errorf("hash mismatch for %s", name)
		}
	}
	var manifest struct {
		PlanDigest string `json:"planDigest"`
		Server     struct {
			RunID      string `json:"runId"`
			ConfigHash string `json:"configHash"`
		} `json:"server"`
	}
	if err := readJSON(path("loadgen/manifest.json"), &manifest); err != nil {
		return v, err
	}
	if manifest.PlanDigest != complete.PlanDigest || manifest.Server.RunID != ready.RunID || manifest.Server.ConfigHash != ready.ConfigHash {
		return v, fmt.Errorf("loadgen identity mismatch")
	}
	var summary bench.Summary
	if err := readJSON(path("loadgen/summary.json"), &summary); err != nil {
		return v, err
	}
	if !summary.MeasurementValid || summary.PlanDigest != complete.PlanDigest || summary.Measurement.Duration != workload.Workload.Duration {
		return v, fmt.Errorf("invalid measurement summary")
	}
	var run bench.RunResult
	if err := readJSON(path("loadgen/run.json"), &run); err != nil {
		return v, err
	}
	if run.Aborted || run.ScheduleEpoch.IsZero() || run.Elapsed < workload.Workload.Warmup+workload.Workload.Duration {
		return v, fmt.Errorf("invalid loadgen run")
	}
	v.PlanDigest = complete.PlanDigest
	v.MeasurementStartUTC = run.ScheduleEpoch.Add(workload.Workload.Warmup)
	v.MeasurementEndUTC = v.MeasurementStartUTC.Add(workload.Workload.Duration)
	lastStage := ""
	err = jsonLines(path("server-metrics.jsonl"), func(b []byte) error {
		var record struct {
			Stage string `json:"stage"`
			RunID string `json:"runId"`
		}
		if err := json.Unmarshal(b, &record); err != nil {
			return err
		}
		if record.RunID != ready.RunID || (v.MetricRecords == 0 && record.Stage != "initial") || (v.MetricRecords > 0 && record.Stage == "initial") || lastStage == "final" {
			return fmt.Errorf("invalid metrics stream")
		}
		if record.Stage != "initial" && record.Stage != "periodic" && record.Stage != "final" {
			return fmt.Errorf("invalid metrics stage")
		}
		v.MetricRecords++
		lastStage = record.Stage
		return nil
	})
	if err != nil {
		return v, err
	}
	if lastStage != "final" || v.MetricRecords < 2 {
		return v, fmt.Errorf("missing final metrics")
	}
	if o.snapshots() {
		err = jsonLines(path("snapshot-events.jsonl"), func(b []byte) error {
			var event struct {
				RunID       string    `json:"runId"`
				Outcome     string    `json:"outcome"`
				StartedUTC  time.Time `json:"startedUTC"`
				FinishedUTC time.Time `json:"finishedUTC"`
			}
			if err := json.Unmarshal(b, &event); err != nil {
				return err
			}
			if event.RunID != ready.RunID || event.StartedUTC.IsZero() || event.FinishedUTC.Before(event.StartedUTC) {
				return fmt.Errorf("invalid snapshot event")
			}
			if event.Outcome == "COMPLETED" && event.StartedUTC.Before(v.MeasurementEndUTC) && event.FinishedUTC.After(v.MeasurementStartUTC) {
				v.SnapshotsOverlappingMeasurement++
			}
			return nil
		})
		if err != nil {
			return v, err
		}
		if v.SnapshotsOverlappingMeasurement == 0 {
			return v, fmt.Errorf("no completed snapshot overlaps measurement; lengthen run or shorten interval")
		}
	}
	if o.profiles() {
		var profile struct {
			StartedUTC    time.Time `json:"startedUTC"`
			CPUStoppedUTC time.Time `json:"cpuStoppedUTC"`
			FinishedUTC   time.Time `json:"finishedUTC"`
			PostDrainGC   bool      `json:"postDrainGC"`
		}
		if err := readJSON(path("profiles/profile.json"), &profile); err != nil {
			return v, err
		}
		if profile.StartedUTC.IsZero() || profile.StartedUTC.After(run.ScheduleEpoch) || profile.CPUStoppedUTC.Before(run.FinishedUTC) || profile.FinishedUTC.Before(profile.CPUStoppedUTC) || !profile.PostDrainGC {
			return v, fmt.Errorf("profile does not cover the run")
		}
		for _, name := range []string{"cpu.pprof", "heap.pprof", "allocs.pprof"} {
			if err := verifyProfile(path("profiles/" + name)); err != nil {
				return v, err
			}
		}
		v.ProfileRecorded = true
	}
	return v, nil
}
func verifyProfile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(r, (128<<20)+1))
	err = errors.Join(err, r.Close())
	if n == 0 || n > 128<<20 {
		return errors.Join(err, fmt.Errorf("empty or oversized profile"))
	}
	return err
}
