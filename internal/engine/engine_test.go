package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

const runID = "11111111-1111-4111-8111-111111111111"

func config() d.Config {
	return d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 1000, QuoteAtoms: 100000}, {AccountID: "b", BaseLots: 1000, QuoteAtoms: 100000}, {AccountID: "c", BaseLots: 1000, QuoteAtoms: 100000}})
}
func newEngine(t testing.TB, c d.Config) *engine.Engine {
	t.Helper()
	e, err := engine.New(runID, c)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func place(a, id string, side d.Side, p, q d.Int) d.Command {
	return d.Command{RunID: runID, AccountID: a, Kind: d.PlaceOrder, Place: d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: p, QuantityLots: q}}
}
func cancel(a, id, target string) d.Command {
	return d.Command{RunID: runID, AccountID: a, Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: id, ClientOrderID: target}}
}
func apply(t testing.TB, e *engine.Engine, c d.Command) d.Result {
	t.Helper()
	r, err := e.Apply(e.AppliedSeq()+1, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return r
}
func equal(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestPriceFIFOAndRestingPrice(t *testing.T) {
	for _, side := range []d.Side{d.Buy, d.Sell} {
		t.Run(string(side), func(t *testing.T) {
			e := newEngine(t, config())
			makerSide := d.Sell
			prices := []d.Int{102, 100, 100}
			limit := d.Int(105)
			if side == d.Sell {
				makerSide = d.Buy
				prices = []d.Int{100, 102, 102}
				limit = 98
			}
			for i, p := range prices {
				apply(t, e, place("b", fmt.Sprint(i), makerSide, p, 2))
			}
			r := apply(t, e, place("a", "take", side, limit, 5))
			equal(t, r.Order.Status, d.Filled)
			tr := e.State().Trades
			equal(t, len(tr), 3)
			for i, id := range []string{"1", "2", "0"} {
				equal(t, tr[i].Maker.ClientOrderID, id)
				equal(t, tr[i].MatchIndex, d.Int(i+1))
				equal(t, tr[i].PriceTicks, prices[[]int{1, 2, 0}[i]])
			}
			equal(t, tr[2].QuantityLots, d.Int(1))
			equal(t, r.Order.ExecutedQuoteAtoms, prices[1]*4+prices[0])
		})
	}
}

func TestPriceImprovementAndCancel(t *testing.T) {
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 0, QuoteAtoms: 2000}, {AccountID: "b", BaseLots: 4, QuoteAtoms: 0}})
	e := newEngine(t, c)
	apply(t, e, place("b", "sell", d.Sell, 100, 4))
	r := apply(t, e, place("a", "buy", d.Buy, 105, 10))
	equal(t, r.Order.Status, d.PartiallyFilled)
	equal(t, r.Order.OpenQty, d.Int(6))
	equal(t, r.Order.ExecutedQty, d.Int(4))
	b, _ := e.Balances("a")
	equal(t, b, d.Balances{Base: d.Balance{Available: 4}, Quote: d.Balance{Available: 970, Reserved: 630}})
	r = apply(t, e, cancel("a", "cancel", "buy"))
	equal(t, r.Code, d.CancelSucceeded)
	equal(t, r.Order.CancelledQty, d.Int(6))
	b, _ = e.Balances("a")
	equal(t, b, d.Balances{Base: d.Balance{Available: 4}, Quote: d.Balance{Available: 1600}})
	other, _ := e.Balances("b")
	equal(t, other, d.Balances{Quote: d.Balance{Available: 400}})
	apply(t, e, cancel("a", "cancel-again", "buy"))
	after, _ := e.Balances("a")
	equal(t, after, b)
}

func TestSTPStopsAtOwnOrder(t *testing.T) {
	for _, side := range []d.Side{d.Buy, d.Sell} {
		t.Run(string(side), func(t *testing.T) {
			e := newEngine(t, config())
			opposite := d.Sell
			if side == d.Sell {
				opposite = d.Buy
			}
			apply(t, e, place("b", "first", opposite, 100, 2))
			self := apply(t, e, place("a", "self", opposite, 100, 3))
			apply(t, e, place("c", "behind", opposite, 100, 4))
			r := apply(t, e, place("a", "incoming", side, 100, 10))
			equal(t, r.Order.Status, d.Cancelled)
			equal(t, r.Order.Reason, d.SelfTradePrevention)
			equal(t, r.Order.ExecutedQty, d.Int(2))
			equal(t, r.Order.CancelledQty, d.Int(8))
			equal(t, r.TradeCount, d.Int(1))
			o, _ := e.Order("a", "self")
			equal(t, o, *self.Order)
			o, _ = e.Order("c", "behind")
			equal(t, o.OpenQty, d.Int(4))
		})
	}
}

