package runtime

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type fakeCheckpoints struct {
	docs        map[d.Int]snapshot.Document
	loadErrors  map[d.Int]error
	listErr     error
	confirmErr  error
	confirmed   []d.Int
	loaded      []d.Int
	confirmGate chan struct{}
	confirmCall chan struct{}
}

func (f *fakeCheckpoints) List() ([]snapshot.Candidate, error) {
	result := make([]snapshot.Candidate, 0, len(f.docs))
	for seq := range f.docs {
		name, _ := snapshot.Filename(seq)
		result = append(result, snapshot.Candidate{Name: name, Seq: seq})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq > result[j].Seq })
	return result, f.listErr
}
func (f *fakeCheckpoints) Load(c snapshot.Candidate, _ snapshot.Identity, _ d.Int) (snapshot.Document, error) {
	f.loaded = append(f.loaded, c.Seq)
	return f.docs[c.Seq], f.loadErrors[c.Seq]
}
func (f *fakeCheckpoints) Confirm(doc snapshot.Document) error {
	if f.confirmCall != nil {
		close(f.confirmCall)
		<-f.confirmGate
	}
	f.confirmed = append(f.confirmed, doc.SnapshotSeq)
	return f.confirmErr
}
func (f *fakeCheckpoints) Publish(snapshot.Document) error {
	panic("unexpected publication during recovery")
}

func recoveryCommands(id string) []d.Command {
	commands := []d.Command{
		cancel("missing", "future"),
		command("bob", "ask", d.Sell, 100, 4),
		command("alice", "fill", d.Buy, 105, 2),
		command("alice", "rest", d.Buy, 90, 4),
		cancel("cancel-rest", "rest"),
		{AccountID: "bob", Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: "cancel-ask", ClientOrderID: "ask"}},
		command("alice", "rejected", d.Buy, d.MaxPriceTicks, d.MaxQuantityLots),
		cancel("cancel-rejected", "rejected"),
		command("alice", "own-ask", d.Sell, 120, 2),
		command("alice", "stp", d.Buy, 130, 3),
		command("alice", "future", d.Buy, 80, 2),
	}
	for i := range commands {
		commands[i].RunID = id
	}
	return commands
}

func recoveryFixture(t *testing.T) (*fakeJournal, *fakeCheckpoints, *engine.Engine) {
	t.Helper()
	f := fake(t)
	files := &fakeCheckpoints{docs: make(map[d.Int]snapshot.Document), loadErrors: make(map[d.Int]error)}
	e, err := engine.New(f.g.RunID, f.g.Config)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range recoveryCommands(f.g.RunID) {
		r := wal.Record{Seq: d.Int(i + 1), Command: c}
		if _, err := e.Apply(r.Seq, c); err != nil {
			t.Fatal(err)
		}
		f.records = append(f.records, r)
		if r.Seq == 3 || r.Seq == 7 || r.Seq == 11 {
			files.docs[r.Seq] = snapshot.Document{Identity: snapshotIdentity(f.g), SnapshotSeq: r.Seq, State: e.Capture()}
		}
	}
	return f, files, e
}

