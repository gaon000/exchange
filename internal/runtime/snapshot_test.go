package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	stdruntime "runtime"
	"sync"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

var panicPublisher = errors.New("panic in publisher")

type controlledSnapshots struct {
	*fakeCheckpoints
	started    chan snapshot.Document
	gate       chan error
	published  []snapshot.Document // inspect only after response synchronization or Shutdown
	beforeSort func()
}

func (f *controlledSnapshots) beforeSnapshotSort() {
	if f.beforeSort != nil {
		f.beforeSort()
	}
}

func (f *controlledSnapshots) Publish(doc snapshot.Document) error {
	before, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	f.started <- doc
	if f.gate != nil {
		err = <-f.gate
	}
	if err == panicPublisher {
		panic("publisher failed")
	}
	after, encodeErr := json.Marshal(doc)
	if encodeErr != nil || !bytes.Equal(before, after) {
		return fmt.Errorf("checkpoint changed while publisher was waiting")
	}
	if err == nil {
		f.published = append(f.published, doc)
	}
	return err
}

func startSnapshotTest(t *testing.T, f *fakeJournal, o Options, h hooks, blocked bool) (*Runtime, *controlledSnapshots) {
	t.Helper()
	files := &controlledSnapshots{fakeCheckpoints: &fakeCheckpoints{}, started: make(chan snapshot.Document, 128)}
	if blocked {
		files.gate = make(chan error)
	}
	r, err := startWithSnapshots(f, files, o, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if files.gate != nil {
			close(files.gate)
		}
		if f.gate != nil {
			close(f.gate)
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(c)
	})
	return r, files
}

func snapshotAsync(r *Runtime, c context.Context) <-chan snapshotReply {
	result := make(chan snapshotReply, 1)
	go func() { value, err := r.Snapshot(c); result <- snapshotReply{value, err} }()
	return result
}

func waitSnapshotAdmission(t *testing.T, r *Runtime, busy bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.admission.Lock()
		got := r.snapshotBusy
		r.admission.Unlock()
		if got == busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot admission deadline")
		}
		stdruntime.Gosched()
	}
}

