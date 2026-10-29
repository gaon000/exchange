package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/wal"
)

func mustSubmit(t *testing.T, r *Runtime, c d.Command) d.Result {
	t.Helper()
	res, err := r.Submit(ctx(t), c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestScopedDetachedQueriesAndStableTradePages(t *testing.T) {
	f := fake(t)
	r := startTest(t, f, DefaultOptions(), hooks{})
	first := command("bob", "ask-1", d.Sell, 100, 2)
	mustSubmit(t, r, first)
	mustSubmit(t, r, command("bob", "ask-2", d.Sell, 101, 2))
	mustSubmit(t, r, command("alice", "buy-1", d.Buy, 101, 3))
	v := read(t, r, Query{Kind: BalancesQuery})
	if v.AsOfSeq != 3 || v.Balances.Base.Available != 1003 || v.Balances.Quote.Available != 1000000-301 {
		t.Fatalf("%+v", v)
	}
	order := read(t, r, Query{Kind: OrderQuery, ID: "ask-1", AccountID: "bob"})
	if order.Order.Status != d.Filled {
		t.Fatal(order)
	}
	initial := read(t, r, Query{Kind: ResultQuery, ID: "ask-1", RequestKind: d.PlaceOrder, AccountID: "bob"})
	if initial.Result.Order.Status != d.Open || initial.Result.CommandSeq != 1 {
		t.Fatal(initial)
	}
	initial.Result.Order.Status = d.Rejected
	order.Order.OpenQty = 999
	if read(t, r, Query{Kind: ResultQuery, ID: "ask-1", RequestKind: d.PlaceOrder, AccountID: "bob"}).Result.Order.Status != d.Open {
		t.Fatal("initial result alias")
	}
	if read(t, r, Query{Kind: OrderQuery, ID: "ask-1", AccountID: "bob"}).Order.OpenQty != 0 {
		t.Fatal("order alias")
	}
	_, err := r.Read(ctx(t), Query{RunID: runID, AccountID: "alice", Kind: OrderQuery, ID: "ask-1"})
	checkFailure(t, err, "ORDER_NOT_FOUND", NotAdmitted)
	if read(t, r, Query{Kind: ResultQuery, ID: "ask-1", RequestKind: d.PlaceOrder}).ResultState != "UNKNOWN" {
		t.Fatal("other account result exposed")
	}
	book := read(t, r, Query{Kind: BookQuery, Limit: 20})
	if len(book.Bids) != 0 || len(book.Asks) != 1 || book.Asks[0].QuantityLots != 1 {
		t.Fatal(book)
	}
	book.Asks[0].QuantityLots = 999
	if read(t, r, Query{Kind: BookQuery, Limit: 20}).Asks[0].QuantityLots != 1 {
		t.Fatal("book alias")
	}
	page := read(t, r, Query{Kind: TradesQuery, Limit: 1})
	if page.AsOfSeq != 3 || page.ThroughSeq != 3 || len(page.Trades) != 1 || page.Trades[0].MatchIndex != 1 || page.Trades[0].PriceTicks != 100 {
		t.Fatal(page)
	}
	mustSubmit(t, r, command("bob", "later-ask", d.Sell, 99, 1))
	mustSubmit(t, r, command("alice", "later-buy", d.Buy, 101, 1))
	through := page.ThroughSeq
	q := Query{Kind: TradesQuery, Limit: 100, AfterSeq: 3, AfterIndex: 1, ThroughSeq: &through}
	next := read(t, r, q)
	if next.AsOfSeq != 5 || next.ThroughSeq != 3 || len(next.Trades) != 1 || next.Trades[0].CommandSeq != 3 || next.Trades[0].MatchIndex != 2 {
		t.Fatal(next)
	}
	next.Trades[0].QuantityLots = 999
	if read(t, r, q).Trades[0].QuantityLots != 1 {
		t.Fatal("trade alias")
	}
	q.AfterIndex = 2
	if len(read(t, r, q).Trades) != 0 {
		t.Fatal("later trades leaked into fixed page")
	}
	q.AfterIndex = 99
	q.RunID = runID
	q.AccountID = "alice"
	_, err = r.Read(ctx(t), q)
	checkFailure(t, err, "INVALID_REQUEST", NotAdmitted)
	tooFar := d.Int(6)
	_, err = r.Read(ctx(t), Query{RunID: runID, AccountID: "alice", Kind: TradesQuery, Limit: 1, ThroughSeq: &tooFar})
	checkFailure(t, err, "INVALID_REQUEST", NotAdmitted)
	finish(t, r)
}

func TestPreAdmissionValidationDoesNotLog(t *testing.T) {
	f := fake(t)
	r := startTest(t, f, DefaultOptions(), hooks{})
	for _, tc := range []struct {
		command d.Command
		code    string
	}{
		{command("unknown", "a", d.Buy, 100, 1), "UNKNOWN_ACCOUNT"},
		{command("alice", "a", d.Buy, 0, 1), "INVALID_REQUEST"},
		{func() d.Command { c := buy("a"); c.RunID = "00000000-0000-4000-8000-000000000002"; return c }(), "RUN_MISMATCH"},
	} {
		_, err := r.Submit(ctx(t), tc.command)
		checkFailure(t, err, tc.code, NotAdmitted)
	}
	for _, q := range []Query{{Kind: 0}, {Kind: OrderQuery}, {Kind: BookQuery, Limit: 101}, {Kind: TradesQuery, Limit: 0}, {Kind: TradesQuery, Limit: 1, AfterSeq: 1}, {Kind: TradesQuery, Limit: 1, AfterSeq: 1, AfterIndex: 1}, {Kind: ResultQuery, ID: "a", RequestKind: "OTHER"}} {
		q.RunID = runID
		q.AccountID = "alice"
		_, err := r.Read(ctx(t), q)
		checkFailure(t, err, "INVALID_REQUEST", NotAdmitted)
	}
	_, err := r.Read(ctx(t), Query{RunID: runID, AccountID: "unknown", Kind: BalancesQuery})
	checkFailure(t, err, "UNKNOWN_ACCOUNT", NotAdmitted)
	c, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Submit(c, buy("a"))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	finish(t, r)
	if len(f.records) != 0 {
		t.Fatal("invalid requests consumed WAL sequence")
	}
}

func TestQueryQueueAndTimeoutDoNotBlockPublication(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := fake(t)
	o := DefaultOptions()
	o.BatchCommands = 1
	o.QueryQueue = 1
	r := startTest(t, f, o, hooks{beforeApply: func(wal.Record) { close(entered); <-release }})
	out := submitAsync(r, ctx(t), buy("a"))
	receive(t, entered)
	c, cancel := context.WithCancel(context.Background())
	queries := make(chan error, 1)
	go func() {
		_, err := r.Read(c, Query{RunID: runID, AccountID: "alice", Kind: BalancesQuery})
		queries <- err
	}()
	waitSlots(t, r, 2)
	_, err := r.Read(ctx(t), Query{RunID: runID, AccountID: "alice", Kind: BalancesQuery})
	checkFailure(t, err, "QUEUE_FULL", NotAdmitted)
	cancel()
	checkFailure(t, receive(t, queries), "RESULT_TIMEOUT", Unknown)
	close(release)
	if got := receive(t, out); got.err != nil {
		t.Fatal(got.err)
	}
	if v := read(t, r, Query{Kind: BalancesQuery}); v.AsOfSeq != 1 {
		t.Fatal(v)
	}
	finish(t, r)
}

func TestInvalidOptionsFailBeforeStarting(t *testing.T) {
	for _, modify := range []func(*Options){func(o *Options) { o.CommandQueue = 0 }, func(o *Options) { o.QueryQueue = 0 }, func(o *Options) { o.RequestLimit = 0 }, func(o *Options) { o.PendingWaiters = 0 }, func(o *Options) { o.BatchCommands = 257 }, func(o *Options) { o.BatchBytes = 4099 }, func(o *Options) { o.BatchBytes = wal.MaxGroupPayload + 1 }, func(o *Options) { o.BatchDelay = -time.Second }} {
		f := fake(t)
		o := DefaultOptions()
		modify(&o)
		if r, err := start(f, o, hooks{}); err == nil || r != nil || f.closed.Load() != 1 {
			t.Fatal("invalid options started runtime")
		}
	}
}
