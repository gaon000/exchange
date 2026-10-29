package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	stdruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/wal"
)

const runID = "00000000-0000-4000-8000-000000000001"

var injected = errors.New("injected failure")

// The owner and writer have exclusive access in each phase. Tests inspect records
// only after Shutdown; channels control sync boundaries without timing guesses.
type fakeJournal struct {
	g              wal.Genesis
	records        []wal.Record
	durable        d.Int
	calls          chan []wal.Record
	gate           chan error
	confirmErr     error
	replayErr      error
	confirmStarted chan struct{}
	confirmGate    chan struct{}
	wrongDurable   bool
	panicAppend    bool
	closed         atomic.Int32
}

func fake(t *testing.T) *fakeJournal {
	t.Helper()
	g, err := wal.NewGenesis(runID, d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 1000, QuoteAtoms: 1000000}, {AccountID: "bob", BaseLots: 1000, QuoteAtoms: 1000000}}))
	if err != nil {
		t.Fatal(err)
	}
	return &fakeJournal{g: g, calls: make(chan []wal.Record, 1024)}
}
func (f *fakeJournal) Genesis() wal.Genesis { return f.g }
func (f *fakeJournal) Recovery() wal.Recovery {
	var seq d.Int
	if len(f.records) > 0 {
		seq = f.records[len(f.records)-1].Seq
	}
	return wal.Recovery{LastSeq: seq}
}
func (f *fakeJournal) Replay(visit func(wal.Record) error) error {
	for _, r := range f.records {
		if err := visit(r); err != nil {
			return err
		}
	}
	return f.replayErr
}
func (f *fakeJournal) ConfirmRecovery() error {
	if f.confirmStarted != nil {
		close(f.confirmStarted)
		<-f.confirmGate
	}
	if f.confirmErr != nil {
		return f.confirmErr
	}
	f.durable = f.Recovery().LastSeq
	return nil
}
func (f *fakeJournal) Append(batch []wal.Record) error {
	if f.panicAppend {
		panic("writer failure")
	}
	copyBatch := append([]wal.Record(nil), batch...)
	f.records = append(f.records, copyBatch...)
	f.calls <- copyBatch
	if f.gate != nil {
		if err := <-f.gate; err != nil {
			return err
		}
	}
	f.durable = batch[len(batch)-1].Seq
	if f.wrongDurable {
		f.durable++
	}
	return nil
}
func (f *fakeJournal) DurableSeq() d.Int { return f.durable }
func (f *fakeJournal) Close() error      { f.closed.Add(1); return nil }

