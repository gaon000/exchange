package engine_test

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/strictjson"
)

func checkpointCommands() []d.Command {
	return []d.Command{
		cancel("a", "before-create", "future"),
		place("b", "ask-1", d.Sell, 100, 3), place("c", "ask-2", d.Sell, 100, 3),
		place("a", "fill", d.Buy, 105, 4), place("a", "rest-1", d.Buy, 90, 5), place("a", "rest-2", d.Buy, 90, 4),
		place("c", "ask-3", d.Sell, 110, 5), place("a", "rejected", d.Buy, d.MaxPriceTicks, d.MaxQuantityLots),
		cancel("a", "cancel-rest", "rest-1"), cancel("a", "again", "rest-1"), cancel("b", "filled", "ask-1"),
		place("a", "self-ask", d.Sell, 120, 2), place("a", "stp", d.Buy, 130, 20), place("a", "future", d.Buy, 80, 1),
		place("c", "best-bid", d.Buy, 97, 1),
	}
}

func TestIndependentCheckpointGoldenJSON(t *testing.T) {
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", QuoteAtoms: 1000}, {AccountID: "b", BaseLots: 5}})
	e := newEngine(t, c)
	apply(t, e, place("b", "sell", d.Sell, 100, 2))
	apply(t, e, place("a", "buy", d.Buy, 110, 1))
	apply(t, e, cancel("b", "cancel-sell", "sell"))
	want, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(e.Capture())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("checkpoint wire contract changed")
	}
	copy := e.CopyCheckpoint()
	engine.CanonicalizeCheckpoint(&copy)
	encodedCopy, err := json.Marshal(copy)
	if err != nil || !bytes.Equal(encodedCopy, want) {
		t.Fatal("deferred canonicalization changed golden bytes")
	}
	var decoded d.Checkpoint
	if err := strictjson.Decode(want, &decoded); err != nil {
		t.Fatal(err)
	}
	restored, err := engine.Restore(runID, c, 3, decoded)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, restored.State(), e.State())
}

func restore(t testing.TB, e *engine.Engine, c d.Config) *engine.Engine {
	t.Helper()
	got, err := engine.Restore(runID, c, e.AppliedSeq(), e.Capture())
	if err != nil {
		t.Fatal(err)
	}
	equal(t, got.State(), e.State())
	equal(t, got.Capture(), e.Capture())
	return got
}

