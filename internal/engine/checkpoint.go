package engine

import (
	"fmt"
	"sort"

	d "github.com/gaon000/exchange/internal/domain"
)

// Capture returns a detached checkpoint in canonical storage order.
func (e *Engine) Capture() d.Checkpoint {
	c := e.CopyCheckpoint()
	CanonicalizeCheckpoint(&c)
	return c
}

// CopyCheckpoint copies every persisted field at the owner's command boundary.
// Accounts, Orders, and Results retain unspecified map iteration order. Their
// detached slices may be canonicalized by another goroutine while the engine
// continues processing commands. Only that goroutine may access the copy until
// canonicalization finishes; it must finish before publication or Restore.
func (e *Engine) CopyCheckpoint() d.Checkpoint {
	c := d.Checkpoint{Accounts: make([]d.CheckpointAccount, 0, len(e.accounts)), Orders: make([]d.CheckpointOrder, 0, len(e.orders)), Book: d.CheckpointBook{Bids: captureBook(&e.bids), Asks: captureBook(&e.asks)}, Trades: append([]d.Trade{}, e.trades...), Results: make([]d.CheckpointResult, 0, len(e.results))}
	for id, b := range e.accounts {
		c.Accounts = append(c.Accounts, d.CheckpointAccount{AccountID: id, AvailableBaseLots: b.Base.Available, ReservedBaseLots: b.Base.Reserved, AvailableQuoteAtoms: b.Quote.Available, ReservedQuoteAtoms: b.Quote.Reserved})
	}
	for ref, x := range e.orders {
		c.Orders = append(c.Orders, d.CheckpointOrder{AccountID: ref.AccountID, Order: x.order})
	}
	for _, r := range e.results {
		command := d.CheckpointCommand{AccountID: r.command.AccountID, Kind: r.command.Kind}
		if r.command.Kind == d.PlaceOrder {
			p := r.command.Place
			command.Place = &p
		} else {
			c := r.command.Cancel
			command.Cancel = &c
		}
		c.Results = append(c.Results, d.CheckpointResult{Command: command, Response: r.result.Clone()})
	}
	return c
}

// CanonicalizeCheckpoint sorts the three unordered collections of an exclusively
// owned copy in place. It neither changes FIFO/trade order nor validates or
// repairs state. Recovery must validate stored order rather than call this.
func CanonicalizeCheckpoint(c *d.Checkpoint) {
	sort.Slice(c.Accounts, func(i, j int) bool { return c.Accounts[i].AccountID < c.Accounts[j].AccountID })
	sort.Slice(c.Orders, func(i, j int) bool { return orderLess(c.Orders[i], c.Orders[j]) })
	sort.Slice(c.Results, func(i, j int) bool { return resultLess(c.Results[i], c.Results[j]) })
}

func orderLess(a, b d.CheckpointOrder) bool {
	if a.AccountID != b.AccountID {
		return a.AccountID < b.AccountID
	}
	return a.Order.ClientOrderID < b.Order.ClientOrderID
}
func resultLess(a, b d.CheckpointResult) bool {
	if a.Command.AccountID != b.Command.AccountID {
		return a.Command.AccountID < b.Command.AccountID
	}
	if a.Command.Kind != b.Command.Kind {
		return a.Command.Kind < b.Command.Kind
	}
	return a.Response.RequestID < b.Response.RequestID
}
func captureBook(b *book) []d.CheckpointLevel {
	out := make([]d.CheckpointLevel, 0, len(b.prices))
	for i := range len(b.prices) {
		j := i
		if b.side == d.Buy {
			j = len(b.prices) - 1 - i
		}
		price := b.prices[j]
		level := b.levels[price]
		refs := make([]d.OrderRef, 0, level.Len())
		for n := level.Front(); n != nil; n = n.Next() {
			refs = append(refs, n.Value.(*entry).ref)
		}
		out = append(out, d.CheckpointLevel{PriceTicks: price, Orders: refs})
	}
	return out
}

