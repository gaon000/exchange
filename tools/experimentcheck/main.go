package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/telemetry"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

func read(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: experimentcheck LOADGEN_DIR NEW_OUTPUT")
	}
	dir := args[0]
	var diagnosticManifest struct {
		DiagnosticsInterval time.Duration `json:"diagnosticsIntervalNanos"`
	}
	if err := read(filepath.Join(dir, "manifest.json"), &diagnosticManifest); err != nil {
		return err
	}
	names := []string{"manifest.json", "plan.jsonl", "preparation.json", "samples.jsonl", "run.json", "summary.json"}
	if diagnosticManifest.DiagnosticsInterval != 0 {
		if diagnosticManifest.DiagnosticsInterval < 100*time.Millisecond || diagnosticManifest.DiagnosticsInterval > time.Minute {
			return fmt.Errorf("invalid diagnostics interval")
		}
		names = append(names, "dispatch.jsonl", "generator-metrics.jsonl")
	}
	var seal struct {
		PlanDigest string            `json:"planDigest"`
		Files      map[string]string `json:"sha256"`
	}
	complete := true
	inputHashes := map[string]string{}
	if _, err := os.Stat(filepath.Join(dir, "completed.json")); os.IsNotExist(err) {
		complete = false
		var failure any
		if err := read(filepath.Join(dir, "failed.json"), &failure); err != nil {
			return err
		}
		var m struct {
			PlanDigest string `json:"planDigest"`
		}
		if err := read(filepath.Join(dir, "manifest.json"), &m); err != nil {
			return err
		}
		seal.PlanDigest = m.PlanDigest
		seal.Files = map[string]string{}
		for _, name := range names {
			seal.Files[name] = ""
		}
	} else {
		if err := read(filepath.Join(dir, "completed.json"), &seal); err != nil {
			return err
		}
	}
	if len(seal.Files) != len(names) {
		return fmt.Errorf("%s", "unexpected artifact file count")
	}
	for _, name := range names {
		want, ok := seal.Files[name]
		if !ok {
			return fmt.Errorf("%s", "missing artifact: "+name)
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s", "invalid artifact path")
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		got := sha256.Sum256(b)
		inputHashes[name] = hex.EncodeToString(got[:])
		if complete && inputHashes[name] != want {
			return fmt.Errorf("%s", "artifact hash mismatch: "+name)
		}
	}
	var manifest struct {
		Settings struct {
			Workload  bench.Spec       `json:"workload"`
			Execution bench.RunOptions `json:"execution"`
		} `json:"settings"`
		Server struct {
			RunID string `json:"runId"`
		} `json:"server"`
	}
	if err := read(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return err
	}
	plan, err := bench.Build(manifest.Server.RunID, manifest.Settings.Workload)
	if err != nil {
		return err
	}
	if plan.Digest() != seal.PlanDigest {
		return fmt.Errorf("%s", "plan digest mismatch")
	}
	var run bench.RunResult
	if err := read(filepath.Join(dir, "run.json"), &run); err != nil {
		return err
	}
	if run.DispatchRecorded != (diagnosticManifest.DiagnosticsInterval != 0) {
		return fmt.Errorf("dispatch recording disagrees with manifest")
	}
	f, err := os.Open(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 1<<20)
	for scanner.Scan() {
		var s bench.Sample
		if err := json.Unmarshal(scanner.Bytes(), &s); err != nil {
			return err
		}
		run.Samples = append(run.Samples, s)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if diagnosticManifest.DiagnosticsInterval != 0 {
		if err := verifyGeneratorDiagnostics(dir, manifest.Server.RunID, settings{manifest.Settings.Workload, manifest.Settings.Execution}, diagnosticManifest.DiagnosticsInterval, bench.RunResult{Options: run.Options, Elapsed: run.Elapsed, DispatchRecorded: run.DispatchRecorded}); err != nil {
			return err
		}
	}
	summary, err := bench.Summarize(plan, run)
	if err != nil {
		return err
	}
	var recorded bench.Summary
	if err := read(filepath.Join(dir, "summary.json"), &recorded); err != nil {
		return err
	}
	if !reflect.DeepEqual(summary, recorded) {
		return fmt.Errorf("%s", "raw recomputation differs from recorded summary")
	}
	windows := map[string][]bench.WindowSummary{}
	for _, width := range []time.Duration{5 * time.Second, 60 * time.Second} {
		if manifest.Settings.Workload.Duration%width != 0 {
			continue
		}
		w, err := bench.SummarizeWindows(plan, run, width)
		if err != nil {
			return err
		}
		windows[width.String()] = w
	}
	out, err := os.OpenFile(args[1], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	value := map[string]any{"rawVerified": true, "loadgenComplete": complete, "inputSHA256": inputHashes, "planDigest": seal.PlanDigest, "measurementStartUTC": run.ScheduleEpoch.Add(manifest.Settings.Workload.Warmup),
		"measurementEndUTC": run.ScheduleEpoch.Add(manifest.Settings.Workload.Warmup + manifest.Settings.Workload.Duration), "summary": summary, "windows": windows}
	if err := json.NewEncoder(out).Encode(value); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Raw summary and file hashes verified:", args[1])
	return err
}

type settings struct {
	Workload  bench.Spec
	Execution bench.RunOptions
}

func readJSON(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}
func jsonLines(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 1<<20)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func verifyGeneratorDiagnostics(dir, runID string, workload settings, interval time.Duration, run bench.RunResult) error {
	path := func(name string) string { return filepath.Join(dir, name) }
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
