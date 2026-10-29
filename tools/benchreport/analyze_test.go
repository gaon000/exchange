package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/buildinfo"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/telemetry"
	"github.com/gaon000/exchange/internal/wal"
)

func writeTest(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
func writeLines(t *testing.T, path string, values []any) {
	t.Helper()
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	for _, v := range values {
		if err := e.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func sealFixture(t *testing.T, dir, runID, planDigest string) {
	t.Helper()
	loadgen := map[string]string{}
	for _, name := range []string{"manifest.json", "plan.jsonl", "preparation.json", "samples.jsonl", "run.json", "summary.json"} {
		h, err := hashFile(filepath.Join(dir, "loadgen", name))
		if err != nil {
			t.Fatal(err)
		}
		loadgen[name] = h
	}
	writeTest(t, filepath.Join(dir, "loadgen/completed.json"), map[string]any{"version": 1, "planDigest": planDigest, "sha256": loadgen})
	hashes := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "completed.json" {
			return nil
		}
		h, err := hashFile(path)
		hashes[filepath.ToSlash(rel)] = h
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(dir, "completed.json"), seal{Version: 1, RunID: runID, Files: hashes})
}

func fixture(t *testing.T) (string, criteria, manifest, bench.RunResult) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"bin", "loadgen"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	runID := "11111111-1111-4111-8111-111111111111"
	config := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 10000, QuoteAtoms: 1000000}, {AccountID: "b", BaseLots: 10000, QuoteAtoms: 1000000}})
	g, err := wal.NewGenesis(runID, config)
	if err != nil {
		t.Fatal(err)
	}
	s := settings{Workload: bench.Spec{Namespace: "criteria-test", Seed: 17, Accounts: []string{"a", "b"}, Rate: 10, Warmup: time.Second, Duration: 2 * time.Second, MidPriceTicks: 100, PriceStepTicks: 1, Depth: 1, QuantityLots: 1, SeedQuantityLots: 10, Mix: bench.Mix{Rest: 100}}, Execution: bench.RunOptions{MaxInFlight: 8, RequestTimeout: time.Second, MaxLateness: 20 * time.Millisecond}}
	p, err := bench.Build(runID, s.Workload)
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	run := bench.RunResult{ScheduleEpoch: epoch, FinishedUTC: epoch.Add(3 * time.Second), Elapsed: 3 * time.Second, Options: s.Execution}
	requests, observations := []any{}, []any{}
	for _, req := range p.Preparation() {
		requests = append(requests, req)
	}
	for i := 0; i < p.Count(); i++ {
		req := p.Request(i)
		requests = append(requests, req)
		sample := bench.Sample{Index: i, StartedAt: req.Scheduled + time.Millisecond, WrittenAt: -1, FinishedAt: req.Scheduled + 3*time.Millisecond, Outcome: bench.Outcome{Class: bench.Business, HTTPStatus: 200, Code: string(d.Accepted), Certainty: "COMPLETED", CommandSeq: d.Int(i + 3), OrderStatus: d.Open, Fingerprint: fmt.Sprintf("%064x", i+3)}}
		run.Samples = append(run.Samples, sample)
		observations = append(observations, sample)
	}
	summary, err := bench.Summarize(p, run)
	if err != nil {
		t.Fatal(err)
	}
	build := buildinfo.Info{GoVersion: "test-go", GOOS: "darwin", GOARCH: "arm64", Revision: "revision", RevisionKnown: true}
	m := manifest{Version: 1, PlanDigest: p.Digest(), Settings: s, InitialConfig: config, Generator: build, Server: serverRecord{Event: "ready", RunID: runID, ConfigHash: g.ConfigHash, Build: build, Runtime: json.RawMessage(`{"batchDelay":"1ms"}`), HTTP: json.RawMessage(`{"resultTimeout":"1s"}`)}}
	m.Server.Metrics.Enabled = true
	m.Server.Metrics.Instrumentation = true
	m.Server.Metrics.Interval = "100ms"
	m.Server.Experiments.SnapshotInterval = "0s"
	writeTest(t, filepath.Join(dir, "manifest.json"), map[string]string{"scenario": "normal"})
	writeTest(t, filepath.Join(dir, "server-ready.json"), m.Server)
	writeTest(t, filepath.Join(dir, "config.json"), config)
	writeTest(t, filepath.Join(dir, "workload.json"), s)
	writeTest(t, filepath.Join(dir, "verification.json"), map[string]bool{"checked": true})
	writeTest(t, filepath.Join(dir, "environment.json"), map[string]any{"logicalCPUs": 4, "runnerGOMAXPROCS": 4, "runtimeEnvironment": map[string]string{}, "hardware": map[string]any{"cpu": map[string]string{"value": "cpu"}, "ramBytes": map[string]string{"value": "1000"}, "model": map[string]string{"value": "test"}, "os": map[string]string{"value": "macOS"}}, "storage": map[string]any{"type": "apfs", "blockBytes": "4096", "local": true}})
	writeTest(t, filepath.Join(dir, "loadgen/manifest.json"), m)
	writeTest(t, filepath.Join(dir, "loadgen/run.json"), run)
	writeTest(t, filepath.Join(dir, "loadgen/summary.json"), summary)
	writeTest(t, filepath.Join(dir, "loadgen/preparation.json"), bench.PreparationReport{Complete: true, Samples: make([]bench.PreparationSample, len(p.Preparation()))})
	writeLines(t, filepath.Join(dir, "loadgen/plan.jsonl"), requests)
	writeLines(t, filepath.Join(dir, "loadgen/samples.jsonl"), observations)
	metrics := []any{}
	for i := 0; i <= 30; i++ {
		v := metric{Version: 1, Stage: "periodic", RunID: runID, StartedUTC: epoch.Add(time.Duration(i) * 100 * time.Millisecond), FinishedUTC: epoch.Add(time.Duration(i)*100*time.Millisecond + time.Microsecond), Process: telemetry.Process{OSAvailable: true}}
		if i == 0 {
			v.Stage = "initial"
		}
		if i == 30 {
			v.Stage = "final"
		}
		metrics = append(metrics, v)
	}
	writeLines(t, filepath.Join(dir, "server-metrics.jsonl"), metrics)
	for _, name := range []string{"exchange", "loadgen"} {
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sealFixture(t, dir, runID, p.Digest())
	c := criteria{Version: 1, P99Limit: 20 * time.Millisecond, Window: time.Second, MinimumRepeats: 3, MaxQueueGrowth: 64, MinimumMetricsPerWindow: 8, MinimumSnapshotOverlaps: 2, MetricsInterval: 100 * time.Millisecond, SnapshotInterval: time.Second, RateResolution: 1, configHash: g.ConfigHash, expected: s}
	return dir, c, m, run
}

