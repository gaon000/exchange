// Package engine implements the v1 deterministic, in-memory matching engine.
// All methods require a single owner; it performs no synchronization or I/O.
package engine

import (
	"errors"
	"fmt"
	"math"

	d "github.com/gaon000/exchange/internal/domain"
)

var (
	ErrRunMismatch    = errors.New("RUN_MISMATCH")
	ErrUnknownAccount = errors.New("UNKNOWN_ACCOUNT")
	ErrConflict       = errors.New("IDEMPOTENCY_CONFLICT")
	ErrRunLimit       = errors.New("RUN_LIMIT_REACHED")
	ErrSequence       = errors.New("non-contiguous command sequence")
)

type record struct {
	command d.Command
	result  d.Result
}

// Engine owns all mutable business state. Invalid inputs return errors before
// mutation. An internal invariant panic is fatal: discard the instance rather
// than recovering and continuing with potentially partial state.
type Engine struct {
	runID       string
	applied     d.Int
	maxCommands d.Int
	baseSupply  d.Int
	quoteSupply d.Int
	accounts    map[string]d.Balances
	orders      map[d.OrderRef]*entry
	results     map[d.Key]record
	trades      []d.Trade
	bids        book
	asks        book
}

// New validates and copies Genesis values; it does not generate run IDs or read files.
func New(runID string, config d.Config) (*Engine, error) {
	if !d.ValidRunID(runID) {
		return nil, d.ErrInvalid
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{runID: runID, maxCommands: config.MaxCommands, accounts: make(map[string]d.Balances), orders: make(map[d.OrderRef]*entry), results: make(map[d.Key]record), bids: newBook(d.Buy), asks: newBook(d.Sell), trades: make([]d.Trade, 0)}
	for _, a := range config.Accounts {
		e.accounts[a.AccountID] = d.Balances{Base: d.Balance{Available: a.BaseLots}, Quote: d.Balance{Available: a.QuoteAtoms}}
		e.baseSupply = add(e.baseSupply, a.BaseLots)
		e.quoteSupply = add(e.quoteSupply, a.QuoteAtoms)
	}
	return e, nil
}

// AppliedSeq is the last fully applied unique command. Genesis has sequence zero.
func (e *Engine) AppliedSeq() d.Int { return e.applied }

// Lookup validates immutable input and returns a copy of an existing result.
// A false result with nil error is eligible for a new sequence, not a balance
// approval. The future runtime must call this before appending a new WAL record.
func (e *Engine) Lookup(c d.Command) (d.Result, bool, error) {
	if err := c.Validate(); err != nil {
		return d.Result{}, false, err
	}
	if c.RunID != e.runID {
		return d.Result{}, false, ErrRunMismatch
	}
	if _, ok := e.accounts[c.AccountID]; !ok {
		return d.Result{}, false, ErrUnknownAccount
	}
	if r, ok := e.results[c.Key()]; ok {
		if r.command != c {
			return d.Result{}, false, ErrConflict
		}
		return r.result.Clone(), true, nil
	}
	if e.applied >= e.maxCommands {
		return d.Result{}, false, ErrRunLimit
	}
	return d.Result{}, false, nil
}

// Apply applies a new command at exactly AppliedSeq()+1. Identical retries return
// the original result and ignore seq without consuming it. Runtime/WAL replay
// must not append duplicate records: this API does not validate log structure.
// Business rejections consume a sequence; malformed input and conflicts do not.
func (e *Engine) Apply(seq d.Int, c d.Command) (d.Result, error) {
	if r, found, err := e.Lookup(c); err != nil || found {
		return r, err
	}
	if e.applied == math.MaxInt64 || seq != e.applied+1 {
		return d.Result{}, ErrSequence
	}
	r := d.Result{SchemaVersion: d.Version, RunID: e.runID, AccountID: c.AccountID, Kind: c.Kind, RequestID: c.Key().RequestID, CommandSeq: seq}
	if c.Kind == d.PlaceOrder {
		before := len(e.trades)
		o, code := e.place(seq, c)
		r.Order, r.Code, r.TradeCount = &o, code, d.Int(len(e.trades)-before)
	} else {
		r.Order, r.Code = e.cancel(seq, c)
	}
	e.results[c.Key()] = record{c, r.Clone()}
	e.applied = seq
	return r, nil
}

func (e *Engine) side(s d.Side) *book {
	if s == d.Buy {
		return &e.bids
	}
	return &e.asks
}

func (e *Engine) place(seq d.Int, c d.Command) (d.Order, d.Code) {
	p := c.Place
	x := &entry{ref: d.OrderRef{AccountID: c.AccountID, ClientOrderID: p.ClientOrderID}, order: d.Order{ClientOrderID: p.ClientOrderID, Symbol: p.Symbol, Side: p.Side, PriceTicks: p.PriceTicks, OriginalQty: p.QuantityLots, CreatedSeq: seq, UpdatedSeq: seq}}
	e.orders[x.ref] = x
	b := e.accounts[c.AccountID]
	need := p.QuantityLots
	if p.Side == d.Buy {
		need = mul(p.PriceTicks, p.QuantityLots)
	}
	if p.Side == d.Buy && b.Quote.Available < need || p.Side == d.Sell && b.Base.Available < need {
		x.order.Status, x.order.Reason = d.Rejected, d.InsufficientFunds
		return x.order, d.InsufficientBalance
	}
	if p.Side == d.Buy {
		b.Quote.Available = sub(b.Quote.Available, need)
		b.Quote.Reserved = add(b.Quote.Reserved, need)
	} else {
		b.Base.Available = sub(b.Base.Available, need)
		b.Base.Reserved = add(b.Base.Reserved, need)
	}
	e.accounts[c.AccountID] = b
	x.order.OpenQty, x.order.Status = p.QuantityLots, d.Open
	opposite := &e.asks
	if p.Side == d.Sell {
		opposite = &e.bids
	}
	var index d.Int
	for x.order.OpenQty > 0 {
		maker := opposite.best()
		if maker == nil || p.Side == d.Buy && p.PriceTicks < maker.order.PriceTicks || p.Side == d.Sell && p.PriceTicks > maker.order.PriceTicks {
			break
		}
		if maker.ref.AccountID == x.ref.AccountID {
			e.release(seq, x, d.SelfTradePrevention)
			break
		}
		index = add(index, 1)
		e.fill(seq, index, maker, x)
		if maker.order.OpenQty == 0 {
			opposite.remove(maker)
		}
	}
	if x.order.OpenQty > 0 {
		e.side(p.Side).add(x)
	}
	return x.order, d.Accepted
}

func (e *Engine) fill(seq, index d.Int, maker, taker *entry) {
	qty := min(maker.order.OpenQty, taker.order.OpenQty)
	price := maker.order.PriceTicks
	money := mul(price, qty)
	buyer, seller := maker, taker
	if taker.order.Side == d.Buy {
		buyer, seller = taker, maker
	}
	buy, sell := e.accounts[buyer.ref.AccountID], e.accounts[seller.ref.AccountID]
	reserved := mul(buyer.order.PriceTicks, qty)
	buy.Quote.Reserved = sub(buy.Quote.Reserved, reserved)
	buy.Quote.Available = add(buy.Quote.Available, sub(reserved, money))
	buy.Base.Available = add(buy.Base.Available, qty)
	sell.Base.Reserved = sub(sell.Base.Reserved, qty)
	sell.Quote.Available = add(sell.Quote.Available, money)
	e.accounts[buyer.ref.AccountID], e.accounts[seller.ref.AccountID] = buy, sell
	for _, x := range []*entry{maker, taker} {
		x.order.OpenQty = sub(x.order.OpenQty, qty)
		x.order.ExecutedQty = add(x.order.ExecutedQty, qty)
		x.order.ExecutedQuoteAtoms = add(x.order.ExecutedQuoteAtoms, money)
		x.order.UpdatedSeq = seq
		x.order.Status = d.PartiallyFilled
		if x.order.OpenQty == 0 {
			x.order.Status = d.Filled
		}
	}
	e.trades = append(e.trades, d.Trade{TradeID: fmt.Sprintf("%s:%s:%s", e.runID, seq, index), CommandSeq: seq, MatchIndex: index, Maker: maker.ref, Taker: taker.ref, TakerSide: taker.order.Side, PriceTicks: price, QuantityLots: qty})
}

func (e *Engine) release(seq d.Int, x *entry, reason d.Reason) {
	b := e.accounts[x.ref.AccountID]
	if x.order.Side == d.Buy {
		n := mul(x.order.PriceTicks, x.order.OpenQty)
		b.Quote.Reserved = sub(b.Quote.Reserved, n)
		b.Quote.Available = add(b.Quote.Available, n)
	} else {
		b.Base.Reserved = sub(b.Base.Reserved, x.order.OpenQty)
		b.Base.Available = add(b.Base.Available, x.order.OpenQty)
	}
	e.accounts[x.ref.AccountID] = b
	x.order.CancelledQty = add(x.order.CancelledQty, x.order.OpenQty)
	x.order.OpenQty = 0
	x.order.Status, x.order.Reason, x.order.UpdatedSeq = d.Cancelled, reason, seq
}

func (e *Engine) cancel(seq d.Int, c d.Command) (*d.Order, d.Code) {
	x := e.orders[d.OrderRef{AccountID: c.AccountID, ClientOrderID: c.Cancel.ClientOrderID}]
	if x == nil || x.order.Status == d.Rejected {
		return nil, d.OrderNotFound
	}
	code := d.CancelSucceeded
	switch x.order.Status {
	case d.Filled:
		code = d.AlreadyFilled
	case d.Cancelled:
		code = d.AlreadyCancelled
	default:
		e.side(x.order.Side).remove(x)
		e.release(seq, x, d.UserCancel)
	}
	o := x.order
	return &o, code
}

// Arithmetic failures indicate a bug or corrupted internal state, never a
// business rejection. Validated supply and per-order bounds make them unreachable
// for valid state, but checks remain at every balance and quantity mutation.
func add(a, b d.Int) d.Int {
	n, err := d.Add(a, b)
	if err != nil {
		panic(fmt.Errorf("engine invariant add: %w", err))
	}
	return n
}
func sub(a, b d.Int) d.Int {
	n, err := d.Sub(a, b)
	if err != nil {
		panic(fmt.Errorf("engine invariant sub: %w", err))
	}
	return n
}
func mul(a, b d.Int) d.Int {
	n, err := d.Mul(a, b)
	if err != nil {
		panic(fmt.Errorf("engine invariant mul: %w", err))
	}
	return n
}
