//go:build darwin && cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/wal"
)

func TestNativeExperimentBundleAndFailureCleanup(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command(filepath.Join(root, "scripts/go"), "build", "-o", bin+"/", "./cmd/exchange", "./cmd/loadgen")
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(data), err)
	}
	var workload settings
	if err := readJSON(filepath.Join(root, "config/examples/workload.json"), &workload); err != nil {
		t.Fatal(err)
	}
	workload.Workload.Rate = 20
	workload.Workload.Warmup = 100 * time.Millisecond
	workload.Workload.Duration = 600 * time.Millisecond
	workload.Execution.MaxLateness = 500 * time.Millisecond
	wp := filepath.Join(t.TempDir(), "workload.json")
	if err := writeJSON(wp, workload); err != nil {
		t.Fatal(err)
	}
	base := []string{"--exchange", filepath.Join(bin, "exchange"), "--loadgen", filepath.Join(bin, "loadgen"), "--config", filepath.Join(root, "config/examples/benchmark.json"), "--workload", wp, "--metrics-interval", "100ms", "--snapshot-interval", "100ms"}
	for _, scenario := range []string{"normal", "snapshot-profile"} {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "experiment")
			args := append(append([]string{}, base...), "--scenario", scenario, "--output", dir)
			var stdout bytes.Buffer
			if err := run(context.Background(), args, &stdout, io.Discard); err != nil {
				t.Fatal(err)
			}
			var complete struct {
				RunID string            `json:"runId"`
				Files map[string]string `json:"sha256"`
			}
			if err := readJSON(filepath.Join(dir, "completed.json"), &complete); err != nil {
				t.Fatal(err)
			}
			if len(complete.Files) < 20 || complete.RunID == "" {
				t.Fatal(complete)
			}
			for name, hash := range complete.Files {
				actual, err := hashFile(filepath.Join(dir, name))
				if err != nil || hash != actual {
					t.Fatal(name, err)
				}
			}
			var v verification
			if err := readJSON(filepath.Join(dir, "verification.json"), &v); err != nil {
				t.Fatal(err)
			}
			if v.RunID != complete.RunID || (scenario == "snapshot-profile" && (v.SnapshotsOverlappingMeasurement < 1 || !v.ProfileRecorded)) {
				t.Fatal(v)
			}
			if _, err := os.Stat(filepath.Join(dir, "failed.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unexpected failure marker", err)
			}
			before, err := os.ReadFile(filepath.Join(dir, "completed.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background(), args, io.Discard, io.Discard); !errors.Is(err, os.ErrExist) {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(dir, "completed.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("old experiment changed", err)
			}
		})
	}
	for _, failure := range []string{"bad-server-option", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "experiment")
			args := append(append([]string{}, base...), "--output", dir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "bad-server-option" {
				args = append(args, "--server-arg=--batch-delay=0")
			}
			done := make(chan error, 1)
			go func() { done <- run(ctx, args, io.Discard, io.Discard) }()
			if failure == "cancel" {
				deadline := time.Now().Add(10 * time.Second)
				for {
					var r ready
					if readJSON(filepath.Join(dir, "server-ready.json"), &r) == nil && r.Event == "ready" {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						t.Fatal("server not ready")
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failed experiment succeeded")
				}
			case <-time.After(40 * time.Second):
				cancel()
				t.Fatal("children did not terminate")
			}
			if _, err := os.Stat(filepath.Join(dir, "completed.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed experiment marked complete", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "failed.json"))
			if err != nil || !json.Valid(data) {
				t.Fatal(string(data), err)
			}
			store, err := wal.Open(filepath.Join(dir, "run"))
			if err != nil {
				t.Fatal("run lock leaked or data unreadable", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
