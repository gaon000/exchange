package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduleBoundariesAndJoin(t *testing.T) {
	for _, w := range []int{0, 2048} {
		rows, _, e := schedule(context.Background(), 20, w, false)
		if e != nil || len(rows) != 20 {
			t.Fatal(e, len(rows))
		}
		for i, r := range rows {
			if r.Index != i || r.Scheduled != int64(i)*10000000 || r.WaitEnd < r.WaitStart {
				t.Fatal(r)
			}
			if w == 0 && r.Worker != -1 {
				t.Fatal(r)
			}
			if w > 0 && r.Failure == "" && r.Worker < r.WaitEnd {
				t.Fatal(r)
			}
		}
	}
}
func TestCanceledScheduleJoinsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	rows, _, e := schedule(ctx, 60000, 2048, false)
	if e == nil || len(rows) != 0 || time.Since(start) > time.Second {
		t.Fatal(e, len(rows))
	}
}

func TestRunCancellationRetainsEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(t.TempDir(), "case")
	if e := run(ctx, []string{"--output", out, "--duration", "1s", "--workers", "2048"}, io.Discard); e == nil {
		t.Fatal("cancellation passed")
	}
	var m struct {
		Count   int
		Aborted bool
	}
	b, e := os.ReadFile(filepath.Join(out, "run.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &m); e != nil || !m.Aborted || m.Count != 0 {
		t.Fatal(e, m)
	}
	if _, e = os.Stat(filepath.Join(out, "failed.json")); e != nil {
		t.Fatal(e)
	}
}
func TestTraceModesAndNoReuse(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		out := filepath.Join(t.TempDir(), "case")
		args := []string{"--duration", "20ms", "--trace=" + enabled, "--output", out}
		if e := run(context.Background(), args, io.Discard); e != nil {
			t.Fatal(e)
		}
		_, e := os.Stat(filepath.Join(out, "timer.trace"))
		if (e == nil) != (enabled == "true") {
			t.Fatal(enabled, e)
		}
		before, e := os.ReadFile(filepath.Join(out, "samples.jsonl"))
		if e != nil {
			t.Fatal(e)
		}
		if e = run(context.Background(), args, io.Discard); e == nil {
			t.Fatal("reused directory")
		}
		after, _ := os.ReadFile(filepath.Join(out, "samples.jsonl"))
		if string(before) != string(after) {
			t.Fatal("overwrote retained evidence")
		}
	}
}
func TestFractionalSlotRejectedBeforeOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "case")
	if e := run(context.Background(), []string{"--duration", "11ms", "--output", out}, io.Discard); e == nil {
		t.Fatal("fractional slot accepted")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("created output", e)
	}
}