func command(account, id string, side d.Side, price, qty d.Int) d.Command {
	return d.Command{RunID: runID, AccountID: account, Kind: d.PlaceOrder, Place: d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}}
}
func buy(id string) d.Command { return command("alice", id, d.Buy, 100, 2) }
func cancel(id, target string) d.Command {
	return d.Command{RunID: runID, AccountID: "alice", Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: id, ClientOrderID: target}}
}
func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}
func startTest(t *testing.T, f *fakeJournal, o Options, h hooks) *Runtime {
	t.Helper()
	r, err := start(f, o, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(c)
	})
	return r
}
func submitAsync(r *Runtime, c context.Context, cmd d.Command) <-chan commandReply {
	out := make(chan commandReply, 1)
	go func() { res, err := r.Submit(c, cmd); out <- commandReply{res, err} }()
	return out
}
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("channel deadline")
		var zero T
		return zero
	}
}
func checkFailure(t *testing.T, err error, code string, outcome Outcome) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) || f.Code != code || f.Outcome != outcome {
		t.Fatalf("want %s/%s got %v", code, outcome, err)
	}
}
func read(t *testing.T, r *Runtime, q Query) View {
	t.Helper()
	q.RunID = r.RunID()
	if q.AccountID == "" {
		q.AccountID = "alice"
	}
	v, err := r.Read(ctx(t), q)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func pending(t *testing.T, r *Runtime, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if read(t, r, Query{Kind: ResultQuery, ID: id, RequestKind: d.PlaceOrder}).ResultState == "PENDING" {
			return
		}
		stdruntime.Gosched()
	}
	t.Fatal("request never became pending")
}
func waitSlots(t *testing.T, r *Runtime, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.slots) != n {
		if time.Now().After(deadline) {
			t.Fatalf("slots=%d want %d", len(r.slots), n)
		}
		stdruntime.Gosched()
	}
	r.admission.Lock()
	r.admission.Unlock()
}
func finish(t *testing.T, r *Runtime) {
	t.Helper()
	if err := r.Shutdown(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestDurableBeforeApplyAndResponse(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error, 2)
	o := DefaultOptions()
	o.BatchCommands = 1
	r := startTest(t, f, o, hooks{})
	out := submitAsync(r, ctx(t), buy("a"))
	batch := receive(t, f.calls)
	if len(batch) != 1 || batch[0].Seq != 1 {
		t.Fatal(batch)
	}
	v := read(t, r, Query{Kind: BalancesQuery})
	if v.AsOfSeq != 0 || v.Balances.Quote.Reserved != 0 {
		t.Fatalf("applied before sync: %+v", v)
	}
	if read(t, r, Query{Kind: ResultQuery, ID: "a", RequestKind: d.PlaceOrder}).ResultState != "PENDING" {
		t.Fatal("not pending during sync")
	}
	select {
	case res := <-out:
		t.Fatalf("responded before sync: %+v", res)
	default:
	}
	f.gate <- nil
	res := receive(t, out)
	if res.err != nil || res.result.CommandSeq != 1 {
		t.Fatalf("%+v", res)
	}
	v = read(t, r, Query{Kind: BalancesQuery})
	if v.AsOfSeq != 1 || v.Balances.Quote.Reserved != 200 {
		t.Fatalf("%+v", v)
	}
	res.result.Order.OpenQty = 999
	result := read(t, r, Query{Kind: ResultQuery, ID: "a", RequestKind: d.PlaceOrder})
	if result.Result.Order.OpenQty != 2 {
		t.Fatal("result alias")
	}
	finish(t, r)
}

func TestPendingDuplicatesConflictWaiterLimitAndRunLimit(t *testing.T) {
	f := fake(t)
	f.g.Config.MaxCommands = 1
	o := DefaultOptions()
	o.BatchDelay = time.Hour
	o.PendingWaiters = 2
	r := startTest(t, f, o, hooks{})
	a := submitAsync(r, ctx(t), buy("a"))
	pending(t, r, "a")
	b := submitAsync(r, ctx(t), buy("a"))
	waitSlots(t, r, 2)
	_, err := r.Submit(ctx(t), buy("a"))
	checkFailure(t, err, "DUPLICATE_WAITERS_FULL", NotAdmitted)
	conflict := buy("a")
	conflict.Place.PriceTicks++
	_, err = r.Submit(ctx(t), conflict)
	checkFailure(t, err, "IDEMPOTENCY_CONFLICT", NotAdmitted)
	_, err = r.Submit(ctx(t), buy("new"))
	checkFailure(t, err, "RUN_LIMIT_REACHED", NotAdmitted)
	finish(t, r)
	ra, rb := receive(t, a), receive(t, b)
	if ra.err != nil || rb.err != nil || !reflect.DeepEqual(ra.result, rb.result) || ra.result.CommandSeq != 1 {
		t.Fatalf("%+v %+v", ra, rb)
	}
	if len(f.records) != 1 {
		t.Fatalf("duplicate logged %d times", len(f.records))
	}
	// Recovered completed duplicates must also precede the run-limit check.
	restarted := fake(t)
	restarted.g = f.g
	restarted.records = append([]wal.Record(nil), f.records...)
	r = startTest(t, restarted, DefaultOptions(), hooks{})
	got, err := r.Submit(ctx(t), buy("a"))
	if err != nil || !reflect.DeepEqual(got, ra.result) {
		t.Fatal(err)
	}
	_, err = r.Submit(ctx(t), conflict)
	checkFailure(t, err, "IDEMPOTENCY_CONFLICT", NotAdmitted)
	_, err = r.Submit(ctx(t), buy("new"))
	checkFailure(t, err, "RUN_LIMIT_REACHED", NotAdmitted)
	finish(t, r)
}

func TestQueueAndGlobalLimitAreImmediate(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprint(global), func(t *testing.T) {
			f := fake(t)
			f.gate = make(chan error, 3)
			o := DefaultOptions()
			o.CommandQueue = 1
			o.BatchCommands = 1
			if global {
				o.RequestLimit = 2
				o.PendingWaiters = 2
			}
			r := startTest(t, f, o, hooks{})
			a := submitAsync(r, ctx(t), buy("a"))
			receive(t, f.calls)
			b := submitAsync(r, ctx(t), buy("b"))
			waitSlots(t, r, 2)
			_, err := r.Submit(ctx(t), buy("c"))
			code := "QUEUE_FULL"
			if global {
				code = "TOO_MANY_REQUESTS"
			}
			checkFailure(t, err, code, NotAdmitted)
			f.gate <- nil
			if res := receive(t, a); res.err != nil {
				t.Fatal(res.err)
			}
			receive(t, f.calls)
			f.gate <- nil
			if res := receive(t, b); res.err != nil {
				t.Fatal(res.err)
			}
			finish(t, r)
			if len(f.records) != 2 {
				t.Fatal("rejected attempt logged")
			}
		})
	}
}

