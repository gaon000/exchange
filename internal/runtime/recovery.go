package runtime

import (
	"errors"
	"fmt"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

// SkippedSnapshot reports a corrupt candidate retained on disk for inspection.
type SkippedSnapshot struct {
	Name   string
	Reason string
}

// RecoveryReport describes startup only, not later snapshot publications.
// An empty SnapshotFile means Genesis replay; a seq=0 snapshot still has a name.
type RecoveryReport struct {
	SnapshotFile     string
	SnapshotSeq      d.Int
	ReplayedCommands d.Int
	Skipped          []SkippedSnapshot
}

type checkpoints interface {
	List() ([]snapshot.Candidate, error)
	Load(snapshot.Candidate, snapshot.Identity, d.Int) (snapshot.Document, error)
	Confirm(snapshot.Document) error
	Publish(snapshot.Document) error
}

type diskCheckpoints struct{ dir string }

func (s diskCheckpoints) List() ([]snapshot.Candidate, error) { return snapshot.List(s.dir) }
func (s diskCheckpoints) Load(c snapshot.Candidate, id snapshot.Identity, seq d.Int) (snapshot.Document, error) {
	return snapshot.Load(s.dir, c, id, seq)
}
func (s diskCheckpoints) Confirm(doc snapshot.Document) error { return snapshot.Confirm(s.dir, doc) }
func (s diskCheckpoints) Publish(doc snapshot.Document) error { return snapshot.Publish(s.dir, doc) }

func snapshotIdentity(g wal.Genesis) snapshot.Identity {
	return snapshot.Identity{RunID: g.RunID, ConfigHash: g.ConfigHash, RulesVersion: g.Config.RulesVersion, ResponseVersion: g.Config.ResponseVersion}
}

func recoverState(s journal, files checkpoints) (*engine.Engine, RecoveryReport, error) {
	g := s.Genesis()
	report := RecoveryReport{Skipped: []SkippedSnapshot{}}
	if files != nil {
		candidates, err := files.List()
		if err != nil {
			return nil, report, err
		}
		for _, candidate := range candidates {
			doc, err := files.Load(candidate, snapshotIdentity(g), s.Recovery().LastSeq)
			if err != nil {
				if !errors.Is(err, snapshot.ErrCorrupt) {
					return nil, report, err
				}
				report.Skipped = append(report.Skipped, SkippedSnapshot{candidate.Name, err.Error()})
				continue
			}
			if doc.Identity != snapshotIdentity(g) || doc.SnapshotSeq > s.Recovery().LastSeq {
				return nil, report, snapshot.ErrIdentity
			}
			if doc.SnapshotSeq != candidate.Seq {
				return nil, report, snapshot.ErrInvalid
			}
			e, err := engine.Restore(g.RunID, g.Config, doc.SnapshotSeq, doc.State)
			if err == nil {
				var walErr error
				err, walErr = replayInto(s, e, doc.SnapshotSeq)
				if walErr != nil {
					return nil, report, walErr
				}
			}
			if err != nil {
				report.Skipped = append(report.Skipped, SkippedSnapshot{candidate.Name, err.Error()})
				continue
			}
			if err := files.Confirm(doc); err != nil {
				return nil, report, err
			}
			report.SnapshotFile, report.SnapshotSeq = candidate.Name, doc.SnapshotSeq
			report.ReplayedCommands = e.AppliedSeq() - doc.SnapshotSeq
			return e, report, nil
		}
	}
	e, err := engine.New(g.RunID, g.Config)
	if err != nil {
		return nil, report, err
	}
	stateErr, walErr := replayInto(s, e, 0)
	if stateErr != nil || walErr != nil {
		return nil, report, errors.Join(stateErr, walErr)
	}
	report.ReplayedCommands = e.AppliedSeq()
	return e, report, nil
}

// replayInto scans ALL retained records. Prefix records validate original input
// and sequence without re-matching; only the suffix changes the engine. Expected
// candidate mismatches must not escape the visitor and poison the WAL handle:
// an older snapshot can still be valid. A WAL scan error is always fatal.
func replayInto(s journal, e *engine.Engine, snapshotSeq d.Int) (stateErr, walErr error) {
	var visited d.Int
	walErr = s.Replay(func(record wal.Record) error {
		if stateErr != nil {
			return nil
		}
		if record.Seq <= visited || record.Seq-visited != 1 {
			stateErr = engine.ErrSequence
			return nil
		}
		visited = record.Seq
		result, found, err := e.Lookup(record.Command)
		if err != nil {
			stateErr = err
			return nil
		}
		if record.Seq <= snapshotSeq {
			if !found || result.CommandSeq != record.Seq {
				stateErr = fmt.Errorf("snapshot input/sequence differs from WAL at %s", record.Seq)
			}
			return nil
		}
		if found {
			stateErr = fmt.Errorf("repeated WAL request key")
			return nil
		}
		if _, err := e.Apply(record.Seq, record.Command); err != nil {
			stateErr = err
		} else if e.AppliedSeq() != record.Seq {
			stateErr = engine.ErrSequence
		}
		return nil
	})
	if stateErr == nil && walErr == nil {
		if visited != s.Recovery().LastSeq || e.AppliedSeq() != visited {
			stateErr = engine.ErrSequence
		} else {
			stateErr = e.CheckInvariants()
		}
	}
	return
}

// Recovery returns a detached startup report, including any skipped candidates.
func (r *Runtime) Recovery() RecoveryReport {
	copy := r.recovery
	copy.Skipped = append([]SkippedSnapshot{}, copy.Skipped...)
	return copy
}