func TestSnapshotCopyDetachedWhileTradingContinues(t *testing.T) {
	f := fake(t)
	o := DefaultOptions()
	o.BatchCommands = 1
	r, files := startSnapshotTest(t, f, o, hooks{}, true)
	first, err := r.Submit(ctx(t), buy("rest"))
	if err != nil {
		t.Fatal(err)
	}
	pending := snapshotAsync(r, ctx(t))
	doc := receive(t, files.started)
	if doc.SnapshotSeq != 1 || r.SnapshotSeq() != 0 {
		t.Fatal("publication advanced before sync")
	}
	select {
	case <-pending:
		t.Fatal("snapshot returned before sync")
	default:
	}
	if _, err := r.Snapshot(ctx(t)); err == nil {
		t.Fatal("second snapshot accepted while first copy exists")
	} else {
		checkFailure(t, err, "SNAPSHOT_BUSY", NotAdmitted)
	}
	if _, err := r.Submit(ctx(t), command("bob", "fill", d.Sell, 100, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Submit(ctx(t), cancel("already-filled", "rest")); err != nil {
		t.Fatal(err)
	}
	v := read(t, r, Query{Kind: OrderQuery, ID: "rest"})
	if v.AsOfSeq != 3 || v.Order.Status != d.Filled || doc.State.Orders[0].Order.Status != d.Open {
		t.Fatal("live order and captured order mixed")
	}
	initial := read(t, r, Query{Kind: ResultQuery, ID: "rest", RequestKind: d.PlaceOrder})
	if !reflect.DeepEqual(*initial.Result, first) || r.SnapshotSeq() != 0 {
		t.Fatal("first response or publication sequence changed")
	}
	files.gate <- nil
	got := receive(t, pending)
	if got.err != nil || got.result != (SnapshotResult{runID, 1}) || r.SnapshotSeq() != 1 {
		t.Fatalf("published result: %+v / %v", got, r.SnapshotSeq())
	}
	old, err := engine.Restore(runID, f.g.Config, 1, doc.State)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := old.Lookup(buy("rest"))
	if err != nil || !found || !reflect.DeepEqual(result, first) {
		t.Fatal("captured original result changed")
	}
	pending = snapshotAsync(r, ctx(t))
	if next := receive(t, files.started); next.SnapshotSeq != 3 {
		t.Fatal("new snapshot did not capture latest applied state")
	}
	files.gate <- nil
	if got := receive(t, pending); got.err != nil || got.result.SnapshotSeq != 3 || r.SnapshotSeq() != 3 {
		t.Fatal(got)
	}
	finish(t, r)
}

func TestSnapshotSortingRunsOutsideOwnerAndShutdownJoinsIt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	f := fake(t)
	o := DefaultOptions()
	o.BatchCommands = 1
	r, files := startSnapshotTest(t, f, o, hooks{}, false)
	files.beforeSort = func() {
		close(entered)
		<-release
	}
	t.Cleanup(unblock)
	first, err := r.Submit(ctx(t), buy("rest"))
	if err != nil {
		t.Fatal(err)
	}
	pending := snapshotAsync(r, ctx(t))
	receive(t, entered)
	// The worker is paused before sorting the detached copy. Live commands and
	// queries must complete without releasing it or changing that old boundary.
	if _, err := r.Submit(ctx(t), command("bob", "fill", d.Sell, 100, 2)); err != nil {
		t.Fatal(err)
	}
	v := read(t, r, Query{Kind: OrderQuery, ID: "rest"})
	if v.AsOfSeq != 2 || v.Order.Status != d.Filled || r.SnapshotSeq() != 0 {
		t.Fatal("sorting blocked progress or acknowledged an unpublished snapshot")
	}
	if _, err := r.Snapshot(ctx(t)); err == nil {
		t.Fatal("sorting allowed another checkpoint copy")
	} else {
		checkFailure(t, err, "SNAPSHOT_BUSY", NotAdmitted)
	}
	stopped, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	if err := r.Shutdown(stopped); !errors.Is(err, context.Canceled) || r.Ready() || f.closed.Load() != 0 {
		t.Fatal("shutdown released storage before the sorting worker finished")
	}
	unblock()
	doc := receive(t, files.started)
	result := receive(t, pending)
	if result.err != nil || result.result.SnapshotSeq != 1 || doc.SnapshotSeq != 1 {
		t.Fatalf("snapshot boundary: %+v / %d", result, doc.SnapshotSeq)
	}
	finish(t, r)
	restored, err := engine.Restore(runID, f.g.Config, 1, doc.State)
	if err != nil {
		t.Fatal(err)
	}
	initial, found, err := restored.Lookup(buy("rest"))
	if err != nil || !found || !reflect.DeepEqual(initial, first) || f.closed.Load() != 1 {
		t.Fatal("deferred sorting changed the original result or leaked the store")
	}
}

func TestSnapshotSortingPanicFailsWithoutPublication(t *testing.T) {
	f := fake(t)
	r, files := startSnapshotTest(t, f, DefaultOptions(), hooks{}, false)
	files.beforeSort = func() {
		panic("sorting failure")
	}
	if _, err := r.Submit(ctx(t), buy("initial")); err != nil {
		t.Fatal(err)
	}
	_, err := r.Snapshot(ctx(t))
	checkFailure(t, err, "INTERNAL_FAILURE", Unknown)
	if r.Shutdown(ctx(t)) == nil || r.Ready() || r.SnapshotSeq() != 0 || f.closed.Load() != 1 || len(files.published) != 0 {
		t.Fatal("sorting failure was accepted or leaked the storage lifetime")
	}
}

func TestSnapshotDuringWALSyncCapturesOnlyAppliedState(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error)
	o := DefaultOptions()
	o.BatchCommands = 1
	r, files := startSnapshotTest(t, f, o, hooks{}, true)
	order := submitAsync(r, ctx(t), buy("pending"))
	receive(t, f.calls)
	request := snapshotAsync(r, ctx(t))
	doc := receive(t, files.started)
	if doc.SnapshotSeq != 0 || len(doc.State.Orders) != 0 || len(doc.State.Results) != 0 {
		t.Fatal("unapplied command entered snapshot")
	}
	files.gate <- nil
	if got := receive(t, request); got.err != nil || got.result.SnapshotSeq != 0 {
		t.Fatal(got)
	}
	if v := read(t, r, Query{Kind: BalancesQuery}); v.AsOfSeq != 0 || v.Balances.Quote.Reserved != 0 {
		t.Fatal(v)
	}
	f.gate <- nil
	if got := receive(t, order); got.err != nil || got.result.CommandSeq != 1 {
		t.Fatal(got)
	}
	finish(t, r)
}