func TestAnalyzerRecomputesAndRejectsUnsealedOrAlteredEvidence(t *testing.T) {
	dir, c, m, _ := fixture(t)
	r, err := analyze(dir, c)
	if err != nil || !r.Pass || !r.Sealed || !r.Recomputed || len(r.Windows) != 2 || r.Windows[0].MetricSamples != 10 {
		t.Fatal(r, err)
	}
	if err := os.Remove(filepath.Join(dir, "completed.json")); err != nil {
		t.Fatal(err)
	}
	r, err = analyze(dir, c)
	if err != nil || r.Pass || !r.Recomputed || !strings.Contains(strings.Join(r.Reasons, ","), "UNSEALED") {
		t.Fatal(r, err)
	}
	sealFixture(t, dir, m.Server.RunID, m.PlanDigest)
	path := filepath.Join(dir, "loadgen/samples.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(" ")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := analyze(dir, c); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatal(err)
	}
}

func TestAnalyzerDoesNotTrustRehashedSummaryOrPlan(t *testing.T) {
	for _, name := range []string{"summary", "plan"} {
		t.Run(name, func(t *testing.T) {
			dir, c, m, _ := fixture(t)
			if name == "summary" {
				var s bench.Summary
				if err := readJSON(filepath.Join(dir, "loadgen/summary.json"), &s); err != nil {
					t.Fatal(err)
				}
				s.Measurement.NewOrdersPerSecond++
				writeTest(t, filepath.Join(dir, "loadgen/summary.json"), s)
			} else {
				if err := os.WriteFile(filepath.Join(dir, "loadgen/plan.jsonl"), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			sealFixture(t, dir, m.Server.RunID, m.PlanDigest)
			if _, err := analyze(dir, c); err == nil {
				t.Fatal("rehashed incorrect evidence accepted")
			}
		})
	}
}

func TestAnalyzerRejectsSlowWindowAndQueueGrowth(t *testing.T) {
	dir, c, m, run := fixture(t)
	// Keep the saved summary honest: the gate must reject a bad window even
	// though integrity and recomputation still succeed.
	run.Samples[15].FinishedAt += 25 * time.Millisecond
	p, err := bench.Build(m.Server.RunID, m.Settings.Workload)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := bench.Summarize(p, run)
	if err != nil {
		t.Fatal(err)
	}
	values := []any{}
	for _, v := range run.Samples {
		values = append(values, v)
	}
	writeLines(t, filepath.Join(dir, "loadgen/samples.jsonl"), values)
	writeTest(t, filepath.Join(dir, "loadgen/summary.json"), summary)
	var metrics []any
	if err := lines(filepath.Join(dir, "server-metrics.jsonl"), func(b []byte) error {
		var v metric
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if !v.StartedUTC.Before(run.ScheduleEpoch.Add(2 * time.Second)) {
			v.Runtime.CommandQueueDepth = 65
		}
		metrics = append(metrics, v)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(dir, "server-metrics.jsonl"), metrics)
	sealFixture(t, dir, m.Server.RunID, m.PlanDigest)
	r, err := analyze(dir, c)
	reason := strings.Join(r.Reasons, ",")
	if err != nil || r.Pass || !r.Recomputed || !strings.Contains(reason, "WINDOW_0_P99") || !strings.Contains(reason, "QUEUE_GROWTH") {
		t.Fatal(r, err)
	}
}

func TestGroupingRequiresEveryIndependentRepeat(t *testing.T) {
	cases := []caseReport{{RunID: "a", Series: "same", Rate: 100, Pass: true}, {RunID: "b", Series: "same", Rate: 100, Pass: true}, {RunID: "c", Series: "same", Rate: 100, Pass: true}}
	g := groupCases(cases, 3)
	if len(g) != 1 || !g[0].Qualified {
		t.Fatal(g)
	}
	cases = append(cases, caseReport{RunID: "d", Series: "same", Rate: 100, Pass: false})
	g = groupCases(cases, 3)
	if g[0].Qualified || g[0].Runs != 4 {
		t.Fatal(g)
	}
	cases = cases[:3]
	cases[2].RunID = "a"
	g = groupCases(cases, 3)
	if g[0].Qualified || cases[0].Pass || cases[2].Pass {
		t.Fatal(g, cases)
	}
}

func TestCriteriaAndReportOutputRules(t *testing.T) {
	c, err := loadCriteria("../../config/benchmarks/criteria-v1.json")
	if err != nil || c.P99Limit != 20*time.Millisecond || c.expected.Workload.Duration != 20*time.Second {
		t.Fatal(c, err)
	}
	dir, _, _, _ := fixture(t)
	if err := run([]string{"--criteria", "../../config/benchmarks/criteria-v1.json", "--output", filepath.Join(dir, "new-report.json"), dir}, io.Discard, io.Discard); err == nil {
		t.Fatal("report written into sealed evidence")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--criteria", "../../config/benchmarks/criteria-v1.json", "--output", filepath.Join(alias, "new-report.json"), dir}, io.Discard, io.Discard); err == nil {
		t.Fatal("symlink hid output inside evidence")
	}
	out := filepath.Join(t.TempDir(), "report.json")
	if err := run([]string{"--criteria", "../../config/benchmarks/criteria-v1.json", "--output", out, dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var r report
	if err := readJSON(out, &r); err != nil || len(r.Cases) != 1 || r.Cases[0].Pass {
		t.Fatal(r, err)
	}
	if err := run([]string{"--output", out, dir, dir}, io.Discard, io.Discard); err == nil {
		t.Fatal("duplicate bundles accepted")
	}
}

func TestAnalyzerSeparatesOperationalAndGeneratorFailures(t *testing.T) {
	for _, local := range []bool{false, true} {
		dir, c, m, run := fixture(t)
		if local {
			run.Samples[15] = bench.Sample{Index: 15, Local: bench.LocalCapacity, StartedAt: -1, WrittenAt: -1, FinishedAt: 1500 * time.Millisecond}
		} else {
			run.Samples[15].Outcome = bench.Outcome{Class: bench.TransportTimeout, Certainty: "UNKNOWN"}
		}
		p, err := bench.Build(m.Server.RunID, m.Settings.Workload)
		if err != nil {
			t.Fatal(err)
		}
		s, err := bench.Summarize(p, run)
		if err != nil {
			t.Fatal(err)
		}
		values := []any{}
		for _, v := range run.Samples {
			values = append(values, v)
		}
		writeLines(t, filepath.Join(dir, "loadgen/samples.jsonl"), values)
		writeTest(t, filepath.Join(dir, "loadgen/summary.json"), s)
		sealFixture(t, dir, m.Server.RunID, m.PlanDigest)
		r, err := analyze(dir, c)
		if err != nil || r.Pass || !r.Recomputed {
			t.Fatal(r, err)
		}
		if local && r.Windows[0].LocalFailures != 1 || !local && r.Windows[0].OperationalFailures != 1 {
			t.Fatal(r.Windows)
		}
	}
}

func TestAnalyzerRequiresMetricCoverageAndActualSnapshotOverlap(t *testing.T) {
	dir, c, m, run := fixture(t)
	var metrics []any
	if err := lines(filepath.Join(dir, "server-metrics.jsonl"), func(b []byte) error {
		var v metric
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		at := v.StartedUTC.Sub(run.ScheduleEpoch)
		if at < time.Second || at >= 1300*time.Millisecond {
			metrics = append(metrics, v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(dir, "server-metrics.jsonl"), metrics)
	sealFixture(t, dir, m.Server.RunID, m.PlanDigest)
	r, err := analyze(dir, c)
	if err != nil || r.Pass || !strings.Contains(strings.Join(r.Reasons, ","), "METRICS_COVERAGE") {
		t.Fatal(r, err)
	}
	dir, c, m, run = fixture(t)
	m.Server.Experiments.SnapshotLog = "events"
	m.Server.Experiments.SnapshotInterval = c.SnapshotInterval.String()
	writeTest(t, filepath.Join(dir, "server-ready.json"), m.Server)
	writeTest(t, filepath.Join(dir, "loadgen/manifest.json"), m)
	writeTest(t, filepath.Join(dir, "manifest.json"), map[string]string{"scenario": "snapshot"})
	writeLines(t, filepath.Join(dir, "snapshot-events.jsonl"), []any{map[string]any{"runId": m.Server.RunID, "outcome": "COMPLETED", "startedUTC": run.ScheduleEpoch, "finishedUTC": run.ScheduleEpoch.Add(time.Millisecond)}})
	sealFixture(t, dir, m.Server.RunID, m.PlanDigest)
	r, err = analyze(dir, c)
	if err != nil || r.Pass || r.SnapshotOverlaps != 0 || !strings.Contains(strings.Join(r.Reasons, ","), "SNAPSHOT_OVERLAP") {
		t.Fatal(r, err)
	}
}