// Restore rebuilds the engine directly from a checkpoint and validates its
// order/FIFO, supply, reservation, trade, command, and initial-result relations.
// It does not match historical commands again. Recovery must additionally check
// these saved command inputs against the retained WAL before publishing state.
func Restore(runID string, config d.Config, seq d.Int, c d.Checkpoint) (_ *Engine, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("invalid checkpoint arithmetic/state: %v", p)
		}
	}()
	e, err := New(runID, config)
	if err != nil {
		return nil, err
	}
	bad := func(reason string) (*Engine, error) { return nil, fmt.Errorf("invalid checkpoint: %s", reason) }
	if seq < 0 || seq > config.MaxCommands || d.Int(len(c.Results)) != seq || c.Accounts == nil || c.Orders == nil || c.Book.Bids == nil || c.Book.Asks == nil || c.Trades == nil || c.Results == nil {
		return bad("sequence/arrays")
	}
	if len(c.Accounts) != len(config.Accounts) || d.Int(len(c.Orders)) > seq || d.Int(len(c.Trades)) > seq {
		return bad("state counts")
	}
	e.applied = seq
	for i, a := range c.Accounts {
		if _, ok := e.accounts[a.AccountID]; !ok || i > 0 && c.Accounts[i-1].AccountID >= a.AccountID {
			return bad("accounts missing/duplicated/out of order")
		}
		e.accounts[a.AccountID] = d.Balances{Base: d.Balance{Available: a.AvailableBaseLots, Reserved: a.ReservedBaseLots}, Quote: d.Balance{Available: a.AvailableQuoteAtoms, Reserved: a.ReservedQuoteAtoms}}
	}
	for i, o := range c.Orders {
		if i > 0 && !orderLess(c.Orders[i-1], o) || !validOrder(o.Order, seq) {
			return bad("order fields or sorting")
		}
		ref := d.OrderRef{AccountID: o.AccountID, ClientOrderID: o.Order.ClientOrderID}
		e.orders[ref] = &entry{ref: ref, order: o.Order}
	}
	for _, side := range []struct {
		levels []d.CheckpointLevel
		book   *book
	}{{c.Book.Bids, &e.bids}, {c.Book.Asks, &e.asks}} {
		var lastPrice d.Int
		for i, level := range side.levels {
			if level.PriceTicks < 1 || level.PriceTicks > d.MaxPriceTicks || len(level.Orders) == 0 || i > 0 && (side.book.side == d.Buy && lastPrice <= level.PriceTicks || side.book.side == d.Sell && lastPrice >= level.PriceTicks) {
				return bad("book level bounds/order")
			}
			lastPrice = level.PriceTicks
			var lastSeq d.Int
			for _, ref := range level.Orders {
				x := e.orders[ref]
				if x == nil || !x.order.Active() || x.order.Side != side.book.side || x.order.PriceTicks != level.PriceTicks || x.element != nil || x.order.CreatedSeq <= lastSeq {
					return bad("book references or FIFO")
				}
				lastSeq = x.order.CreatedSeq
				side.book.add(x)
			}
		}
	}
	e.trades = append([]d.Trade{}, c.Trades...)
	for i, saved := range c.Results {
		if i > 0 && !resultLess(c.Results[i-1], saved) {
			return bad("result sorting/duplicates")
		}
		command, err := saved.Command.Command(runID)
		if err != nil {
			return bad("saved command union")
		}
		if _, known := e.accounts[command.AccountID]; !known {
			return bad("result account")
		}
		key := command.Key()
		if _, duplicate := e.results[key]; duplicate {
			return bad("duplicate request key")
		}
		e.results[key] = record{command: command, result: saved.Response.Clone()}
	}
	if err := e.CheckInvariants(); err != nil {
		return nil, err
	}
	if err := e.checkCheckpointRelations(config); err != nil {
		return nil, err
	}
	return e, nil
}