func TestSnapshotTimeoutRetainsSinglePublicationSlot(t *testing.T) {
	f := fake(t)
	r, files := startSnapshotTest(t, f, DefaultOptions(), hooks{}, true)
	c, cancelWait := context.WithCancel(context.Background())
	pending := snapshotAsync(r, c)
	receive(t, files.started)
	cancelWait()
	checkFailure(t, receive(t, pending).err, "SNAPSHOT_TIMEOUT", Unknown)
	if len(r.slots) != 0 {
		t.Fatal("cancelled waiter retained global request slot")
	}
	if _, err := r.Snapshot(ctx(t)); err == nil {
		t.Fatal("timeout allowed another full state copy")
	} else {
		checkFailure(t, err, "SNAPSHOT_BUSY", NotAdmitted)
	}
	if _, err := r.Submit(ctx(t), buy("later")); err != nil {
		t.Fatal(err)
	}
	files.gate <- nil
	waitSnapshotAdmission(t, r, false)
	pending = snapshotAsync(r, ctx(t))
	if doc := receive(t, files.started); doc.SnapshotSeq != 1 {
		t.Fatal("later snapshot did not capture progress")
	}
	files.gate <- nil
	if got := receive(t, pending); got.err != nil || got.result.SnapshotSeq != 1 {
		t.Fatal(got)
	}
	if _, err := r.Snapshot(c); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancelled snapshot admitted")
	}
	finish(t, r)
	if _, err := r.Snapshot(ctx(t)); err == nil {
		t.Fatal("snapshot admitted after shutdown")
	} else {
		checkFailure(t, err, "NOT_READY", NotAdmitted)
	}
}

func TestShutdownWaitsForBothWALAndSnapshot(t *testing.T) {
	for _, first := range []string{"WAL", "snapshot"} {
		t.Run(first, func(t *testing.T) {
			f := fake(t)
			f.gate = make(chan error)
			o := DefaultOptions()
			o.BatchCommands = 1
			r, files := startSnapshotTest(t, f, o, hooks{}, true)
			snap := snapshotAsync(r, ctx(t))
			receive(t, files.started)
			order := submitAsync(r, ctx(t), buy("drained"))
			receive(t, f.calls)
			cancelled, cancelWait := context.WithCancel(context.Background())
			cancelWait()
			if err := r.Shutdown(cancelled); !errors.Is(err, context.Canceled) || r.Ready() || f.closed.Load() != 0 {
				t.Fatal("shutdown cancelled work or released lock early")
			}
			if first == "WAL" {
				f.gate <- nil
				if got := receive(t, order); got.err != nil {
					t.Fatal(got.err)
				}
			} else {
				files.gate <- nil
				if got := receive(t, snap); got.err != nil {
					t.Fatal(got.err)
				}
			}
			if f.closed.Load() != 0 {
				t.Fatal("store closed while another writer was active")
			}
			if first == "WAL" {
				files.gate <- nil
				if got := receive(t, snap); got.err != nil {
					t.Fatal(got.err)
				}
			} else {
				f.gate <- nil
				if got := receive(t, order); got.err != nil {
					t.Fatal(got.err)
				}
			}
			finish(t, r)
			if f.closed.Load() != 1 || r.engine.AppliedSeq() != 1 || len(files.published) != 1 {
				t.Fatal("shutdown did not drain accepted work")
			}
		})
	}
}