func TestTimeoutDoesNotCancelAcceptedOrQueuedCommands(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error, 3)
	o := DefaultOptions()
	o.BatchCommands = 1
	r := startTest(t, f, o, hooks{})
	c1, cancel1 := context.WithCancel(context.Background())
	a := submitAsync(r, c1, buy("a"))
	receive(t, f.calls)
	c2, cancel2 := context.WithCancel(context.Background())
	b := submitAsync(r, c2, buy("b"))
	waitSlots(t, r, 2)
	cancel1()
	cancel2()
	checkFailure(t, receive(t, a).err, "RESULT_TIMEOUT", Unknown)
	checkFailure(t, receive(t, b).err, "RESULT_TIMEOUT", Unknown)
	f.gate <- nil
	receive(t, f.calls)
	f.gate <- nil
	result, err := r.Submit(ctx(t), buy("b"))
	if err != nil || result.CommandSeq != 2 {
		t.Fatalf("retry %v %v", result, err)
	}
	finish(t, r)
	if len(f.records) != 2 || r.engine.AppliedSeq() != 2 {
		t.Fatal("disconnected commands were lost or repeated")
	}
}

func TestCancelledPendingWaiterReleasesCapacity(t *testing.T) {
	f := fake(t)
	o := DefaultOptions()
	o.BatchDelay = time.Hour
	o.PendingWaiters = 1
	r := startTest(t, f, o, hooks{})
	c, cancel := context.WithCancel(context.Background())
	a := submitAsync(r, c, buy("a"))
	pending(t, r, "a")
	cancel()
	checkFailure(t, receive(t, a).err, "RESULT_TIMEOUT", Unknown)
	b := submitAsync(r, ctx(t), buy("a"))
	waitSlots(t, r, 1)
	// FIFO command processing makes this third attempt observe the replacement waiter.
	_, err := r.Submit(ctx(t), buy("a"))
	checkFailure(t, err, "DUPLICATE_WAITERS_FULL", NotAdmitted)
	finish(t, r)
	if result := receive(t, b); result.err != nil || result.result.CommandSeq != 1 {
		t.Fatalf("%+v", result)
	}
}

