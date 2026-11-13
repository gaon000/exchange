package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type scenarioRun struct {
	dir, output string
	g           wal.Genesis
	model       *engine.Engine
	s           *session
	first       map[d.Key][]byte
	records     []wal.Record
	events      *json.Encoder
	trace       *json.Encoder
	step        *int
	lastSnap    d.Int
	haveSnap    bool
	restarts    int
	snapshots   int
	retries     int
	conflicts   int
}

func execute(ctx context.Context, p plan, output string, step *int, reproduce string) (err error) {
	events, err := os.OpenFile(filepath.Join(output, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, events.Sync(), events.Close()) }()
	trace, err := os.OpenFile(filepath.Join(output, "http.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, trace.Sync(), trace.Close()) }()
	r := &scenarioRun{dir: filepath.Join(output, "run"), output: output, step: step, first: make(map[d.Key][]byte),
		events: json.NewEncoder(events), trace: json.NewEncoder(trace)}
	defer func() { err = errors.Join(err, r.s.close()) }()
	store, err := wal.Init(r.dir, config())
	if err != nil {
		return err
	}
	r.g = store.Genesis()
	if err := store.Close(); err != nil {
		return err
	}
	r.model, err = engine.New(r.g.RunID, r.g.Config)
	if err != nil {
		return err
	}
	r.s, err = openSession(r.dir, r.trace, step)
	if err != nil {
		return err
	}
	for i, a := range p.Actions {
		*step = i
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.events.Encode(map[string]any{"step": i, "phase": "start", "action": a}); err != nil {
			return err
		}
		switch a.Kind {
		case "command":
			err = r.submit(ctx, *a.Command)
		case "snapshot":
			err = r.capture(ctx, r.s, r.dir, fmt.Sprintf("snapshot-%03d", i))
			if err == nil {
				r.lastSnap = r.model.AppliedSeq()
				r.haveSnap = true
				r.snapshots++
			}
		case "restart":
			err = r.restart(ctx)
		}
		if err != nil {
			return fmt.Errorf("step %d (%s): %w", i, a.Kind, err)
		}
		if err := r.model.CheckInvariants(); err != nil {
			return err
		}
		if err := r.events.Encode(map[string]any{"step": i, "phase": "passed", "appliedSeq": r.model.AppliedSeq()}); err != nil {
			return err
		}
		if err := errors.Join(events.Sync(), trace.Sync()); err != nil {
			return err
		}
	}
	// Even a manually edited replay ending in a command gets a complete final
	// checkpoint comparison and actual persisted-order audit.
	if err := r.capture(ctx, r.s, r.dir, "final"); err != nil {
		return err
	}
	r.snapshots++
	if err := r.s.close(); err != nil {
		return err
	}
	if err := r.auditWAL(); err != nil {
		return err
	}
	if err := errors.Join(events.Sync(), trace.Sync()); err != nil {
		return err
	}
	scenarioHash, err := hashFile(filepath.Join(output, "scenario.json"))
	if err != nil {
		return err
	}
	result := map[string]any{"status": "PASSED", "seed": p.Seed, "actions": len(p.Actions), "appliedSeq": r.model.AppliedSeq(),
		"snapshots": r.snapshots, "restarts": r.restarts, "genesisReplayChecks": r.restarts,
		"walOnlySnapshots": r.restarts,
		"retries":          r.retries, "conflicts": r.conflicts, "trades": len(r.model.Capture().Trades),
		"scenarioSHA256": scenarioHash, "reproduce": reproduce, "powerLossTested": false, "processKillTested": false}
	return writeJSON(filepath.Join(output, "summary.json"), result)
}

func (r *scenarioRun) submit(ctx context.Context, input d.CheckpointCommand) error {
	c, err := input.Command(r.g.RunID)
	if err != nil {
		return err
	}
	prior, found, lookupErr := r.model.Lookup(c)
	path, payload := commandPayload(c)
	if errors.Is(lookupErr, engine.ErrConflict) {
		body, err := r.s.request(ctx, r.g.RunID, c.AccountID, "POST", path, payload, 409)
		if err != nil {
			return err
		}
		var got api.ErrorResponse
		if err := json.Unmarshal(body, &got); err != nil || got.Error.Code != "IDEMPOTENCY_CONFLICT" || got.Error.Outcome != rt.NotAdmitted {
			return fmt.Errorf("conflict response mismatch: %s", body)
		}
		r.conflicts++
		return nil
	}
	if lookupErr != nil {
		return lookupErr
	}
	want := prior
	if !found {
		want, err = r.model.Apply(r.model.AppliedSeq()+1, c)
		if err != nil {
			return err
		}
	}
	body, err := r.s.request(ctx, r.g.RunID, c.AccountID, "POST", path, payload, 200)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(want)
	if err != nil || !bytes.Equal(encoded, body) {
		return fmt.Errorf("HTTP/model result mismatch: want %s; got %s (%v)", encoded, body, err)
	}
	if found {
		r.retries++
		if !bytes.Equal(r.first[c.Key()], body) {
			return errors.New("duplicate first response changed")
		}
	} else {
		r.first[c.Key()] = append([]byte(nil), body...)
		r.records = append(r.records, wal.Record{Seq: want.CommandSeq, Command: c})
	}
	return nil
}

func (r *scenarioRun) capture(ctx context.Context, s *session, dir, label string) error {
	before, err := hashFile(filepath.Join(dir, "commands.wal"))
	if err != nil {
		return err
	}
	if err := s.checkViews(ctx, r.g.RunID, r.model, r.first); err != nil {
		return err
	}
	saved, err := s.runtime.Snapshot(ctx)
	if err != nil {
		return err
	}
	if saved.SnapshotSeq != r.model.AppliedSeq() {
		return fmt.Errorf("snapshot sequence mismatch: %+v", saved)
	}
	candidates, err := snapshot.List(dir)
	if err != nil || len(candidates) == 0 {
		return fmt.Errorf("snapshot candidates unavailable: %v", err)
	}
	id := snapshot.Identity{RunID: r.g.RunID, ConfigHash: r.g.ConfigHash, RulesVersion: r.g.Config.RulesVersion, ResponseVersion: r.g.Config.ResponseVersion}
	doc, err := snapshot.Load(dir, candidates[0], id, r.model.AppliedSeq())
	if err != nil {
		return err
	}
	want := r.model.Capture()
	if err := writeJSON(filepath.Join(r.output, label+"-expected.json"), want); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(r.output, label+"-actual.json"), doc.State); err != nil {
		return err
	}
	if err := same(want, doc.State, "complete checkpoint (accounts/orders/FIFO/trades/first results)"); err != nil {
		return err
	}
	after, err := hashFile(filepath.Join(dir, "commands.wal"))
	if err != nil {
		return err
	}
	return same(before, after, "queries/retries/snapshot must not append WAL")
}

func (r *scenarioRun) auditWAL() (err error) {
	s, err := wal.Open(r.dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	sequential, err := engine.New(r.g.RunID, r.g.Config)
	if err != nil {
		return err
	}
	var records []wal.Record
	if err := s.Replay(func(record wal.Record) error {
		records = append(records, record)
		_, err := sequential.Apply(record.Seq, record.Command)
		return err
	}); err != nil {
		return err
	}
	if err := same(r.records, records, "actual stored command order"); err != nil {
		return err
	}
	return same(r.model.Capture(), sequential.Capture(), "actual WAL sequential replay")
}

func (r *scenarioRun) restart(ctx context.Context) error {
	before, err := hashFile(filepath.Join(r.dir, "commands.wal"))
	if err != nil {
		return err
	}
	if err := r.s.close(); err != nil {
		return err
	}
	if err := r.auditWAL(); err != nil {
		return err
	}
	r.s, err = openSession(r.dir, r.trace, r.step)
	if err != nil {
		return err
	}
	report := r.s.runtime.Recovery()
	if report.SnapshotSeq != r.lastSnap || report.ReplayedCommands != r.model.AppliedSeq()-r.lastSnap || len(report.Skipped) != 0 || (report.SnapshotFile != "") != r.haveSnap {
		return fmt.Errorf("unexpected snapshot/tail recovery: %+v", report)
	}
	if err := writeJSON(filepath.Join(r.output, fmt.Sprintf("restart-%03d.json", *r.step)), report); err != nil {
		return err
	}
	// Capture only after checking the selected snapshot and WAL tail. This
	// explicit verification publication becomes the next restart's base.
	if err := r.capture(ctx, r.s, r.dir, fmt.Sprintf("restart-%03d", *r.step)); err != nil {
		return err
	}
	r.lastSnap = r.model.AppliedSeq()
	r.haveSnap = true
	r.snapshots++
	shadow := filepath.Join(r.output, fmt.Sprintf("wal-only-%03d", *r.step))
	if err := os.Mkdir(shadow, 0700); err != nil {
		return err
	}
	for _, name := range []string{"genesis.bin", "commands.wal", ".lock"} {
		data, err := readRegular(filepath.Join(r.dir, name), 8<<20)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(shadow, name), data, 0600); err != nil {
			return err
		}
	}
	s, err := openSession(shadow, r.trace, r.step)
	if err != nil {
		return err
	}
	defer s.close()
	full := s.runtime.Recovery()
	if full.SnapshotFile != "" || full.SnapshotSeq != 0 || full.ReplayedCommands != r.model.AppliedSeq() {
		return fmt.Errorf("WAL-only replay did not start from Genesis: %+v", full)
	}
	if err := r.capture(ctx, s, shadow, fmt.Sprintf("wal-only-%03d", *r.step)); err != nil {
		return err
	}
	if err := s.close(); err != nil {
		return err
	}
	after, err := hashFile(filepath.Join(r.dir, "commands.wal"))
	if err != nil {
		return err
	}
	if err := same(before, after, "restart/first response retries changed WAL"); err != nil {
		return err
	}
	r.restarts++
	cp, _ := json.Marshal(r.model.Capture())
	h := sha256.Sum256(cp)
	return r.events.Encode(map[string]any{"step": *r.step, "phase": "recovery-verified", "snapshotSeq": report.SnapshotSeq,
		"tailCommands": report.ReplayedCommands, "genesisCommands": full.ReplayedCommands, "stateSHA256": hex.EncodeToString(h[:])})
}