func TestSnapshotFailureStopsOwnerButJoinsWAL(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error)
	o := DefaultOptions()
	o.BatchCommands = 1
	r, files := startSnapshotTest(t, f, o, hooks{}, true)
	snap := snapshotAsync(r, ctx(t))
	receive(t, files.started)
	order := submitAsync(r, ctx(t), buy("unconfirmed"))
	receive(t, f.calls)
	files.gate <- injected
	checkFailure(t, receive(t, snap).err, "STORAGE_UNAVAILABLE", Unknown)
	checkFailure(t, receive(t, order).err, "STORAGE_UNAVAILABLE", Unknown)
	if r.Ready() || f.closed.Load() != 0 {
		t.Fatal("snapshot failure kept admission open or closed active WAL")
	}
	f.gate <- nil
	if err := r.Shutdown(ctx(t)); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if f.closed.Load() != 1 || r.engine.AppliedSeq() != 0 || r.SnapshotSeq() != 0 {
		t.Fatal("failed owner continued applying")
	}
}

func TestWALFailureJoinsSnapshotBeforeClosingRun(t *testing.T) {
	f := fake(t)
	f.records = []wal.Record{{Seq: 1, Command: buy("existing")}}
	f.gate = make(chan error)
	o := DefaultOptions()
	o.BatchCommands = 1
	r, files := startSnapshotTest(t, f, o, hooks{}, true)
	snap := snapshotAsync(r, ctx(t))
	if doc := receive(t, files.started); doc.SnapshotSeq != 1 {
		t.Fatal("wrong capture")
	}
	order := submitAsync(r, ctx(t), buy("failing"))
	receive(t, f.calls)
	f.gate <- injected
	checkFailure(t, receive(t, order).err, "STORAGE_UNAVAILABLE", Unknown)
	checkFailure(t, receive(t, snap).err, "STORAGE_UNAVAILABLE", Unknown)
	if f.closed.Load() != 0 || r.Ready() {
		t.Fatal("WAL failure released ownership during snapshot publication")
	}
	files.gate <- nil
	if err := r.Shutdown(ctx(t)); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if len(files.published) != 1 || files.published[0].SnapshotSeq != 1 || r.SnapshotSeq() != 0 {
		t.Fatal("late publication must remain unacknowledged by the failed owner")
	}
}

func TestSnapshotSizeLimitAndWorkerFailurePolicies(t *testing.T) {
	for _, issue := range []error{snapshot.ErrTooLarge, errors.Join(snapshot.ErrTooLarge, injected), snapshot.ErrExistsMismatch, snapshot.ErrInvalid, panicPublisher} {
		t.Run(issue.Error(), func(t *testing.T) {
			f := fake(t)
			r, files := startSnapshotTest(t, f, DefaultOptions(), hooks{}, true)
			if _, err := r.Submit(ctx(t), buy("initial")); err != nil {
				t.Fatal(err)
			}
			snap := snapshotAsync(r, ctx(t))
			receive(t, files.started)
			files.gate <- issue
			got := receive(t, snap)
			if got.err == nil || r.SnapshotSeq() != 0 {
				t.Fatal("failed snapshot returned success/advanced sequence")
			}
			if issue == snapshot.ErrTooLarge {
				if !errors.Is(got.err, snapshot.ErrTooLarge) || !r.Ready() {
					t.Fatal("pre-I/O size refusal stopped trading")
				}
				snap = snapshotAsync(r, ctx(t))
				receive(t, files.started)
				files.gate <- nil
				if got := receive(t, snap); got.err != nil || got.result.SnapshotSeq != 1 {
					t.Fatal(got)
				}
				finish(t, r)
			} else {
				if r.Ready() || r.Shutdown(ctx(t)) == nil {
					t.Fatal("storage/internal failure not terminal")
				}
			}
		})
	}
}