func TestSyncFailureStopsAllWorkAndReplaysUnacknowledgedGroup(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error, 1)
	o := DefaultOptions()
	o.BatchCommands = 1
	r := startTest(t, f, o, hooks{})
	a := submitAsync(r, ctx(t), buy("a"))
	receive(t, f.calls)
	b := submitAsync(r, ctx(t), buy("b"))
	waitSlots(t, r, 2)
	f.gate <- injected
	checkFailure(t, receive(t, a).err, "STORAGE_UNAVAILABLE", Unknown)
	checkFailure(t, receive(t, b).err, "STORAGE_UNAVAILABLE", Unknown)
	if err := r.Shutdown(ctx(t)); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if r.Ready() || r.engine.AppliedSeq() != 0 || f.durable != 0 || len(f.records) != 1 || f.closed.Load() != 1 {
		t.Fatal("failed storage advanced state or leaked handle")
	}
	_, err := r.Submit(ctx(t), buy("a"))
	checkFailure(t, err, "NOT_READY", NotAdmitted)
	restarted := fake(t)
	restarted.records = append([]wal.Record(nil), f.records...)
	r = startTest(t, restarted, DefaultOptions(), hooks{})
	res, err := r.Submit(ctx(t), buy("a"))
	if err != nil || res.CommandSeq != 1 {
		t.Fatal(err)
	}
	res, err = r.Submit(ctx(t), buy("b"))
	if err != nil || res.CommandSeq != 2 {
		t.Fatal(err)
	}
	finish(t, r)
}

func TestBatchClosesOnCountBytesAndTime(t *testing.T) {
	for _, mode := range []string{"count", "bytes", "time"} {
		t.Run(mode, func(t *testing.T) {
			f := fake(t)
			f.gate = make(chan error)
			o := DefaultOptions()
			o.BatchDelay = time.Hour
			n := 2
			if mode == "count" {
				o.BatchCommands = 2
			}
			if mode == "bytes" {
				o.BatchBytes = wal.MaxRecordBytes + 4
				n = 30
			}
			if mode == "time" {
				o.BatchDelay = 2 * time.Millisecond
				n = 1
			}
			r := startTest(t, f, o, hooks{})
			outs := make([]<-chan commandReply, 0, n)
			for i := 0; i < n; i++ {
				outs = append(outs, submitAsync(r, ctx(t), buy(fmt.Sprintf("order-%02d", i))))
			}
			first := receive(t, f.calls)
			if mode == "count" && len(first) != 2 || mode == "time" && len(first) != 1 || mode == "bytes" && (len(first) <= 1 || len(first) >= n) {
				t.Fatalf("%s first group size %d", mode, len(first))
			}
			waitSlots(t, r, n)
			close(f.gate)
			finish(t, r)
			for _, out := range outs {
				if res := receive(t, out); res.err != nil {
					t.Fatal(res.err)
				}
			}
			for len(f.calls) > 0 {
				batch := <-f.calls
				size := 0
				for _, record := range batch {
					n, err := wal.PayloadBytes(record)
					if err != nil {
						t.Fatal(err)
					}
					size += n
				}
				if len(batch) > o.BatchCommands || size > o.BatchBytes {
					t.Fatal("batch bounds exceeded")
				}
			}
			if len(f.records) != n {
				t.Fatalf("%d records, want %d", len(f.records), n)
			}
		})
	}
}

func TestShutdownTimeoutStillDrainsAndRejectsAdmission(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error, 2)
	o := DefaultOptions()
	o.BatchCommands = 1
	r := startTest(t, f, o, hooks{})
	a := submitAsync(r, ctx(t), buy("a"))
	receive(t, f.calls)
	b := submitAsync(r, ctx(t), buy("b"))
	waitSlots(t, r, 2)
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Shutdown(c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.Ready() {
		t.Fatal("ready during drain")
	}
	_, err := r.Submit(ctx(t), buy("new"))
	checkFailure(t, err, "NOT_READY", NotAdmitted)
	f.gate <- nil
	receive(t, f.calls)
	f.gate <- nil
	finish(t, r)
	for _, out := range []<-chan commandReply{a, b} {
		if res := receive(t, out); res.err != nil {
			t.Fatal(res.err)
		}
	}
	if f.closed.Load() != 1 || len(f.records) != 2 {
		t.Fatal("did not drain once")
	}
}

