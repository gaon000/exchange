package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/wal"
)

func copyPlan(t *testing.T, p plan) plan {
	t.Helper()
	b, _ := json.Marshal(p)
	var q plan
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	return q
}
func TestConcurrentPlanSeedsBoundsAndRecordedSchedules(t *testing.T) {
	p, err := concurrentPlan(17)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := concurrentPlan(17)
	other, _ := concurrentPlan(42)
	if !reflect.DeepEqual(p, again) || reflect.DeepEqual(p, other) {
		t.Fatal("non-deterministic/unseeded concurrent plan")
	}
	for name, edit := range map[string]func(*plan){
		"v1":            func(p *plan) { p.Version = 1 },
		"clients":       func(p *plan) { p.Actions[2].Concurrent.Initial[1].Client = 0 },
		"unknown-fault": func(p *plan) { p.Actions[2].Concurrent.Initial[0].Fault = "unknown" },
		"retry-fault":   func(p *plan) { p.Actions[2].Concurrent.Retries[0].Fault = "drop-headers" },
		"held-marker":   func(p *plan) { p.Actions[2].Concurrent.SnapshotClient = 3 },
		"held-no-retry": func(p *plan) {
			p.Actions[2].Concurrent.Retries[2] = p.Actions[2].Concurrent.Retries[3]
			p.Actions[2].Concurrent.Retries[2].Client = 2
		},
		"different-payload": func(p *plan) { p.Actions[2].Concurrent.Initial[1].Command.Place.QuantityLots++ },
		"unrecorded-order":  func(p *plan) { p.Actions[2].Concurrent.Recorded = &concurrentSchedule{SnapshotAfter: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			q := copyPlan(t, p)
			edit(&q)
			if validatePlan(q) == nil {
				t.Fatal("invalid concurrent replay accepted")
			}
		})
	}
	for _, seed := range []int64{-1, 1 << 31} {
		if _, err := concurrentPlan(seed); err == nil {
			t.Fatal("invalid seed accepted")
		}
	}
	for _, args := range [][]string{{"--concurrent", "--actions", "32"}, {"--concurrent", "--transport-loss"}, {"--concurrent", "--replay", "missing"}} {
		if command(args, io.Discard, io.Discard) == nil {
			t.Fatal("mixed modes accepted", args)
		}
	}
}

func TestRecordedAdmissionOrderAndSnapshotGate(t *testing.T) {
	p, _ := concurrentPlan(17)
	g := *p.Actions[2].Concurrent
	g.Recorded = &concurrentSchedule{Order: []d.CheckpointCommand{g.Initial[0].Command, g.Initial[2].Command, g.Retries[2].Command, g.Retries[3].Command}, SnapshotAfter: 1}
	o := newAdmissionOrder(g, scenarioRunID)
	a, _ := g.Initial[0].Command.Command(scenarioRunID)
	b, _ := g.Initial[2].Command.Command(scenarioRunID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.enter(ctx, a.Key()); err != nil {
		t.Fatal(err)
	}
	if err := o.enter(ctx, a.Key()); err != nil {
		t.Fatal("same-key concurrent duplicate blocked", err)
	}
	o.complete(a.Key())
	if err := o.beforeSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	blocked, cancelBlocked := context.WithCancel(ctx)
	cancelBlocked()
	if err := o.enter(blocked, b.Key()); err == nil {
		t.Fatal("next key crossed snapshot gate")
	}
	o.afterSnapshot()
	if err := o.enter(ctx, b.Key()); err != nil {
		t.Fatal(err)
	}
	o.complete(a.Key()) // a late copy must not advance b's slot
	if o.next != 1 {
		t.Fatal("duplicate advanced recorded order")
	}
	o.complete(b.Key())
}

func TestConcurrentOracleUsesActualOrderAndRejectsDuplicateJournal(t *testing.T) {
	p, _ := concurrentPlan(17)
	g := *p.Actions[2].Concurrent
	model, _ := engine.New(scenarioRunID, config())
	r := &scenarioRun{g: wal.Genesis{RunID: scenarioRunID, Config: config()}, model: model, first: map[d.Key][]byte{}}
	a, _ := g.Initial[0].Command.Command(scenarioRunID)
	b, _ := g.Initial[2].Command.Command(scenarioRunID)
	held, _ := g.Retries[2].Command.Command(scenarioRunID)
	tail, _ := g.Retries[3].Command.Command(scenarioRunID)
	// Reverse the arrival/list order. The cancellation precedes its missing
	// target, and these are valid stored outcomes rather than re-sorted inputs.
	records := []wal.Record{{Seq: 1, Command: b}, {Seq: 2, Command: a}, {Seq: 3, Command: tail}, {Seq: 4, Command: held}}
	got, first, _, order, err := r.replayConcurrent(records, g, 2)
	if err != nil || got.AppliedSeq() != 4 || len(first) != 4 || order[0].Kind != d.CancelOrder {
		t.Fatal("actual WAL order not respected", err)
	}
	duplicate := append([]wal.Record(nil), records...)
	duplicate[3].Command = a
	if _, _, _, _, err := r.replayConcurrent(duplicate, g, 2); err == nil {
		t.Fatal("duplicate WAL key accepted")
	}
	if _, _, _, _, err := r.replayConcurrent(records[:3], g, 2); err == nil {
		t.Fatal("missing WAL key accepted")
	}
}

func TestConcurrentCancellationJoinsAllIntermediaries(t *testing.T) {
	p, _ := concurrentPlan(17)
	g := *p.Actions[2].Concurrent
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	marker, late := gate(), gate()
	s := &session{base: "http://127.0.0.1:1", client: &http.Client{Timeout: time.Second}}
	wave, err := startConcurrentWave(ctx, s, scenarioRunID, "INITIAL", g.Initial, g.SnapshotClient, make(chan error, 1), marker, late, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); marker.open(); late.open(); wave.close() }()
	for range wave.tasks {
		select {
		case <-wave.entered:
		case <-ctx.Done():
			t.Fatal("clients did not reach gate")
		}
	}
	cancel()
	marker.open()
	late.open()
	wave.close()
	for _, task := range wave.tasks {
		select {
		case <-task.clientDone:
		default:
			t.Fatal("client outlived cancellation")
		}
		select {
		case <-task.upstreamDone:
		default:
			t.Fatal("handler outlived cancellation")
		}
		if task.observation.UpstreamStatus != 0 {
			t.Fatal("canceled gate forwarded request")
		}
	}
}

func TestReproductionIgnoresPartialRecordedPlan(t *testing.T) {
	root := t.TempDir()
	p, _ := concurrentPlan(17)
	if err := writeJSON(filepath.Join(root, "replay-002.json"), p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "replay-005.json"), []byte(`{"version":2`), 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reproduction(root), "replay-002.json") {
		t.Fatal("partial plan selected for reproduction")
	}
}