func TestSnapshotCapturePanicFailsQueuedRequests(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			hook := func() { close(entered); <-release; panic("copy failure") }
			h := hooks{}
			if after {
				h.afterCapture = func(snapshot.Document) { hook() }
			} else {
				h.beforeCapture = hook
			}
			f := fake(t)
			r, files := startSnapshotTest(t, f, DefaultOptions(), h, true)
			snap := snapshotAsync(r, ctx(t))
			receive(t, entered)
			order := submitAsync(r, ctx(t), buy("queued"))
			deadline := time.Now().Add(5 * time.Second)
			for len(r.commands) == 0 && time.Now().Before(deadline) {
				stdruntime.Gosched()
			}
			if len(r.commands) == 0 {
				t.Fatal("command not queued")
			}
			once.Do(func() { close(release) })
			checkFailure(t, receive(t, snap).err, "INTERNAL_FAILURE", Unknown)
			checkFailure(t, receive(t, order).err, "INTERNAL_FAILURE", Unknown)
			if r.Shutdown(ctx(t)) == nil || f.closed.Load() != 1 || len(files.published) != 0 || len(f.records) != 0 {
				t.Fatal("copy failure leaked or published state")
			}
		})
	}
}

func TestSnapshotWaitsForCompleteBatchApplyBoundary(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f := fake(t)
	o := DefaultOptions()
	o.BatchCommands, o.BatchDelay = 2, time.Hour
	r, files := startSnapshotTest(t, f, o, hooks{afterApply: func(record wal.Record) {
		if record.Seq == 1 {
			close(entered)
			<-release
		}
	}}, true)
	a := submitAsync(r, ctx(t), buy("a"))
	b := submitAsync(r, ctx(t), buy("b"))
	receive(t, entered)
	snap := snapshotAsync(r, ctx(t))
	waitSnapshotAdmission(t, r, true)
	select {
	case <-files.started:
		t.Fatal("snapshot observed partial batch application")
	default:
	}
	once.Do(func() { close(release) })
	if receive(t, a).err != nil || receive(t, b).err != nil {
		t.Fatal("orders failed")
	}
	doc := receive(t, files.started)
	if doc.SnapshotSeq != 2 || len(doc.State.Orders) != 2 || len(doc.State.Results) != 2 || doc.State.Accounts[0].ReservedQuoteAtoms != 400 {
		t.Fatal("snapshot did not capture complete command boundary")
	}
	files.gate <- nil
	if receive(t, snap).err != nil {
		t.Fatal("snapshot failed")
	}
	finish(t, r)
}

func TestSnapshotUnavailableDoesNotLeakRequestSlots(t *testing.T) {
	r := startTest(t, fake(t), DefaultOptions(), hooks{})
	_, err := r.Snapshot(ctx(t))
	checkFailure(t, err, "SNAPSHOT_UNAVAILABLE", NotAdmitted)
	if len(r.slots) != 0 {
		t.Fatal("request slot leaked")
	}
}

func TestSnapshotAdmissionIsBounded(t *testing.T) {
	o := DefaultOptions()
	o.RequestLimit, o.PendingWaiters = 1, 1
	r, files := startSnapshotTest(t, fake(t), o, hooks{}, true)
	c, stopWaiting := context.WithCancel(context.Background())
	first := snapshotAsync(r, c)
	receive(t, files.started)
	_, err := r.Snapshot(ctx(t))
	checkFailure(t, err, "TOO_MANY_REQUESTS", NotAdmitted)
	stopWaiting()
	checkFailure(t, receive(t, first).err, "SNAPSHOT_TIMEOUT", Unknown)
	// All callers see the existing publication, even after its waiter left.
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Go(func() { _, err := r.Snapshot(context.Background()); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		checkFailure(t, err, "SNAPSHOT_BUSY", NotAdmitted)
	}
	if len(r.slots) != 0 {
		t.Fatal("rejected snapshot leaked admission slot")
	}
	files.gate <- nil
	waitSnapshotAdmission(t, r, false)
	finish(t, r)
	if len(files.published) != 1 {
		t.Fatal("more than one snapshot published")
	}
}

func TestQueuedSnapshotOnShutdownOrOwnerFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			f := fake(t)
			r, files := startSnapshotTest(t, f, DefaultOptions(), hooks{afterApply: func(wal.Record) {
				close(entered)
				<-release
				if fail {
					panic("owner failed with snapshot queued")
				}
			}}, false)
			order := submitAsync(r, ctx(t), buy("queued-snapshot"))
			receive(t, entered)
			snap := snapshotAsync(r, ctx(t))
			waitSnapshotAdmission(t, r, true)
			if len(r.snapshotRequests) != 1 {
				t.Fatal("snapshot was not waiting for owner")
			}
			c, stop := context.WithCancel(context.Background())
			stop()
			if err := r.Shutdown(c); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			once.Do(func() { close(release) })
			a, b := receive(t, order), receive(t, snap)
			if fail {
				checkFailure(t, a.err, "INTERNAL_FAILURE", Unknown)
				checkFailure(t, b.err, "INTERNAL_FAILURE", Unknown)
				if r.Shutdown(ctx(t)) == nil || len(files.published) != 0 {
					t.Fatal("failed owner published queued snapshot")
				}
			} else {
				if a.err != nil || b.err != nil || b.result.SnapshotSeq != 1 {
					t.Fatalf("shutdown lost accepted work: %+v %+v", a, b)
				}
				finish(t, r)
				if len(files.published) != 1 {
					t.Fatal("accepted snapshot not drained")
				}
			}
			if f.closed.Load() != 1 || len(r.slots) != 0 {
				t.Fatal("shutdown leaked storage/request ownership")
			}
		})
	}
}