func validOrder(o d.Order, through d.Int) bool {
	if !d.ValidID(o.ClientOrderID) || o.Symbol != d.Symbol || (o.Side != d.Buy && o.Side != d.Sell) || o.PriceTicks < 1 || o.PriceTicks > d.MaxPriceTicks || o.OriginalQty < 1 || o.OriginalQty > d.MaxQuantityLots || o.ExecutedQty < 0 || o.OpenQty < 0 || o.CancelledQty < 0 || o.ExecutedQuoteAtoms < 0 || o.CreatedSeq < 1 || o.CreatedSeq > o.UpdatedSeq || o.UpdatedSeq > through {
		return false
	}
	if o.Status == d.Rejected {
		return o.ExecutedQty == 0 && o.OpenQty == 0 && o.CancelledQty == 0 && o.ExecutedQuoteAtoms == 0 && o.Reason == d.InsufficientFunds
	}
	qty, err := d.Add(o.ExecutedQty, o.OpenQty)
	if err != nil {
		return false
	}
	qty, err = d.Add(qty, o.CancelledQty)
	if err != nil || qty != o.OriginalQty {
		return false
	}
	switch o.Status {
	case d.Open:
		return o.ExecutedQty == 0 && o.OpenQty > 0 && o.CancelledQty == 0 && o.Reason == ""
	case d.PartiallyFilled:
		return o.ExecutedQty > 0 && o.OpenQty > 0 && o.CancelledQty == 0 && o.Reason == ""
	case d.Filled:
		return o.ExecutedQty == o.OriginalQty && o.OpenQty == 0 && o.CancelledQty == 0 && o.Reason == ""
	case d.Cancelled:
		return o.CancelledQty > 0 && o.OpenQty == 0 && (o.Reason == d.UserCancel || o.Reason == d.SelfTradePrevention)
	default:
		return false
	}
}

func sameOrderInput(o d.Order, p d.Place) bool {
	return o.ClientOrderID == p.ClientOrderID && o.Symbol == p.Symbol && o.Side == p.Side && o.PriceTicks == p.PriceTicks && o.OriginalQty == p.QuantityLots
}