func TestCheckpointEveryBoundaryAndContinuedMatching(t *testing.T) {
	commands := checkpointCommands()
	baseline := newEngine(t, config())
	results := make([]d.Result, len(commands))
	for i, c := range commands {
		results[i] = apply(t, baseline, c)
	}
	for boundary := 0; boundary <= len(commands); boundary++ {
		e := newEngine(t, config())
		for _, c := range commands[:boundary] {
			apply(t, e, c)
		}
		cp := e.Capture()
		wire, err := json.Marshal(cp)
		if err != nil {
			t.Fatal(err)
		}
		var decoded d.Checkpoint
		if err := strictjson.Decode(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		r, err := engine.Restore(runID, config(), e.AppliedSeq(), decoded)
		if err != nil {
			t.Fatalf("boundary %d: %v", boundary, err)
		}
		equal(t, r.State(), e.State())
		equal(t, r.Capture(), cp)
		for i, c := range commands[boundary:] {
			equal(t, apply(t, r, c), results[boundary+i])
		}
		equal(t, r.State(), baseline.State())
		for i, c := range commands {
			result, found, err := r.Lookup(c)
			if err != nil || !found || !reflect.DeepEqual(result, results[i]) {
				t.Fatalf("boundary %d lost original result %d: %v", boundary, i, err)
			}
		}
	}
}

func TestCheckpointIsDetachedBothDirections(t *testing.T) {
	e := newEngine(t, config())
	for _, c := range checkpointCommands() {
		apply(t, e, c)
	}
	want := e.State()
	cp := e.Capture()
	restored, err := engine.Restore(runID, config(), e.AppliedSeq(), cp)
	if err != nil {
		t.Fatal(err)
	}
	cp.Accounts[0].AvailableQuoteAtoms++
	cp.Orders[0].Order.OpenQty++
	cp.Book.Bids[0].Orders[0].AccountID = "changed"
	cp.Trades[0].QuantityLots++
	for i := range cp.Results {
		if cp.Results[i].Command.Place != nil {
			cp.Results[i].Command.Place.PriceTicks++
		} else {
			cp.Results[i].Command.Cancel.ClientOrderID = "changed"
		}
		if cp.Results[i].Response.Order != nil {
			cp.Results[i].Response.Order.OpenQty++
		}
	}
	equal(t, e.State(), want)
	equal(t, restored.State(), want)
	apply(t, e, place("b", "later", d.Sell, 97, 1))
	equal(t, restored.State(), want)
}

func TestCheckpointCopyCanonicalizesAfterFurtherTrading(t *testing.T) {
	e := newEngine(t, config())
	for _, command := range checkpointCommands() {
		apply(t, e, command)
	}
	want, seq := e.Capture(), e.AppliedSeq()
	copy := e.CopyCheckpoint()
	// Scramble only the collections whose canonical order is deferred. FIFO and
	// trade order must survive unchanged, including equal-price order priority.
	slices.Reverse(copy.Accounts)
	slices.Reverse(copy.Orders)
	slices.Reverse(copy.Results)
	apply(t, e, place("b", "later-fill", d.Sell, 97, 1))
	apply(t, e, cancel("a", "later-cancel", "future"))
	engine.CanonicalizeCheckpoint(&copy)
	equal(t, copy, want)
	restored, err := engine.Restore(runID, config(), seq, copy)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, restored.Capture(), want)
	// Restore must still reject unsorted stored state rather than repairing it.
	slices.Reverse(copy.Results)
	if _, err := engine.Restore(runID, config(), seq, copy); err == nil {
		t.Fatal("recovery silently canonicalized corrupted input")
	}
}

func orderIndex(cp *d.Checkpoint, id string) int {
	for i, o := range cp.Orders {
		if o.Order.ClientOrderID == id {
			return i
		}
	}
	panic("test order not found")
}
func resultIndex(cp *d.Checkpoint, id string) int {
	for i, r := range cp.Results {
		if r.Response.RequestID == id {
			return i
		}
	}
	panic("test result not found")
}