func TestImmediateSTPAndMaximumReservation(t *testing.T) {
	e := newEngine(t, config())
	apply(t, e, place("a", "maker", d.Sell, 1, 1))
	r := apply(t, e, place("a", "taker", d.Buy, 1, 1))
	equal(t, r.Order.Reason, d.SelfTradePrevention)
	equal(t, r.TradeCount, d.Int(0))
	e = newEngine(t, d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 0, QuoteAtoms: 100}, {AccountID: "b", BaseLots: 10, QuoteAtoms: 0}}))
	apply(t, e, place("b", "cheap", d.Sell, 1, 10))
	r = apply(t, e, place("a", "too-much", d.Buy, 11, 10))
	equal(t, r.Code, d.InsufficientBalance)
	equal(t, r.Order.Status, d.Rejected)
	equal(t, len(e.State().Trades), 0)
	// A second order cannot spend funds reserved by the first.
	e = newEngine(t, d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 0, QuoteAtoms: 100}, {AccountID: "b", BaseLots: 10, QuoteAtoms: 0}}))
	apply(t, e, place("a", "reserve", d.Buy, 50, 2))
	r = apply(t, e, place("a", "over", d.Buy, 1, 1))
	equal(t, r.Code, d.InsufficientBalance)
}

func TestCancelFIFOAndAccountScope(t *testing.T) {
	e := newEngine(t, config())
	for _, id := range []string{"first", "middle", "last"} {
		apply(t, e, place("b", id, d.Sell, 100, 2))
	}
	equal(t, apply(t, e, cancel("a", "foreign", "middle")).Code, d.OrderNotFound)
	equal(t, apply(t, e, cancel("b", "remove", "middle")).Code, d.CancelSucceeded)
	equal(t, apply(t, e, cancel("b", "remove2", "middle")).Code, d.AlreadyCancelled)
	apply(t, e, place("a", "take", d.Buy, 100, 3))
	tr := e.State().Trades
	equal(t, tr[0].Maker.ClientOrderID, "first")
	equal(t, tr[1].Maker.ClientOrderID, "last")
	equal(t, apply(t, e, cancel("b", "filled", "first")).Code, d.AlreadyFilled)
	equal(t, apply(t, e, cancel("b", "absent", "missing")).Code, d.OrderNotFound)
	equal(t, apply(t, e, cancel("b", "partial", "last")).Order.CancelledQty, d.Int(1))
	// The same client ID on different accounts denotes different orders.
	apply(t, e, place("a", "shared", d.Buy, 90, 1))
	apply(t, e, place("b", "shared", d.Buy, 90, 1))
	a, _ := e.Order("a", "shared")
	b, _ := e.Order("b", "shared")
	if a.CreatedSeq == b.CreatedSeq {
		t.Fatal("account scope lost")
	}
}

func TestCancelFillOrdering(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			e := newEngine(t, config())
			apply(t, e, place("b", "maker", d.Sell, 100, 1))
			if cancelFirst {
				equal(t, apply(t, e, cancel("b", "c", "maker")).Code, d.CancelSucceeded)
				equal(t, apply(t, e, place("a", "take", d.Buy, 100, 1)).Order.Status, d.Open)
			} else {
				apply(t, e, place("a", "take", d.Buy, 100, 1))
				equal(t, apply(t, e, cancel("b", "c", "maker")).Code, d.AlreadyFilled)
			}
		})
	}
}

func TestRetryOriginalResultsAndIsolation(t *testing.T) {
	e := newEngine(t, config())
	c := place("a", "buy", d.Buy, 100, 2)
	first := apply(t, e, c)
	want := first.Clone()
	first.Order.OpenQty = 999
	apply(t, e, place("b", "sell", d.Sell, 100, 2))
	before := e.State()
	again := apply(t, e, c)
	equal(t, again, want)
	equal(t, e.State(), before)
	current, _ := e.Order("a", "buy")
	equal(t, current.Status, d.Filled)
	copy := e.State()
	copy.Accounts[0].Balances.Quote.Available = 0
	copy.Orders[0].Order.OpenQty = 9
	copy.Trades[0].PriceTicks = 9
	copy.Results[0].Result.Order.OpenQty = 9
	equal(t, e.State(), before)
	lookup, found, err := e.Lookup(c)
	if !found || err != nil {
		t.Fatal(err)
	}
	lookup.Order.OpenQty = 7
	equal(t, apply(t, e, c), want)
	bad := c
	bad.Place.PriceTicks++
	if _, err := e.Apply(e.AppliedSeq()+1, bad); !errors.Is(err, engine.ErrConflict) {
		t.Fatal(err)
	}
	equal(t, e.State(), before)
	cancelCmd := cancel("a", "c", "buy")
	originalCancel := apply(t, e, cancelCmd)
	equal(t, apply(t, e, cancelCmd), originalCancel)
	// Namespaces permit the cancellation request ID to equal an order ID.
	equal(t, apply(t, e, cancel("a", "buy", "buy")).Code, d.AlreadyFilled)
}