func TestRecoveryGateAndFailures(t *testing.T) {
	f := fake(t)
	f.confirmStarted = make(chan struct{})
	f.confirmGate = make(chan struct{})
	type started struct {
		r   *Runtime
		err error
	}
	out := make(chan started, 1)
	go func() { r, err := start(f, DefaultOptions(), hooks{}); out <- started{r, err} }()
	receive(t, f.confirmStarted)
	select {
	case <-out:
		t.Fatal("published runtime before recovery sync")
	default:
	}
	close(f.confirmGate)
	startedRuntime := receive(t, out)
	if startedRuntime.err != nil || !startedRuntime.r.Ready() {
		t.Fatal(startedRuntime.err)
	}
	finish(t, startedRuntime.r)
	for _, kind := range []string{"sync", "replay", "duplicate", "gap", "wrong run"} {
		t.Run(kind, func(t *testing.T) {
			f := fake(t)
			f.records = []wal.Record{{Seq: 1, Command: buy("a")}}
			switch kind {
			case "sync":
				f.confirmErr = injected
			case "replay":
				f.replayErr = injected
			case "duplicate":
				f.records = append(f.records, wal.Record{Seq: 2, Command: buy("a")})
			case "gap":
				f.records[0].Seq = 2
			case "wrong run":
				f.records[0].Command.RunID = "00000000-0000-4000-8000-000000000002"
			}
			if r, err := start(f, DefaultOptions(), hooks{}); err == nil || r != nil || f.closed.Load() != 1 {
				t.Fatalf("%v %v", r, err)
			}
		})
	}
}

func TestOwnerAndWriterInternalFailureAreFailStop(t *testing.T) {
	for _, kind := range []string{"before apply", "after apply", "wrong durable", "writer panic"} {
		t.Run(kind, func(t *testing.T) {
			f := fake(t)
			o := DefaultOptions()
			o.BatchCommands = 1
			h := hooks{}
			switch kind {
			case "before apply":
				h.beforeApply = func(wal.Record) { panic("apply failure") }
			case "after apply":
				h.afterApply = func(wal.Record) { panic("publication failure") }
			case "wrong durable":
				f.wrongDurable = true
			case "writer panic":
				f.panicAppend = true
			}
			r := startTest(t, f, o, h)
			_, err := r.Submit(ctx(t), buy("a"))
			checkFailure(t, err, "INTERNAL_FAILURE", Unknown)
			if err := r.Shutdown(ctx(t)); err == nil {
				t.Fatal("internal failure hidden")
			}
			if r.Ready() || f.closed.Load() != 1 {
				t.Fatal("not stopped")
			}
			_, err = r.Read(ctx(t), Query{RunID: runID, AccountID: "alice", Kind: BalancesQuery})
			checkFailure(t, err, "NOT_READY", NotAdmitted)
		})
	}
}