func TestCheckpointRejectsCorruptionInsteadOfRepairingIt(t *testing.T) {
	e := newEngine(t, config())
	for _, c := range checkpointCommands() {
		apply(t, e, c)
	}
	mutations := map[string]func(*d.Checkpoint){
		"missing account":             func(c *d.Checkpoint) { c.Accounts = c.Accounts[:2] },
		"duplicate account":           func(c *d.Checkpoint) { c.Accounts[1].AccountID = c.Accounts[0].AccountID },
		"account order":               func(c *d.Checkpoint) { c.Accounts[0], c.Accounts[1] = c.Accounts[1], c.Accounts[0] },
		"negative balance":            func(c *d.Checkpoint) { c.Accounts[0].AvailableBaseLots = -1 },
		"overflow balance":            func(c *d.Checkpoint) { c.Accounts[0].ReservedQuoteAtoms = math.MaxInt64 },
		"wrong total":                 func(c *d.Checkpoint) { c.Accounts[0].AvailableQuoteAtoms++ },
		"wrong recipient conserved":   func(c *d.Checkpoint) { c.Accounts[0].AvailableQuoteAtoms++; c.Accounts[1].AvailableQuoteAtoms-- },
		"wrong reservation conserved": func(c *d.Checkpoint) { c.Accounts[0].ReservedQuoteAtoms++; c.Accounts[0].AvailableQuoteAtoms-- },
		"missing order":               func(c *d.Checkpoint) { c.Orders = c.Orders[1:] },
		"order sort":                  func(c *d.Checkpoint) { c.Orders[0], c.Orders[1] = c.Orders[1], c.Orders[0] },
		"unknown order account":       func(c *d.Checkpoint) { c.Orders[len(c.Orders)-1].AccountID = "unknown" },
		"order side":                  func(c *d.Checkpoint) { c.Orders[0].Order.Side = "BAD" },
		"order symbol":                func(c *d.Checkpoint) { c.Orders[0].Order.Symbol = "BAD" },
		"order quantity":              func(c *d.Checkpoint) { c.Orders[0].Order.OpenQty++ },
		"order negative execution":    func(c *d.Checkpoint) { c.Orders[0].Order.ExecutedQty = -1 },
		"order future update":         func(c *d.Checkpoint) { c.Orders[0].Order.UpdatedSeq = 100 },
		"order status":                func(c *d.Checkpoint) { c.Orders[0].Order.Status = "OTHER" },
		"order update provenance":     func(c *d.Checkpoint) { i := orderIndex(c, "future"); c.Orders[i].Order.UpdatedSeq++ },
		"missing active ref":          func(c *d.Checkpoint) { c.Book.Bids = c.Book.Bids[1:] },
		"duplicate active ref":        func(c *d.Checkpoint) { c.Book.Bids[0].Orders = append(c.Book.Bids[0].Orders, c.Book.Bids[0].Orders[0]) },
		"empty level":                 func(c *d.Checkpoint) { c.Book.Bids[0].Orders = nil },
		"wrong level price":           func(c *d.Checkpoint) { c.Book.Bids[0].PriceTicks++ },
		"wrong level side": func(c *d.Checkpoint) {
			c.Book.Asks = append(c.Book.Asks, c.Book.Bids[0])
			c.Book.Bids = c.Book.Bids[1:]
		},
		"price sorting":          func(c *d.Checkpoint) { c.Book.Bids[0], c.Book.Bids[1] = c.Book.Bids[1], c.Book.Bids[0] },
		"unknown reference":      func(c *d.Checkpoint) { c.Book.Bids[0].Orders[0].ClientOrderID = "unknown" },
		"missing trade":          func(c *d.Checkpoint) { c.Trades = c.Trades[1:] },
		"trade identity":         func(c *d.Checkpoint) { c.Trades[0].TradeID = "changed" },
		"trade price":            func(c *d.Checkpoint) { c.Trades[0].PriceTicks++ },
		"trade taker side":       func(c *d.Checkpoint) { c.Trades[0].TakerSide = d.Sell },
		"trade order":            func(c *d.Checkpoint) { c.Trades[0], c.Trades[1] = c.Trades[1], c.Trades[0] },
		"missing result":         func(c *d.Checkpoint) { c.Results = c.Results[1:] },
		"result sorting":         func(c *d.Checkpoint) { c.Results[0], c.Results[1] = c.Results[1], c.Results[0] },
		"duplicate sequence":     func(c *d.Checkpoint) { c.Results[0].Response.CommandSeq = c.Results[1].Response.CommandSeq },
		"response wrong account": func(c *d.Checkpoint) { c.Results[0].Response.AccountID = "unknown" },
		"response wrong run":     func(c *d.Checkpoint) { c.Results[0].Response.RunID = "00000000-0000-4000-8000-000000000001" },
		"response wrong version": func(c *d.Checkpoint) { c.Results[0].Response.SchemaVersion = 2 },
		"response code":          func(c *d.Checkpoint) { c.Results[0].Response.Code = "OTHER" },
		"response trade count":   func(c *d.Checkpoint) { c.Results[0].Response.TradeCount = 1 },
		"place payload missing":  func(c *d.Checkpoint) { i := resultIndex(c, "fill"); c.Results[i].Command.Place = nil },
		"place payload mixed": func(c *d.Checkpoint) {
			i := resultIndex(c, "fill")
			c.Results[i].Command.Cancel = &d.Cancel{CancelRequestID: "c", ClientOrderID: "fill"}
		},
		"place input price":            func(c *d.Checkpoint) { i := resultIndex(c, "fill"); c.Results[i].Command.Place.PriceTicks++ },
		"place initial execution":      func(c *d.Checkpoint) { i := resultIndex(c, "fill"); c.Results[i].Response.Order.ExecutedQuoteAtoms++ },
		"place missing response order": func(c *d.Checkpoint) { i := resultIndex(c, "fill"); c.Results[i].Response.Order = nil },
		"initial rejection changed":    func(c *d.Checkpoint) { i := resultIndex(c, "rejected"); c.Results[i].Response.Code = d.Accepted },
		"terminal initial changed":     func(c *d.Checkpoint) { i := resultIndex(c, "stp"); c.Results[i].Response.Order.UpdatedSeq++ },
		"cancel response changed":      func(c *d.Checkpoint) { i := resultIndex(c, "cancel-rest"); c.Results[i].Response.Order.CancelledQty-- },
		"cancel code changed": func(c *d.Checkpoint) {
			i := resultIndex(c, "cancel-rest")
			c.Results[i].Response.Code = d.AlreadyCancelled
		},
		"missing-target response changed": func(c *d.Checkpoint) {
			i := resultIndex(c, "before-create")
			o := c.Orders[0].Order
			c.Results[i].Response.Order = &o
		},
		"null trades": func(c *d.Checkpoint) { c.Trades = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			cp := e.Capture()
			mutate(&cp)
			if got, err := engine.Restore(runID, config(), e.AppliedSeq(), cp); err == nil || got != nil {
				t.Fatalf("corruption accepted: %v", err)
			}
		})
	}
	for _, seq := range []d.Int{-1, 0, e.AppliedSeq() - 1, e.AppliedSeq() + 1, math.MaxInt64} {
		if _, err := engine.Restore(runID, config(), seq, e.Capture()); err == nil {
			t.Fatalf("sequence %d accepted", seq)
		}
	}
	if _, err := engine.Restore("invalid", config(), e.AppliedSeq(), e.Capture()); err == nil {
		t.Fatal("invalid run accepted")
	}
}

