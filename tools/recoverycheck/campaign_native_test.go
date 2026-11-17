//go:build darwin && cgo

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

func TestNativeConcurrentCampaignAndProfileReplay(t *testing.T) {
	parent := scenarioParent(t)
	out := filepath.Join(parent, "campaign")
	o := testCampaignOptions()
	o.DiskBudget = 64 << 20
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runCampaign(ctx, o, out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Results       []campaignResult
		CompletedRuns int
	}
	decodeFile(t, filepath.Join(out, "summary.json"), &summary)
	if summary.CompletedRuns != 3 {
		t.Fatal("missing profiles")
	}
	for _, row := range summary.Results {
		original := filepath.Join(out, row.Directory)
		wantSeq, wantRetries, wantConflicts := d.Int(12), 8, 0
		if row.Profile == "duplicate-storm" {
			wantRetries = 40
		}
		if row.Profile == "id-conflict" {
			wantSeq, wantConflicts = 14, 16
		}
		var s struct {
			AppliedSeq                         d.Int
			Conflicts, Retries, ResponseLosses int
		}
		decodeFile(t, filepath.Join(original, "summary.json"), &s)
		if s.AppliedSeq != wantSeq || s.Conflicts != wantConflicts || s.Retries != wantRetries || s.ResponseLosses != 6 {
			t.Fatal("wrong independent counts", row, s)
		}
		checkpoint := normalized(t, filepath.Join(original, "final-actual.json"))
		if len(checkpoint.Results) != int(wantSeq) || len(checkpoint.Orders) != 10 || len(checkpoint.Trades) < 1 {
			t.Fatal("missing trading coverage")
		}
		entries, err := os.ReadDir(original)
		if err != nil {
			t.Fatal(err)
		}
		replay := ""
		for _, entry := range entries {
			if len(entry.Name()) > 7 && entry.Name()[:7] == "replay-" {
				replay = filepath.Join(original, entry.Name())
			}
		}
		hash, err := hashFile(replay)
		if err != nil {
			t.Fatal(err)
		}
		recovered := filepath.Join(parent, "replay-"+row.Profile)
		if err := command([]string{"--replay", replay, "--output", recovered}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(checkpoint, normalized(t, filepath.Join(recovered, "final-actual.json"))) {
			t.Fatal("recorded state changed")
		}
		after, _ := hashFile(replay)
		if after != hash {
			t.Fatal("source changed")
		}
	}
	if _, err := os.Stat(filepath.Join(out, "completed.json")); err != nil {
		t.Fatal(err)
	}
	if err := runCampaign(ctx, o, out, io.Discard); err == nil {
		t.Fatal("reused campaign")
	}
}

func TestNativeCancelRaceBothStoredOrders(t *testing.T) {
	parent := scenarioParent(t)
	for _, cancelFirst := range []bool{false, true} {
		p, _ := profilePlan(17, "cancel-race")
		for _, index := range []int{2, 5} {
			g := p.Actions[index].Concurrent
			var buy, cancel, held, tail d.CheckpointCommand
			for _, a := range g.Initial {
				if a.Fault == "drop-headers" {
					buy = a.Command
				}
				if a.Command.Cancel != nil {
					cancel = a.Command
				}
				if a.Fault == "held-original" {
					held = a.Command
				}
			}
			for _, a := range g.Retries {
				if a.Command.Place != nil && a.Command.Place.ClientOrderID != buy.Place.ClientOrderID && a.Command.Place.ClientOrderID != held.Place.ClientOrderID {
					tail = a.Command
				}
			}
			order := []d.CheckpointCommand{buy, cancel, held, tail}
			if cancelFirst {
				order[0], order[1] = order[1], order[0]
			}
			g.Recorded = &concurrentSchedule{Order: order, SnapshotAfter: 2}
		}
		label := "buy-first"
		want := "CANCELLED"
		if cancelFirst {
			label = "cancel-first"
			want = "ORDER_NOT_FOUND"
		}
		out := filepath.Join(parent, label)
		runScenario(t, p, out)
		cp := normalized(t, filepath.Join(out, "final-actual.json"))
		found := false
		for _, r := range cp.Results {
			if r.Command.Cancel != nil && r.Command.Cancel.CancelRequestID == "parallel-0-cancel" {
				found = true
				if string(r.Response.Code) != want {
					t.Fatal("wrong stored race outcome", r.Response)
				}
			}
		}
		if !found {
			t.Fatal("missing cancellation result")
		}
	}
}