func TestSnapshotConcurrentCommandsAndQueriesReplayEveryCapture(t *testing.T) {
	f := fake(t)
	r, files := startSnapshotTest(t, f, DefaultOptions(), hooks{}, false)
	calls := ctx(t)
	progress := make(chan struct{}, 128)
	var wg sync.WaitGroup
	errs := make(chan error, 512)
	// Independent producers use fixed seeds; actual durable order is recorded in
	// the journal. Every captured boundary is compared to that prefix below.
	for worker := range 4 {
		wg.Go(func() {
			rng := rand.New(rand.NewSource(int64(17 + worker)))
			account := []string{"alice", "bob"}[worker%2]
			for n := range 32 {
				id := fmt.Sprintf("w%d-%d", worker, n)
				c := command(account, id, []d.Side{d.Buy, d.Sell}[rng.Intn(2)], d.Int(98+rng.Intn(5)), d.Int(1+rng.Intn(4)))
				if n%4 == 3 {
					c = cancel(id, fmt.Sprintf("w%d-%d", worker, n-1))
					c.AccountID = account
				}
				first, err := r.Submit(calls, c)
				if err != nil {
					errs <- err
					return
				}
				progress <- struct{}{}
				if n%3 == 0 {
					again, err := r.Submit(calls, c)
					if err != nil || !reflect.DeepEqual(first, again) {
						errs <- fmt.Errorf("duplicate result changed: %v", err)
					}
				}
			}
		})
	}
	wg.Go(func() {
		for range 32 {
			v, err := r.Read(calls, Query{Kind: BalancesQuery, RunID: runID, AccountID: "alice"})
			if err != nil || v.AsOfSeq < 0 {
				errs <- fmt.Errorf("query during capture: %v", err)
			}
		}
	})
	wg.Go(func() {
		for range 24 {
			// Avoid a fast snapshot loop capturing only the initial empty state.
			for range 4 {
				select {
				case <-progress:
				case <-calls.Done():
					errs <- calls.Err()
					return
				}
			}
			if _, err := r.Snapshot(calls); err != nil {
				errs <- err
				return
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	finish(t, r)
	if len(f.records) != 128 || len(files.published) != 24 {
		t.Fatalf("missing work: %d commands, %d snapshots", len(f.records), len(files.published))
	}
	for _, doc := range files.published {
		restored, err := engine.Restore(runID, f.g.Config, doc.SnapshotSeq, doc.State)
		if err != nil {
			t.Fatal(err)
		}
		prefix, _ := engine.New(runID, f.g.Config)
		for _, record := range f.records {
			if _, err := prefix.Apply(record.Seq, record.Command); err != nil {
				t.Fatal(err)
			}
			if record.Seq == doc.SnapshotSeq && !reflect.DeepEqual(prefix.Capture(), doc.State) {
				t.Fatal("capture differs from its durable command prefix")
			}
			if record.Seq > doc.SnapshotSeq {
				if _, err := restored.Apply(record.Seq, record.Command); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !reflect.DeepEqual(restored.Capture(), r.engine.Capture()) {
			t.Fatal("captured prefix + WAL suffix differs from final state/results")
		}
	}
}