func TestCheckpointPreservesAndValidatesFIFO(t *testing.T) {
	for _, side := range []d.Side{d.Buy, d.Sell} {
		e := newEngine(t, config())
		for _, id := range []string{"first", "second", "third"} {
			apply(t, e, place("b", id, side, 100, 2))
		}
		r := restore(t, e, config())
		taker := d.Buy
		if side == d.Buy {
			taker = d.Sell
		}
		equal(t, apply(t, r, place("a", "taker", taker, 100, 3)), apply(t, e, place("a", "taker", taker, 100, 3)))
		equal(t, r.State(), e.State())
		cp := e.Capture()
		levels := cp.Book.Bids
		if side == d.Sell {
			levels = cp.Book.Asks
		}
		levels[0].Orders[0], levels[0].Orders[1] = levels[0].Orders[1], levels[0].Orders[0]
		if _, err := engine.Restore(runID, config(), e.AppliedSeq(), cp); err == nil {
			t.Fatal("reversed FIFO repaired silently")
		}
	}
}

func TestCheckpointRejectsTradeAfterCancellation(t *testing.T) {
	e := newEngine(t, config())
	apply(t, e, place("b", "maker", d.Sell, 100, 2))
	apply(t, e, place("a", "taker", d.Buy, 100, 1))
	apply(t, e, cancel("b", "cancel-maker", "maker"))
	cp := e.Capture()
	// Preserve totals and all command/result identities, but move the fill
	// after the maker's cancellation. This cannot be a valid command history.
	cp.Orders[orderIndex(&cp, "maker")].Order.UpdatedSeq = 2
	o := &cp.Orders[orderIndex(&cp, "taker")].Order
	o.CreatedSeq, o.UpdatedSeq = 3, 3
	tr := &cp.Trades[0]
	tr.CommandSeq, tr.TradeID = 3, runID+":3:1"
	r := &cp.Results[resultIndex(&cp, "cancel-maker")].Response
	r.CommandSeq, r.Order.UpdatedSeq = 2, 2
	r = &cp.Results[resultIndex(&cp, "taker")].Response
	r.CommandSeq, r.Order.CreatedSeq, r.Order.UpdatedSeq = 3, 3, 3
	if _, err := engine.Restore(runID, config(), 3, cp); err == nil {
		t.Fatal("trade after cancellation accepted")
	}
}