func TestRejectedRetryStaysRejected(t *testing.T) {
	e := newEngine(t, d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 2, QuoteAtoms: 0}, {AccountID: "b", BaseLots: 0, QuoteAtoms: 1000}}))
	c := place("a", "rejected", d.Buy, 100, 1)
	original := apply(t, e, c)
	equal(t, apply(t, e, cancel("a", "cancel-rejected", "rejected")).Code, d.OrderNotFound)
	apply(t, e, place("a", "sell", d.Sell, 100, 1))
	apply(t, e, place("b", "buy", d.Buy, 100, 1))
	equal(t, apply(t, e, c), original)
	equal(t, apply(t, e, place("a", "new", d.Buy, 100, 1)).Code, d.Accepted)
}

func TestInvalidInputsNeverMutate(t *testing.T) {
	e := newEngine(t, config())
	base := place("a", "valid", d.Buy, 100, 1)
	for name, change := range map[string]func(*d.Command){"negative": func(c *d.Command) { c.Place.PriceTicks = -1 }, "overflow": func(c *d.Command) { c.Place.QuantityLots = math.MaxInt64 }, "unknown account": func(c *d.Command) { c.AccountID = "missing" }, "other run": func(c *d.Command) { c.RunID = "22222222-2222-4222-8222-222222222222" }, "tag": func(c *d.Command) { c.Cancel.CancelRequestID = "hidden" }} {
		t.Run(name, func(t *testing.T) {
			before := e.State()
			c := base
			change(&c)
			if _, err := e.Apply(1, c); err == nil {
				t.Fatal("accepted")
			}
			equal(t, e.State(), before)
		})
	}
	for _, seq := range []d.Int{-1, 0, 2, math.MaxInt64} {
		before := e.State()
		if _, err := e.Apply(seq, base); !errors.Is(err, engine.ErrSequence) {
			t.Fatal(err)
		}
		equal(t, e.State(), before)
	}
	apply(t, e, base)
	if _, err := engine.New("invalid", config()); err == nil {
		t.Fatal("bad run")
	}
	bad := config()
	bad.Accounts[0].BaseLots = -1
	if _, err := engine.New(runID, bad); err == nil {
		t.Fatal("bad genesis")
	}
}

func TestLimitsAndGenesisOwnership(t *testing.T) {
	c := config()
	c.MaxCommands = 1
	e := newEngine(t, c)
	c.Accounts[0].QuoteAtoms = 0
	cmd := place("a", "one", d.Buy, 100, 1)
	first := apply(t, e, cmd)
	if _, err := e.Apply(2, place("a", "two", d.Buy, 100, 1)); !errors.Is(err, engine.ErrRunLimit) {
		t.Fatal(err)
	}
	equal(t, apply(t, e, cmd), first)
	e = newEngine(t, d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 0, QuoteAtoms: d.MaxQuoteSupplyAtoms}, {AccountID: "b", BaseLots: d.MaxBaseSupplyLots, QuoteAtoms: 0}}))
	apply(t, e, place("b", "max-sell", d.Sell, d.MaxPriceTicks, d.MaxQuantityLots))
	r := apply(t, e, place("a", "max-buy", d.Buy, d.MaxPriceTicks, d.MaxQuantityLots))
	equal(t, r.Order.ExecutedQuoteAtoms, d.MaxNotionalAtoms)
}

func TestInitialResponseJSON(t *testing.T) {
	e := newEngine(t, config())
	r := apply(t, e, place("a", "one", d.Buy, 100, 1))
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	equal(t, obj["commandSeq"], "1")
	equal(t, obj["tradeCount"], "0")
	o := obj["order"].(map[string]any)
	equal(t, o["reason"], nil)
	equal(t, o["priceTicks"], "100")
}