func TestConcurrentCommandsAndRetriesMatchReplay(t *testing.T) {
	f := fake(t)
	r := startTest(t, f, DefaultOptions(), hooks{})
	commands := make([]d.Command, 80)
	rng := rand.New(rand.NewSource(73))
	for i := range commands {
		if rng.Intn(3) == 0 {
			commands[i] = cancel(fmt.Sprintf("c%d", i), fmt.Sprintf("o%d", rng.Intn(i+1)))
		} else {
			account, side := "alice", d.Buy
			if rng.Intn(2) == 0 {
				account, side = "bob", d.Sell
			}
			commands[i] = command(account, fmt.Sprintf("o%d", i), side, d.Int(100+rng.Intn(5)), d.Int(1+rng.Intn(3)))
		}
	}
	type completed struct {
		cmd   d.Command
		reply commandReply
	}
	results := make(chan completed, 240)
	queryContext, cancelQueries := context.WithCancel(context.Background())
	queryDone := make(chan error, 1)
	go func() {
		var previous d.Int
		for {
			v, err := r.Read(queryContext, Query{RunID: runID, AccountID: "alice", Kind: BookQuery, Limit: 20})
			if queryContext.Err() != nil {
				queryDone <- nil
				return
			}
			if err != nil {
				queryDone <- err
				return
			}
			if v.AsOfSeq < previous {
				queryDone <- errors.New("query sequence regressed")
				return
			}
			previous = v.AsOfSeq
			if len(v.Bids) > 0 && len(v.Asks) > 0 && v.Bids[0].PriceTicks >= v.Asks[0].PriceTicks {
				queryDone <- errors.New("query exposed crossed intermediate book")
				return
			}
			for i, level := range v.Bids {
				if level.QuantityLots <= 0 || i > 0 && v.Bids[i-1].PriceTicks <= level.PriceTicks {
					queryDone <- errors.New("invalid bid snapshot")
					return
				}
			}
			for i, level := range v.Asks {
				if level.QuantityLots <= 0 || i > 0 && v.Asks[i-1].PriceTicks >= level.PriceTicks {
					queryDone <- errors.New("invalid ask snapshot")
					return
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for _, c := range commands {
		for range 3 {
			wg.Add(1)
			go func(c d.Command) {
				defer wg.Done()
				res, err := r.Submit(context.Background(), c)
				results <- completed{c, commandReply{res, err}}
			}(c)
		}
	}
	wg.Wait()
	close(results)
	cancelQueries()
	if err := receive(t, queryDone); err != nil {
		t.Fatal(err)
	}
	finish(t, r)
	if len(f.records) != len(commands) {
		t.Fatalf("logged %d unique commands", len(f.records))
	}
	replayed, err := engine.New(f.g.RunID, f.g.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range f.records {
		if _, err := replayed.Apply(record.Seq, record.Command); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(replayed.State(), r.engine.State()) {
		t.Fatal("runtime state differs from sequential WAL replay")
	}
	for done := range results {
		want, found, err := replayed.Lookup(done.cmd)
		if err != nil || !found || done.reply.err != nil || !reflect.DeepEqual(want, done.reply.result) {
			t.Fatalf("%+v want %+v err %v", done, want, err)
		}
	}
}

func TestBusinessRejectionRemainsInitialResultAfterBalanceChanges(t *testing.T) {
	f := fake(t)
	f.g.Config.Accounts[0].QuoteAtoms = 0
	r := startTest(t, f, DefaultOptions(), hooks{})
	first := mustSubmit(t, r, buy("rejected"))
	if first.Code != d.InsufficientBalance {
		t.Fatal(first)
	}
	mustSubmit(t, r, command("alice", "seller", d.Sell, 100, 2))
	mustSubmit(t, r, command("bob", "taker", d.Buy, 100, 2))
	if v := read(t, r, Query{Kind: BalancesQuery}); v.Balances.Quote.Available != 200 {
		t.Fatal(v)
	}
	retry := mustSubmit(t, r, buy("rejected"))
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("business rejection was reevaluated")
	}
	if fresh := mustSubmit(t, r, buy("funded")); fresh.Code != d.Accepted || fresh.CommandSeq != 4 {
		t.Fatal(fresh)
	}
	finish(t, r)
	if len(f.records) != 4 {
		t.Fatal("rejection retry consumed a sequence")
	}
	restart := fake(t)
	restart.g = f.g
	restart.records = append([]wal.Record(nil), f.records...)
	r = startTest(t, restart, DefaultOptions(), hooks{})
	if got := mustSubmit(t, r, buy("rejected")); !reflect.DeepEqual(got, first) {
		t.Fatal("replayed rejection differs")
	}
	finish(t, r)
}

func TestCancelFillRaceAndDoubleCancellation(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		f := fake(t)
		r := startTest(t, f, DefaultOptions(), hooks{})
		mustSubmit(t, r, command("bob", "maker", d.Sell, 100, 1))
		c := cancel("cancel-maker", "maker")
		c.AccountID = "bob"
		buyResult := submitAsync(r, ctx(t), command("alice", "taker", d.Buy, 100, 1))
		cancelResult := submitAsync(r, ctx(t), c)
		b, cr := receive(t, buyResult), receive(t, cancelResult)
		if b.err != nil || cr.err != nil {
			t.Fatalf("%v %v", b.err, cr.err)
		}
		if b.result.Order.Status == d.Filled {
			if cr.result.Code != d.AlreadyFilled {
				t.Fatal(cr)
			}
		} else if b.result.Order.Status != d.Open || cr.result.Code != d.CancelSucceeded {
			t.Fatalf("inconsistent race: %+v %+v", b.result, cr.result)
		}
		finish(t, r)
	}
	f := fake(t)
	r := startTest(t, f, DefaultOptions(), hooks{})
	mustSubmit(t, r, buy("maker"))
	a := submitAsync(r, ctx(t), cancel("a", "maker"))
	b := submitAsync(r, ctx(t), cancel("b", "maker"))
	ra, rb := receive(t, a), receive(t, b)
	if ra.err != nil || rb.err != nil {
		t.Fatalf("%v %v", ra.err, rb.err)
	}
	if !(ra.result.Code == d.CancelSucceeded && rb.result.Code == d.AlreadyCancelled || ra.result.Code == d.AlreadyCancelled && rb.result.Code == d.CancelSucceeded) {
		t.Fatalf("%+v %+v", ra.result, rb.result)
	}
	if got := mustSubmit(t, r, cancel("a", "maker")); !reflect.DeepEqual(got, ra.result) {
		t.Fatal("cancel retry changed result")
	}
	if missing := mustSubmit(t, r, cancel("unknown", "missing")); missing.Code != d.OrderNotFound || missing.Order != nil {
		t.Fatal(missing)
	}
	finish(t, r)
	if len(f.records) != 4 {
		t.Fatal("cancellation results not retained once")
	}
}

func TestShutdownRacesAdmissionWithoutLosingAcceptedRequests(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error)
	o := DefaultOptions()
	o.BatchCommands = 1
	o.CommandQueue = 16
	r := startTest(t, f, o, hooks{})
	first := submitAsync(r, ctx(t), buy("first"))
	receive(t, f.calls)
	barrier := make(chan struct{})
	results := make(chan commandReply, 100)
	var submitters sync.WaitGroup
	for i := 0; i < 100; i++ {
		submitters.Add(1)
		go func(i int) {
			defer submitters.Done()
			<-barrier
			result, err := r.Submit(context.Background(), buy(fmt.Sprintf("race-%d", i)))
			results <- commandReply{result, err}
		}(i)
	}
	stopped := make(chan error, 1)
	go func() { <-barrier; stopped <- r.Shutdown(context.Background()) }()
	close(barrier)
	close(f.gate)
	submitters.Wait()
	close(results)
	if err := receive(t, stopped); err != nil {
		t.Fatal(err)
	}
	if result := receive(t, first); result.err != nil {
		t.Fatal(result.err)
	}
	success := 1
	for result := range results {
		if result.err == nil {
			success++
			continue
		}
		var err *Failure
		if !errors.As(result.err, &err) || err.Outcome != NotAdmitted || (err.Code != "NOT_READY" && err.Code != "QUEUE_FULL") {
			t.Fatalf("unexpected shutdown outcome: %v", result.err)
		}
	}
	if len(f.records) != success || r.engine.AppliedSeq() != d.Int(success) {
		t.Fatalf("lost admission: %d results, %d records", success, len(f.records))
	}
}