func (e *Engine) checkCheckpointRelations(config d.Config) error {
	bad := func(reason string) error { return fmt.Errorf("checkpoint relation: %s", reason) }
	// Reconstruct total holdings from Genesis and the transfer ledger. Checking
	// only global conservation would miss a transfer to the wrong account.
	holdings := make(map[string]d.Balances, len(config.Accounts))
	for _, a := range config.Accounts {
		holdings[a.AccountID] = d.Balances{Base: d.Balance{Available: a.BaseLots}, Quote: d.Balance{Available: a.QuoteAtoms}}
	}
	type execution struct{ count, qty, quote d.Int }
	incoming := make(map[d.Int]execution)
	lastTrade := make(map[d.OrderRef]d.Int)
	for _, trade := range e.trades {
		if trade.TakerSide != e.orders[trade.Taker].order.Side {
			return bad("trade taker side")
		}
		lastTrade[trade.Maker], lastTrade[trade.Taker] = trade.CommandSeq, trade.CommandSeq
		buyer, seller := trade.Taker.AccountID, trade.Maker.AccountID
		if trade.TakerSide == d.Sell {
			buyer, seller = seller, buyer
		}
		b, s := holdings[buyer], holdings[seller]
		amount := mul(trade.PriceTicks, trade.QuantityLots)
		b.Quote.Available = sub(b.Quote.Available, amount)
		b.Base.Available = add(b.Base.Available, trade.QuantityLots)
		s.Base.Available = sub(s.Base.Available, trade.QuantityLots)
		s.Quote.Available = add(s.Quote.Available, amount)
		holdings[buyer], holdings[seller] = b, s
		x := incoming[trade.CommandSeq]
		x.count++
		x.qty = add(x.qty, trade.QuantityLots)
		x.quote = add(x.quote, amount)
		incoming[trade.CommandSeq] = x
	}
	for id, b := range e.accounts {
		h := holdings[id]
		if add(b.Base.Available, b.Base.Reserved) != h.Base.Available || add(b.Quote.Available, b.Quote.Reserved) != h.Quote.Available {
			return bad("account holdings do not match Genesis plus trades")
		}
	}
	placeCount := 0
	bySequence := make(map[d.Int]record, len(e.results))
	for _, saved := range e.results {
		bySequence[saved.result.CommandSeq] = saved
	}
	for _, saved := range e.results {
		c, s := saved.command, saved.result
		if s.TradeCount < 0 || s.TradeCount != incoming[s.CommandSeq].count {
			return bad("initial trade count")
		}
		if c.Kind == d.PlaceOrder {
			placeCount++
			x := e.orders[d.OrderRef{AccountID: c.AccountID, ClientOrderID: c.Place.ClientOrderID}]
			if x == nil || s.Order == nil || !sameOrderInput(x.order, c.Place) || !sameOrderInput(*s.Order, c.Place) || !validOrder(*s.Order, s.CommandSeq) || s.Order.CreatedSeq != s.CommandSeq || s.Order.UpdatedSeq != s.CommandSeq || x.order.CreatedSeq != s.CommandSeq {
				return bad("place command/order/initial result")
			}
			ex := incoming[s.CommandSeq]
			if s.Order.ExecutedQty != ex.qty || s.Order.ExecutedQuoteAtoms != ex.quote || s.Order.ExecutedQty > x.order.ExecutedQty || s.Order.CancelledQty > x.order.CancelledQty {
				return bad("initial execution totals")
			}
			if s.Order.Status == d.Rejected {
				if s.Code != d.InsufficientBalance || x.order != *s.Order {
					return bad("rejection result")
				}
			} else if s.Code != d.Accepted || s.Order.Reason == d.UserCancel {
				return bad("accepted result")
			}
			if s.Order.Status == d.Filled || s.Order.Status == d.Cancelled {
				if x.order != *s.Order {
					return bad("terminal place changed")
				}
			}
		} else {
			if s.TradeCount != 0 {
				return bad("cancel trade count")
			}
			x := e.orders[d.OrderRef{AccountID: c.AccountID, ClientOrderID: c.Cancel.ClientOrderID}]
			if s.Code == d.OrderNotFound {
				if s.Order != nil || x != nil && x.order.CreatedSeq < s.CommandSeq && x.order.Status != d.Rejected {
					return bad("not-found result")
				}
				continue
			}
			if x == nil || s.Order == nil || *s.Order != x.order || !validOrder(*s.Order, s.CommandSeq) || s.Order.CreatedSeq >= s.CommandSeq {
				return bad("cancel result order")
			}
			switch s.Code {
			case d.CancelSucceeded:
				if s.Order.Status != d.Cancelled || s.Order.Reason != d.UserCancel || s.Order.UpdatedSeq != s.CommandSeq {
					return bad("successful cancel")
				}
			case d.AlreadyCancelled:
				if s.Order.Status != d.Cancelled || s.Order.UpdatedSeq >= s.CommandSeq {
					return bad("already cancelled")
				}
			case d.AlreadyFilled:
				if s.Order.Status != d.Filled || s.Order.UpdatedSeq >= s.CommandSeq {
					return bad("already filled")
				}
			default:
				return bad("unknown cancel outcome")
			}
		}
	}
	if placeCount != len(e.orders) {
		return bad("order without place result")
	}
	for ref, x := range e.orders {
		o := x.order
		if lastTrade[ref] > o.UpdatedSeq {
			return bad("execution after order's last update")
		}
		switch {
		case o.Status == d.Cancelled && o.Reason == d.UserCancel:
			r := bySequence[o.UpdatedSeq]
			if r.command.Kind != d.CancelOrder || r.command.AccountID != ref.AccountID || r.command.Cancel.ClientOrderID != ref.ClientOrderID || r.result.Code != d.CancelSucceeded {
				return bad("cancelled order lacks cancellation provenance")
			}
		case o.Status == d.Cancelled && o.Reason == d.SelfTradePrevention:
			initial := bySequence[o.CreatedSeq].result.Order
			if o.UpdatedSeq != o.CreatedSeq || initial == nil || *initial != o {
				return bad("STP did not occur at creation")
			}
		default:
			if o.UpdatedSeq != max(o.CreatedSeq, lastTrade[ref]) {
				return bad("order update sequence does not match executions")
			}
		}
	}
	return nil
}