func TestRecoveryLatestPreviousAndGenesisAreEquivalent(t *testing.T) {
	for _, mode := range []string{"latest", "previous", "all corrupt", "no snapshots", "invalid state", "input differs from WAL"} {
		t.Run(mode, func(t *testing.T) {
			f, files, want := recoveryFixture(t)
			selected, skipped := d.Int(11), 0
			switch mode {
			case "previous":
				files.loadErrors[11] = snapshot.ErrCorrupt
				selected, skipped = 7, 1
			case "all corrupt":
				for seq := range files.docs {
					files.loadErrors[seq] = snapshot.ErrCorrupt
				}
				selected, skipped = 0, 3
			case "no snapshots":
				files.docs = nil
				selected = 0
			case "invalid state":
				doc := files.docs[11]
				doc.State.Accounts[0].AvailableBaseLots++
				selected, skipped = 7, 1
			case "input differs from WAL":
				doc := files.docs[11]
				for i := range doc.State.Results {
					r := &doc.State.Results[i]
					if r.Command.Cancel != nil && r.Command.Cancel.CancelRequestID == "missing" {
						r.Command.Cancel.ClientOrderID = "never-created"
					}
				}
				// Internal snapshot relations alone remain valid: only the retained
				// WAL proves that this initial missing-target input was different.
				if _, err := engine.Restore(f.g.RunID, f.g.Config, 11, doc.State); err != nil {
					t.Fatal(err)
				}
				selected, skipped = 7, 1
			}
			r, err := startWithSnapshots(f, files, DefaultOptions(), hooks{})
			if err != nil {
				t.Fatal(err)
			}
			report := r.Recovery()
			if report.SnapshotSeq != selected || report.ReplayedCommands != 11-selected || len(report.Skipped) != skipped {
				t.Fatalf("recovery report: %+v", report)
			}
			if selected != 0 && !reflect.DeepEqual(files.confirmed, []d.Int{selected}) {
				t.Fatalf("wrong confirmed snapshot: %v", files.confirmed)
			}
			if selected == 0 && (report.SnapshotFile != "" || len(files.confirmed) != 0) {
				t.Fatal("Genesis recovery claimed a snapshot")
			}
			if len(report.Skipped) > 0 {
				report.Skipped[0].Name = "mutated"
				if r.Recovery().Skipped[0].Name == "mutated" {
					t.Fatal("report aliases runtime state")
				}
			}
			for _, record := range f.records {
				expected, _, _ := want.Lookup(record.Command)
				actual, err := r.Submit(ctx(t), record.Command)
				if err != nil || !reflect.DeepEqual(actual, expected) {
					t.Fatalf("lost initial response: %v", err)
				}
			}
			if err := r.Shutdown(ctx(t)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.engine.State(), want.State()) || len(f.records) != 11 {
				t.Fatal("recovery/retry changed state or repeated WAL records")
			}
		})
	}
}

func TestRecoveryFatalErrorsNeverFallBack(t *testing.T) {
	for _, mode := range []string{"list", "load", "identity", "foreign doc", "future seq", "confirm", "WAL", "WAL confirmation"} {
		t.Run(mode, func(t *testing.T) {
			f, files, _ := recoveryFixture(t)
			switch mode {
			case "list":
				files.listErr = injected
			case "load":
				files.loadErrors[11] = injected
			case "identity":
				files.loadErrors[11] = snapshot.ErrIdentity
			case "foreign doc":
				doc := files.docs[11]
				doc.ConfigHash = "foreign"
				files.docs[11] = doc
			case "future seq":
				f.records = f.records[:10]
			case "confirm":
				files.confirmErr = injected
			case "WAL":
				f.replayErr = injected
			case "WAL confirmation":
				f.confirmErr = injected
			}
			r, err := startWithSnapshots(f, files, DefaultOptions(), hooks{})
			if r != nil || err == nil || f.closed.Load() != 1 || len(files.loaded) > 1 {
				t.Fatalf("fatal error admitted or fell back: runtime=%v err=%v close=%d loaded=%v", r, err, f.closed.Load(), files.loaded)
			}
		})
	}
}

func TestRecoveryWaitsForSnapshotAndWALConfirmation(t *testing.T) {
	f, files, _ := recoveryFixture(t)
	files.confirmCall, files.confirmGate = make(chan struct{}), make(chan struct{})
	f.confirmStarted, f.confirmGate = make(chan struct{}), make(chan struct{})
	type reply struct {
		r   *Runtime
		err error
	}
	done := make(chan reply, 1)
	go func() { r, err := startWithSnapshots(f, files, DefaultOptions(), hooks{}); done <- reply{r, err} }()
	receive(t, files.confirmCall)
	select {
	case <-done:
		t.Fatal("runtime published before snapshot sync")
	default:
	}
	close(files.confirmGate)
	receive(t, f.confirmStarted)
	select {
	case <-done:
		t.Fatal("runtime published before WAL sync")
	default:
	}
	close(f.confirmGate)
	got := receive(t, done)
	if got.err != nil || got.r == nil || !got.r.Ready() {
		t.Fatalf("recovery: %v", got.err)
	}
	if err := got.r.Shutdown(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestWALScanErrorWinsOverCandidateMismatch(t *testing.T) {
	f, files, _ := recoveryFixture(t)
	f.records[0].Command.Cancel.ClientOrderID = "different"
	f.replayErr = injected
	_, _, err := recoverState(f, files)
	if !errors.Is(err, injected) || len(files.loaded) != 1 {
		t.Fatalf("WAL error hidden by candidate mismatch: %v", err)
	}
}
