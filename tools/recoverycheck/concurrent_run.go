package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/strictjson"
	"github.com/gaon000/exchange/internal/wal"
)

func (r *scenarioRun) concurrent(parent context.Context, g concurrentGroup) (err error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	marker, late := gate(), gate()
	ready := make(chan error, 1)
	var waves []*concurrentWave
	old := r.s
	walSaved := false
	step := *r.step
	before := r.model.AppliedSeq()
	var savedSeq d.Int
	defer func() {
		cancel()
		marker.open()
		late.open()
		var observed []concurrentObservation
		for _, w := range waves {
			w.close()
			observed = append(observed, w.observations()...)
		}
		err = errors.Join(err, writeJSON(filepath.Join(r.output, fmt.Sprintf("concurrent-%03d-observations.json", step)), observed))
		if err != nil {
			err = errors.Join(err, old.close())
			if !walSaved {
				s, openErr := wal.Open(r.dir)
				if openErr == nil {
					var records []wal.Record
					readErr := s.Replay(func(record wal.Record) error { records = append(records, record); return nil })
					err = errors.Join(err, readErr, writeJSON(filepath.Join(r.output, fmt.Sprintf("concurrent-%03d-wal.json", step)), records), s.Close())
				} else {
					err = errors.Join(err, openErr)
				}
			}
		}
	}()
	order := newAdmissionOrder(g, r.g.RunID)
	initial, err := startConcurrentWave(ctx, old, r.g.RunID, "INITIAL", g.Initial, g.SnapshotClient, ready, marker, late, order)
	if err != nil {
		return err
	}
	waves = append(waves, initial)
	if err := initial.dispatch(ctx); err != nil {
		return err
	}
	select {
	case err = <-ready:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := order.beforeSnapshot(ctx); err != nil {
		return err
	}
	// The witness response is still blocked in the intermediary. Its caller has
	// not returned, even if other upstream commands already finished.
	for _, task := range initial.tasks {
		if task.observation.Client == g.SnapshotClient {
			select {
			case <-task.clientDone:
				return errors.New("snapshot witness was no longer in flight")
			default:
			}
		}
	}
	saved, err := old.runtime.Snapshot(ctx)
	if err != nil {
		return err
	}
	savedSeq = saved.SnapshotSeq
	if err := r.events.Encode(map[string]any{"step": step, "phase": "concurrent-snapshot", "snapshotSeq": savedSeq, "witnessClient": g.SnapshotClient, "clientStillInFlight": true}); err != nil {
		return err
	}
	order.afterSnapshot()
	marker.open()
	if err := initial.clients(ctx); err != nil {
		return err
	}
	var held d.Command
	for _, a := range g.Initial {
		if a.Fault == "held-original" {
			held, _ = a.Command.Command(r.g.RunID)
		}
	}
	kind := "place"
	if held.Kind == d.CancelOrder {
		kind = "cancel"
	}
	body, err := old.request(ctx, r.g.RunID, held.AccountID, "GET", "/v1/results/"+kind+"/"+held.Key().RequestID, nil, 404)
	if err != nil || !bytes.Equal(body, []byte(`{"state":"UNKNOWN"}`)) {
		return errors.Join(err, errors.New("held original did not produce 404 UNKNOWN witness"))
	}
	r.unknownLookups++
	retry, err := startConcurrentWave(ctx, old, r.g.RunID, "RETRY", g.Retries, -1, nil, marker, late, order)
	if err != nil {
		return err
	}
	waves = append(waves, retry)
	if err := retry.dispatch(ctx); err != nil {
		return err
	}
	if err := retry.clients(ctx); err != nil {
		return err
	}
	late.open()
	var observed []concurrentObservation
	for _, w := range waves {
		if err := w.join(ctx); err != nil {
			return err
		}
		w.close()
		observed = append(observed, w.observations()...)
	}
	// HTTP sequence numbers permit a provisional live-state check while the
	// runtime owns the journal lock. Accept it only after the actual WAL below
	// proves the identical input/sequence order; completion order is never used.
	liveOrder, liveErr := observedConcurrentOrder(r.records, observed, r.g.RunID)
	if liveErr == nil {
		liveModel, liveFirst, _, _, replayErr := r.replayConcurrent(liveOrder, g, savedSeq)
		liveErr = replayErr
		if liveErr == nil {
			liveErr = old.checkViews(ctx, r.g.RunID, liveModel, liveFirst)
		}
	}
	if err := old.close(); err != nil {
		return err
	}
	store, err := wal.Open(r.dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	var records []wal.Record
	if err := store.Replay(func(record wal.Record) error { records = append(records, record); return nil }); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(r.output, fmt.Sprintf("concurrent-%03d-wal.json", step)), records); err != nil {
		return err
	}
	walSaved = true
	model, first, prefix, newOrder, err := r.replayConcurrent(records, g, savedSeq)
	if err != nil {
		return err
	}
	recorded := &concurrentSchedule{Order: newOrder, SnapshotAfter: savedSeq - before}
	// Publish a complete valid schedule before response/checkpoint assertions, so
	// a logical mismatch is replayable with the observed WAL/snapshot boundaries.
	r.replayPlan.Actions[step].Concurrent.Recorded = recorded
	if err := validatePlan(r.replayPlan); err != nil {
		return fmt.Errorf("observed schedule invalid: %w", err)
	}
	if err := writeJSON(filepath.Join(r.output, fmt.Sprintf("replay-%03d.json", step)), r.replayPlan); err != nil {
		return err
	}
	if g.Recorded != nil {
		if err := same(*g.Recorded, *recorded, "recorded WAL order/snapshot boundary"); err != nil {
			return err
		}
	}
	if liveErr != nil {
		return fmt.Errorf("pre-restart live-state check: %w", liveErr)
	}
	if err := same(liveOrder, records, "actual WAL vs live-check sequence order"); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(r.output, fmt.Sprintf("concurrent-%03d-live-order.json", step)), liveOrder); err != nil {
		return err
	}
	conflictAttempts := 0
	for _, o := range observed {
		if err := validateConcurrentOutcome(o); err != nil {
			return err
		}
		if o.ExpectConflict {
			r.conflicts++
			conflictAttempts++
			continue
		}
		c, _ := o.Command.Command(r.g.RunID)
		if !bytes.Equal([]byte(o.UpstreamResponse), first[c.Key()]) {
			return fmt.Errorf("client %d initial response differs from actual WAL replay", o.Client)
		}
		if o.Fault == "" {
			h := sha256.Sum256(first[c.Key()])
			if o.Outcome.Fingerprint != hex.EncodeToString(h[:]) {
				return errors.New("complete client response fingerprint changed")
			}
		}
	}
	name, err := snapshot.Filename(savedSeq)
	if err != nil {
		return err
	}
	doc, err := snapshot.Load(r.dir, snapshot.Candidate{Name: name, Seq: savedSeq}, snapshot.Identity{RunID: r.g.RunID, ConfigHash: r.g.ConfigHash, RulesVersion: r.g.Config.RulesVersion, ResponseVersion: r.g.Config.ResponseVersion}, model.AppliedSeq())
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(r.output, fmt.Sprintf("concurrent-%03d-snapshot-expected.json", step)), prefix); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(r.output, fmt.Sprintf("concurrent-%03d-snapshot-actual.json", step)), doc.State); err != nil {
		return err
	}
	if err := same(prefix, doc.State, "in-flight snapshot vs stored prefix"); err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	r.model, r.records, r.first = model, records, first
	r.lastSnap, r.haveSnap = savedSeq, true
	r.snapshots++
	r.retries += len(observed) - len(newOrder) - conflictAttempts
	for _, o := range observed {
		if o.Fault != "" {
			r.responseLosses++
		}
	}
	if err := r.restart(parent); err != nil {
		return err
	}
	r.concurrentGroups++
	return r.events.Encode(map[string]any{"step": step, "phase": "concurrent-verified", "snapshotSeq": savedSeq, "storedCommands": len(records), "tailCommands": model.AppliedSeq() - savedSeq, "reproduce": reproduction(r.output)})
}

