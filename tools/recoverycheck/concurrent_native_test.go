//go:build darwin && cgo

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

func TestNativeConcurrentFaultsAndRecordedReplay(t *testing.T) {
	parent := scenarioParent(t)
	for _, seed := range []int64{17, 42, 701} {
		p, err := concurrentPlan(seed)
		if err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(parent, "seed-"+stringInt(seed))
		runScenario(t, p, output)
		var summary struct {
			AppliedSeq                                                          d.Int
			ConcurrentGroups, ResponseLosses, UnknownLookups, Restarts, Retries int
		}
		decodeFile(t, filepath.Join(output, "summary.json"), &summary)
		if summary.AppliedSeq != 12 || summary.ConcurrentGroups != 2 || summary.ResponseLosses != 6 || summary.UnknownLookups != 2 || summary.Restarts != 3 || summary.Retries != 8 {
			t.Fatal("missing combined coverage/dedup", summary)
		}
		original := normalized(t, filepath.Join(output, "final-actual.json"))
		if len(original.Results) != 12 || len(original.Orders) != 10 || len(original.Trades) < 1 {
			t.Fatal("wrong independent counts", original)
		}
		for _, step := range []string{"002", "005"} {
			var observations []concurrentObservation
			decodeFile(t, filepath.Join(output, "concurrent-"+step+"-observations.json"), &observations)
			if len(observations) != 8 {
				t.Fatal("missing concurrent observations")
			}
			failures := 0
			for _, o := range observations {
				if err := validateConcurrentOutcome(o); err != nil {
					t.Fatal(err)
				}
				if o.Fault != "" {
					failures++
				}
			}
			if failures != 3 {
				t.Fatal("wrong transport failure count")
			}
		}
		input := filepath.Join(output, "replay-005.json")
		before, _ := hashFile(input)
		replay := filepath.Join(parent, "replay-"+stringInt(seed))
		if err := command([]string{"--replay", input, "--output", replay}, io.Discard, io.Discard); err != nil {
			t.Fatal("recorded replay", err)
		}
		if !reflect.DeepEqual(original, normalized(t, filepath.Join(replay, "final-actual.json"))) {
			t.Fatal("recorded order changed final logical state")
		}
		for _, step := range []string{"002", "005"} {
			var a, b plan
			decodeFile(t, filepath.Join(output, "replay-"+step+".json"), &a)
			decodeFile(t, filepath.Join(replay, "replay-"+step+".json"), &b)
			index := 2
			if step == "005" {
				index = 5
			}
			if !reflect.DeepEqual(a.Actions[index].Concurrent.Recorded, b.Actions[index].Concurrent.Recorded) {
				t.Fatal("recorded snapshot/order not reproduced")
			}
		}
		after, _ := hashFile(input)
		if before != after {
			t.Fatal("original replay specification changed")
		}
		if _, err := os.Stat(filepath.Join(replay, "completed.json")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeConcurrentFailureRetainsRecordedReproduction(t *testing.T) {
	parent := scenarioParent(t)
	output := filepath.Join(parent, "expected-output-failure")
	p, _ := concurrentPlan(17)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := run(ctx, p, output, "", failedWriter{}); err == nil {
		t.Fatal("output failure hidden")
	}
	var failure struct{ Reproduce string }
	decodeFile(t, filepath.Join(output, "failed.json"), &failure)
	if !strings.Contains(failure.Reproduce, "replay-005.json") {
		t.Fatal("failure omitted observed-order replay", failure)
	}
	if _, err := os.Stat(filepath.Join(output, "completed.json")); !os.IsNotExist(err) {
		t.Fatal("failure emitted completion")
	}
	if err := command([]string{"--replay", filepath.Join(output, "replay-005.json"), "--output", filepath.Join(parent, "rerun")}, io.Discard, io.Discard); err != nil {
		t.Fatal("recorded failure input could not be replayed", err)
	}
}

func TestNativeConcurrentSnapshotBeforeRemainingInitialCommands(t *testing.T) {
	parent := scenarioParent(t)
	p, _ := concurrentPlan(17)
	for _, index := range []int{2, 5} {
		g := p.Actions[index].Concurrent
		g.Recorded = &concurrentSchedule{Order: []d.CheckpointCommand{g.Initial[0].Command, g.Initial[2].Command, g.Retries[2].Command, g.Retries[3].Command}, SnapshotAfter: 1}
	}
	if err := validatePlan(p); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "early-boundary")
	runScenario(t, p, output)
	for _, entry := range []struct {
		step string
		seq  d.Int
	}{{"002", 3}, {"005", 9}} {
		var report struct{ SnapshotSeq, ReplayedCommands d.Int }
		decodeFile(t, filepath.Join(output, "restart-"+entry.step+".json"), &report)
		if report.SnapshotSeq != entry.seq || report.ReplayedCommands != 3 {
			t.Fatal("remaining initial command crossed snapshot replay gate", report)
		}
	}
}
