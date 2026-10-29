package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/bench"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "experiment-test-child" {
		switch os.Args[2] {
		case "fail":
			fmt.Fprintln(os.Stderr, "expected child failure")
			os.Exit(3)
		case "wait":
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGTERM)
			fmt.Println(`{"event":"ready","runId":"helper"}`)
			<-ch
			os.Exit(0)
		case "ignore":
			signal.Ignore(syscall.SIGTERM)
			fmt.Println(`{"event":"ready","runId":"helper"}`)
			for {
				time.Sleep(time.Second)
			}
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestOptionsAndFreshArtifacts(t *testing.T) {
	base := []string{"--config", "config", "--workload", "workload", "--output", "output"}
	for _, extra := range [][]string{{"--scenario", "bad"}, {"--metrics-interval", "1ms"}, {"--snapshot-interval", "0"}, {"--server-arg=--data-dir=old"}, {"--server-arg=--batch-delay"}, {"--server-arg=--batch-delay=1ms", "--server-arg=--batch-delay=2ms"}} {
		if _, err := parse(append(append([]string{}, base...), extra...), io.Discard); err == nil {
			t.Fatal(extra)
		}
	}
	o, err := parse(append(base, "--scenario", "snapshot-profile", "--server-arg=--batch-delay=2ms"), io.Discard)
	if err != nil || !o.snapshots() || !o.profiles() || len(o.ServerArgs) != 1 {
		t.Fatal(o, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, base, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(source, target, false); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(source, target, false); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if err := copyFile(dir, filepath.Join(dir, "invalid"), false); err == nil {
		t.Fatal("directory copied")
	}
	hashes, err := bundleHashes(dir)
	if err != nil || len(hashes) != 2 || hashes["source"] != hashes["target"] {
		t.Fatal(hashes, err)
	}
	if err := os.Symlink(source, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := bundleHashes(dir); err == nil {
		t.Fatal("symlink included in evidence")
	}
}

func TestChildOwnershipDrainAndFailure(t *testing.T) {
	for _, mode := range []string{"wait", "ignore", "fail"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "stdout")
			p, err := startChild([]string{os.Args[0], "experiment-test-child", mode}, out, filepath.Join(dir, "stderr"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.stop(100 * time.Millisecond) })
			if mode == "fail" {
				if err := p.wait(context.Background()); err == nil {
					t.Fatal("child failure lost")
				}
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := awaitReady(ctx, p, out); err != nil {
				t.Fatal(err)
			}
			timeout := 100 * time.Millisecond
			if mode == "wait" {
				timeout = 3 * time.Second
			} // race runtime exit bookkeeping
			err = p.stop(timeout)
			if mode == "wait" && err != nil {
				t.Fatal(err)
			}
			if mode == "ignore" && (err == nil || !strings.Contains(err.Error(), "killed own process")) {
				t.Fatal(err)
			}
			select {
			case <-p.done:
			default:
				t.Fatal("child not joined")
			}
		})
	}
}

func evidenceFixture(t *testing.T) (string, ready, settings) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "loadgen"), 0700); err != nil {
		t.Fatal(err)
	}
	r := ready{Event: "ready", RunID: "run", ConfigHash: "config"}
	s := settings{Workload: bench.Spec{Warmup: time.Second, Duration: time.Second}}
	epoch := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	digest := strings.Repeat("0", 64)
	values := map[string]any{
		"manifest.json": map[string]any{"planDigest": digest, "server": r},
		"plan.jsonl":    map[string]string{"input": "retained"}, "samples.jsonl": map[string]string{"sample": "retained"}, "preparation.json": map[string]bool{"complete": true},
		"run.json":     bench.RunResult{ScheduleEpoch: epoch, Elapsed: 2 * time.Second, FinishedUTC: epoch.Add(2 * time.Second)},
		"summary.json": bench.Summary{Version: 1, PlanDigest: digest, MeasurementValid: true, Measurement: bench.PhaseSummary{Duration: time.Second}},
	}
	hashes := map[string]string{}
	for name, value := range values {
		path := filepath.Join(dir, "loadgen", name)
		if err := writeJSON(path, value); err != nil {
			t.Fatal(err)
		}
		hash, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hashes[name] = hash
	}
	if err := writeJSON(filepath.Join(dir, "loadgen/completed.json"), map[string]any{"version": 1, "planDigest": digest, "sha256": hashes}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server-metrics.jsonl"), []byte("{\"stage\":\"initial\",\"runId\":\"run\"}\n{\"stage\":\"final\",\"runId\":\"run\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, r, s
}

func TestEvidenceHashesFinalMetricsAndSnapshotOverlap(t *testing.T) {
	dir, r, s := evidenceFixture(t)
	v, err := verifyEvidence(dir, r, s, options{Scenario: "normal"})
	if err != nil || v.MetricRecords != 2 {
		t.Fatal(v, err)
	}
	log := filepath.Join(dir, "snapshot-events.jsonl")
	for _, test := range []struct {
		outcome string
		at      time.Time
		want    bool
	}{{"COMPLETED", v.MeasurementStartUTC.Add(-time.Second), false}, {"UNKNOWN", v.MeasurementStartUTC, false}, {"COMPLETED", v.MeasurementStartUTC, true}} {
		data, err := json.Marshal(map[string]any{"runId": "run", "outcome": test.outcome, "startedUTC": test.at, "finishedUTC": test.at.Add(time.Millisecond)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(log, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = verifyEvidence(dir, r, s, options{Scenario: "snapshot"})
		if (err == nil) != test.want {
			t.Fatal(test, err)
		}
	}
	file := filepath.Join(dir, "loadgen/samples.jsonl")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(bytes.Clone(original), ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyEvidence(dir, r, s, options{}); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server-metrics.jsonl"), []byte("{\"stage\":\"initial\",\"runId\":\"run\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyEvidence(dir, r, s, options{}); err == nil {
		t.Fatal("missing final metrics accepted")
	}
}