func observedConcurrentOrder(prior []wal.Record, observations []concurrentObservation, run string) ([]wal.Record, error) {
	byKey := make(map[d.Key]wal.Record)
	for _, record := range prior {
		byKey[record.Command.Key()] = record
	}
	for _, o := range observations {
		if err := validateConcurrentOutcome(o); err != nil {
			return nil, err
		}
		if o.ExpectConflict {
			continue
		}
		c, err := o.Command.Command(run)
		if err != nil {
			return nil, err
		}
		var result d.Result
		if err := strictjson.Decode([]byte(o.UpstreamResponse), &result); err != nil {
			return nil, err
		}
		if result.RunID != run || result.AccountID != c.AccountID || result.Kind != c.Kind || result.RequestID != c.Key().RequestID || result.CommandSeq < 1 {
			return nil, errors.New("upstream result identity/sequence mismatch")
		}
		record := wal.Record{Seq: result.CommandSeq, Command: c}
		if previous, ok := byKey[c.Key()]; ok && previous != record {
			return nil, errors.New("copies returned different first sequences")
		}
		byKey[c.Key()] = record
	}
	rows := make([]wal.Record, 0, len(byKey))
	for _, record := range byKey {
		rows = append(rows, record)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	return rows, nil
}

// replayConcurrent uses the journal, never network completion or submission
// order, as the oracle's order. Every planned key must occur exactly once.
func (r *scenarioRun) replayConcurrent(records []wal.Record, g concurrentGroup, snapshotSeq d.Int) (*engine.Engine, map[d.Key][]byte, d.Checkpoint, []d.CheckpointCommand, error) {
	expected := make(map[d.Key]d.Command)
	for _, record := range r.records {
		expected[record.Command.Key()] = record.Command
	}
	for _, list := range [][]concurrentAttempt{g.Initial, g.Retries} {
		for _, a := range list {
			if a.ExpectConflict {
				continue
			}
			c, err := a.Command.Command(r.g.RunID)
			if err != nil {
				return nil, nil, d.Checkpoint{}, nil, err
			}
			if prior, ok := expected[c.Key()]; ok && prior != c {
				return nil, nil, d.Checkpoint{}, nil, errors.New("planned key payload conflict")
			}
			expected[c.Key()] = c
		}
	}
	if len(records) != len(expected) || len(records) < len(r.records) {
		return nil, nil, d.Checkpoint{}, nil, errors.New("missing/duplicate persisted logical commands")
	}
	for i, prior := range r.records {
		if records[i] != prior {
			return nil, nil, d.Checkpoint{}, nil, errors.New("previous WAL prefix changed")
		}
	}
	model, err := engine.New(r.g.RunID, r.g.Config)
	if err != nil {
		return nil, nil, d.Checkpoint{}, nil, err
	}
	first := make(map[d.Key][]byte)
	seen := make(map[d.Key]bool)
	var prefix d.Checkpoint
	var order []d.CheckpointCommand
	for i, record := range records {
		c := record.Command
		if record.Seq != d.Int(i+1) || seen[c.Key()] || expected[c.Key()] != c {
			return nil, nil, d.Checkpoint{}, nil, errors.New("unexpected/duplicate WAL key, input or sequence")
		}
		seen[c.Key()] = true
		result, err := model.Apply(record.Seq, c)
		if err != nil {
			return nil, nil, d.Checkpoint{}, nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, nil, d.Checkpoint{}, nil, err
		}
		if prior, ok := r.first[c.Key()]; ok && !bytes.Equal(prior, encoded) {
			return nil, nil, d.Checkpoint{}, nil, errors.New("previous first response changed")
		}
		first[c.Key()] = encoded
		if record.Seq == snapshotSeq {
			prefix = model.Capture()
		}
		if i >= len(r.records) {
			input := d.CheckpointCommand{AccountID: c.AccountID, Kind: c.Kind}
			if c.Kind == d.PlaceOrder {
				p := c.Place
				input.Place = &p
			} else {
				cancel := c.Cancel
				input.Cancel = &cancel
			}
			order = append(order, input)
		}
	}
	if snapshotSeq <= r.model.AppliedSeq() || snapshotSeq >= model.AppliedSeq() {
		return nil, nil, d.Checkpoint{}, nil, errors.New("concurrent snapshot must contain new commands and have a WAL tail")
	}
	if err := model.CheckInvariants(); err != nil {
		return nil, nil, d.Checkpoint{}, nil, err
	}
	return model, first, prefix, order, nil
}
