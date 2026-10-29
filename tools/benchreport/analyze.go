package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/wal"
)

func analyze(dir string, c criteria) (caseReport, error) {
	r := caseReport{Bundle: dir, Reasons: []string{}, Windows: []windowReport{}}
	path := func(name string) string { return filepath.Join(dir, name) }
	var m manifest
	if err := readJSON(path("loadgen/manifest.json"), &m); err != nil {
		return r, err
	}
	var experiment struct {
		Scenario string `json:"scenario"`
	}
	if err := readJSON(path("manifest.json"), &experiment); err != nil {
		return r, err
	}
	r.RunID, r.Scenario, r.Rate, r.ServerBuild = m.Server.RunID, experiment.Scenario, m.Settings.Workload.Rate, m.Server.Build
	var env environmentRecord
	if err := readJSON(path("environment.json"), &env); err != nil {
		return r, err
	}
	var err error
	for _, name := range []string{"bin/exchange", "bin/loadgen"} {
		info, err := os.Lstat(path(name))
		if err != nil {
			return r, err
		}
		if !info.Mode().IsRegular() {
			return r, fmt.Errorf("non-regular binary %s", name)
		}
	}
	r.ServerSHA, err = hashFile(path("bin/exchange"))
	if err != nil {
		return r, err
	}
	r.GeneratorSHA, err = hashFile(path("bin/loadgen"))
	if err != nil {
		return r, err
	}
	// Normalize run-specific paths/rate; changes to the binary, settings or
	// observed hardware/runtime environment form a separate comparison series.
	normalized := m
	normalized.PlanDigest = ""
	normalized.Server.RunID = ""
	normalized.Settings.Workload.Rate = 0
	if normalized.Server.Experiments.SnapshotLog != "" {
		normalized.Server.Experiments.SnapshotLog = "enabled"
	}
	if normalized.Server.Experiments.ProfileDir != "" {
		normalized.Server.Experiments.ProfileDir = "enabled"
	}
	r.Series = digest([]byte(canonical([]any{normalized, r.Scenario, r.ServerSHA, r.GeneratorSHA, env})))
	s, sealErr := verifyFiles(dir)
	if sealErr != nil {
		if _, err := os.Lstat(path("completed.json")); !errors.Is(err, os.ErrNotExist) {
			return r, sealErr
		}
		r.Reasons = append(r.Reasons, "UNSEALED_EXPERIMENT")
	} else {
		r.Sealed = true
		if s.RunID != r.RunID {
			return r, fmt.Errorf("seal run identity mismatch")
		}
	}
	var ready serverRecord
	if err := readJSON(path("server-ready.json"), &ready); err != nil {
		return r, err
	}
	if canonical(ready) != canonical(m.Server) || ready.Event != "ready" {
		return r, fmt.Errorf("ready/manifest mismatch")
	}
	g, err := wal.NewGenesis(r.RunID, m.InitialConfig)
	if err != nil {
		return r, err
	}
	if g.ConfigHash != m.Server.ConfigHash {
		return r, fmt.Errorf("initial config hash mismatch")
	}
	p, err := bench.Build(r.RunID, m.Settings.Workload)
	if err != nil {
		return r, err
	}
	if p.Digest() != m.PlanDigest || m.Version != bench.Version {
		return r, fmt.Errorf("rebuilt plan digest/version mismatch")
	}
	if r.Sealed {
		var loadgenSeal struct {
			Version    int               `json:"version"`
			PlanDigest string            `json:"planDigest"`
			Files      map[string]string `json:"sha256"`
		}
		if err := readJSON(path("loadgen/completed.json"), &loadgenSeal); err != nil {
			return r, err
		}
		if loadgenSeal.Version != bench.Version || loadgenSeal.PlanDigest != p.Digest() || len(loadgenSeal.Files) != 6 {
			return r, fmt.Errorf("invalid loadgen completion marker")
		}
		for _, name := range []string{"manifest.json", "plan.jsonl", "preparation.json", "samples.jsonl", "run.json", "summary.json"} {
			if loadgenSeal.Files[name] != s.Files["loadgen/"+name] {
				return r, fmt.Errorf("loadgen completion hash mismatch")
			}
		}
	}
	if err := bench.ValidatePreparation(p, m.InitialConfig); err != nil {
		return r, err
	}
	prepared := p.Preparation()
	planCount := 0
	if err := lines(path("loadgen/plan.jsonl"), func(b []byte) error {
		var req bench.Request
		if err := json.Unmarshal(b, &req); err != nil {
			return err
		}
		var expected bench.Request
		if planCount < len(prepared) {
			expected = prepared[planCount]
		} else if planCount-len(prepared) < p.Count() {
			expected = p.Request(planCount - len(prepared))
		} else {
			return fmt.Errorf("extra planned request")
		}
		if !reflect.DeepEqual(req, expected) {
			return fmt.Errorf("raw plan differs from rebuilt request %d", planCount)
		}
		planCount++
		return nil
	}); err != nil {
		return r, err
	}
	if planCount != len(prepared)+p.Count() {
		return r, fmt.Errorf("missing planned requests")
	}
	var prep bench.PreparationReport
	if err := readJSON(path("loadgen/preparation.json"), &prep); err != nil {
		return r, err
	}
	if !prep.Complete || len(prep.Samples) != len(prepared) {
		return r, fmt.Errorf("incomplete preparation")
	}
	var run bench.RunResult
	if err := readJSON(path("loadgen/run.json"), &run); err != nil {
		return r, err
	}
	if run.Options != m.Settings.Execution {
		return r, fmt.Errorf("recorded execution settings mismatch")
	}
	run.Samples = make([]bench.Sample, 0, p.Count())
	err = lines(path("loadgen/samples.jsonl"), func(b []byte) error {
		if len(run.Samples) >= p.Count() {
			return fmt.Errorf("extra sample")
		}
		var value bench.Sample
		if err := json.Unmarshal(b, &value); err != nil {
			return err
		}
		run.Samples = append(run.Samples, value)
		return nil
	})
	if err != nil {
		return r, err
	}
	r.Summary, err = bench.Summarize(p, run)
	if err != nil {
		return r, err
	}
	var recorded bench.Summary
	if err := readJSON(path("loadgen/summary.json"), &recorded); err != nil {
		return r, err
	}
	if canonical(r.Summary) != canonical(recorded) {
		return r, fmt.Errorf("summary differs from raw sample recomputation")
	}
	r.Recomputed = true
	if run.ScheduleEpoch.IsZero() || run.FinishedUTC.IsZero() || (!run.Aborted && run.FinishedUTC.Before(run.ScheduleEpoch.Add(m.Settings.Workload.Warmup+m.Settings.Workload.Duration))) {
		return r, fmt.Errorf("missing run timestamps")
	}
	expected := c.expected
	expected.Workload.Rate = r.Rate
	if !reflect.DeepEqual(m.Settings, expected) || m.Server.ConfigHash != c.configHash || r.Rate%c.RateResolution != 0 {
		r.Reasons = append(r.Reasons, "FIXED_INPUT_MISMATCH")
	}
	if !m.Server.Build.RevisionKnown || m.Server.Build.Modified || !m.Generator.RevisionKnown || m.Generator.Modified {
		r.Reasons = append(r.Reasons, "UNPINNED_BINARY_BUILD")
	}
	if !m.Server.Metrics.Enabled || !m.Server.Metrics.Instrumentation || m.Server.Metrics.Interval != c.MetricsInterval.String() {
		r.Reasons = append(r.Reasons, "METRICS_SETTINGS_MISMATCH")
	}
	if env.LogicalCPUs < 1 || env.GOMAXPROCS < 1 || !env.Storage.Local || env.Storage.Type != "apfs" {
		r.Reasons = append(r.Reasons, "INCOMPLETE_ENVIRONMENT")
	}
	for _, key := range []string{"cpu", "ramBytes", "model", "os"} {
		v := env.Hardware[key]
		if v.Value == "" || v.Error != "" {
			r.Reasons = append(r.Reasons, "MISSING_ENVIRONMENT_"+key)
		}
	}
	if !r.Summary.MeasurementValid {
		r.Reasons = append(r.Reasons, "INVALID_MEASUREMENT")
	}
	for _, phase := range []bench.PhaseSummary{r.Summary.Warmup, r.Summary.Measurement} {
		if phase.GeneratorDropRate > 0 || phase.UnknownLogicalCommands > 0 {
			r.Reasons = append(r.Reasons, string(phase.Phase)+"_GENERATOR_OR_UNKNOWN")
		}
		for class, count := range phase.Responses {
			if class != bench.Business && count > 0 {
				r.Reasons = append(r.Reasons, string(phase.Phase)+"_"+string(class))
			}
		}
	}
	meter := r.Summary.Measurement
	if meter.RequestLatency.Count == 0 || meter.UniqueNewOrderScheduledLatency.Count == 0 || meter.RequestLatency.P99 > c.P99Limit || meter.UniqueNewOrderScheduledLatency.P99 > c.P99Limit {
		r.Reasons = append(r.Reasons, "GLOBAL_P99")
	}
	windows, err := bench.SummarizeWindows(p, run, c.Window)
	if err != nil {
		return r, err
	}
	for _, w := range windows {
		r.Windows = append(r.Windows, windowReport{WindowSummary: w})
		if w.Started == 0 || w.RequestLatency.P99 > c.P99Limit || w.ScheduledAttemptLatency.P99 > c.P99Limit {
			r.Reasons = append(r.Reasons, fmt.Sprintf("WINDOW_%d_P99", len(r.Windows)-1))
		}
	}
	if err := readMetrics(path("server-metrics.jsonl"), &r, run.ScheduleEpoch); err != nil {
		return r, err
	}
	for i, w := range r.Windows {
		if w.MetricSamples < c.MinimumMetricsPerWindow {
			r.Reasons = append(r.Reasons, fmt.Sprintf("WINDOW_%d_METRICS_COVERAGE", i))
		}
	}
	if r.Windows[len(r.Windows)-1].QueueMean-r.Windows[0].QueueMean > c.MaxQueueGrowth {
		r.Reasons = append(r.Reasons, "QUEUE_GROWTH")
	}
	if err := checkScenario(dir, &r, m, run, c); err != nil {
		return r, err
	}
	sort.Strings(r.Reasons)
	r.Pass = len(r.Reasons) == 0
	return r, nil
}
