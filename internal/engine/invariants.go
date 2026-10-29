package engine

import (
	"fmt"

	d "github.com/gaon000/exchange/internal/domain"
)

// CheckInvariants audits supply, reservations, book membership, FIFO, executions,
// and result sequence coverage. It is an O(state size) diagnostic, not a hot-path
// operation. An error requires discarding the engine; it never repairs state.
func (e *Engine) CheckInvariants() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invariant audit: %v", r)
		}
	}()
	bad := func(message string) error { return fmt.Errorf("engine invariant: %s", message) }
	var base, quote d.Int
	for _, a := range e.accounts {
		base = add(base, add(a.Base.Available, a.Base.Reserved))
		quote = add(quote, add(a.Quote.Available, a.Quote.Reserved))
	}
	if base != e.baseSupply || quote != e.quoteSupply {
		return bad("supply changed")
	}
	seen := make(map[d.OrderRef]bool)
	for _, b := range []*book{&e.bids, &e.asks} {
		if len(b.prices) != len(b.levels) {
			return bad("price index size")
		}
		for i, p := range b.prices {
			if i > 0 && p <= b.prices[i-1] {
				return bad("price sorting")
			}
			level := b.levels[p]
			if level == nil || level.Len() == 0 {
				return bad("empty level")
			}
			var seq d.Int
			for n := level.Front(); n != nil; n = n.Next() {
				x := n.Value.(*entry)
				if e.orders[x.ref] != x || seen[x.ref] || x.element != n || !x.order.Active() || x.order.Side != b.side || x.order.PriceTicks != p || x.order.CreatedSeq <= seq {
					return bad("book membership/FIFO")
				}
				seen[x.ref] = true
				seq = x.order.CreatedSeq
			}
		}
	}
	if bid, ask := e.bids.best(), e.asks.best(); bid != nil && ask != nil && bid.order.PriceTicks >= ask.order.PriceTicks {
		return bad("crossed book")
	}
	reserved := make(map[string]d.Balances)
	type execution struct{ qty, money d.Int }
	executed := make(map[d.OrderRef]execution)
	var prevSeq, prevIndex d.Int
	for _, tr := range e.trades {
		maker, taker := e.orders[tr.Maker], e.orders[tr.Taker]
		if maker == nil || taker == nil || tr.Maker.AccountID == tr.Taker.AccountID || maker.order.Side == taker.order.Side || tr.TakerSide != taker.order.Side || tr.PriceTicks != maker.order.PriceTicks || tr.QuantityLots <= 0 || tr.QuantityLots > d.MaxQuantityLots || tr.CommandSeq != taker.order.CreatedSeq || maker.order.CreatedSeq >= tr.CommandSeq {
			return bad("trade counterparties/price/quantity")
		}
		if tr.CommandSeq < prevSeq || tr.CommandSeq > e.applied || tr.MatchIndex < 1 || tr.CommandSeq == prevSeq && tr.MatchIndex != prevIndex+1 || tr.CommandSeq != prevSeq && tr.MatchIndex != 1 {
			return bad("trade sequence")
		}
		if tr.TradeID != fmt.Sprintf("%s:%s:%s", e.runID, tr.CommandSeq, tr.MatchIndex) {
			return bad("trade ID")
		}
		prevSeq, prevIndex = tr.CommandSeq, tr.MatchIndex
		for _, ref := range []d.OrderRef{tr.Maker, tr.Taker} {
			o := e.orders[ref].order
			if o.Side == d.Buy && tr.PriceTicks > o.PriceTicks || o.Side == d.Sell && tr.PriceTicks < o.PriceTicks {
				return bad("limit violated")
			}
			x := executed[ref]
			x.qty = add(x.qty, tr.QuantityLots)
			x.money = add(x.money, mul(tr.PriceTicks, tr.QuantityLots))
			executed[ref] = x
		}
	}
	for ref, x := range e.orders {
		o := x.order
		if _, ok := e.accounts[ref.AccountID]; !ok {
			return bad("order owner")
		}
		if ref != x.ref || ref.ClientOrderID != o.ClientOrderID || o.PriceTicks < 1 || o.PriceTicks > d.MaxPriceTicks || o.OriginalQty < 1 || o.OriginalQty > d.MaxQuantityLots || o.CreatedSeq < 1 || o.CreatedSeq > o.UpdatedSeq || o.UpdatedSeq > e.applied {
			return bad("order identity/bounds")
		}
		if o.Status == d.Rejected {
			if o.OpenQty != 0 || o.ExecutedQty != 0 || o.CancelledQty != 0 || o.Reason != d.InsufficientFunds {
				return bad("rejected quantity/reason")
			}
		} else if add(add(o.OpenQty, o.ExecutedQty), o.CancelledQty) != o.OriginalQty {
			return bad("order quantity conservation")
		}
		switch o.Status {
		case d.Open:
			if o.OpenQty == 0 || o.ExecutedQty != 0 || o.CancelledQty != 0 || o.Reason != "" {
				return bad("open status")
			}
		case d.PartiallyFilled:
			if o.OpenQty == 0 || o.ExecutedQty == 0 || o.CancelledQty != 0 || o.Reason != "" {
				return bad("partial status")
			}
		case d.Filled:
			if o.ExecutedQty != o.OriginalQty || o.OpenQty != 0 || o.CancelledQty != 0 || o.Reason != "" {
				return bad("filled status")
			}
		case d.Cancelled:
			if o.OpenQty != 0 || o.CancelledQty == 0 || (o.Reason != d.UserCancel && o.Reason != d.SelfTradePrevention) {
				return bad("cancelled status")
			}
		case d.Rejected:
		default:
			return bad("unknown status")
		}
		if seen[ref] != o.Active() || o.Active() != (x.element != nil) {
			return bad("active order membership")
		}
		if ex := executed[ref]; ex.qty != o.ExecutedQty || ex.money != o.ExecutedQuoteAtoms {
			return bad("execution totals")
		}
		if o.Active() {
			r := reserved[ref.AccountID]
			if o.Side == d.Buy {
				r.Quote.Reserved = add(r.Quote.Reserved, mul(o.PriceTicks, o.OpenQty))
			} else {
				r.Base.Reserved = add(r.Base.Reserved, o.OpenQty)
			}
			reserved[ref.AccountID] = r
		}
	}
	for id, a := range e.accounts {
		if r := reserved[id]; r.Base.Reserved != a.Base.Reserved || r.Quote.Reserved != a.Quote.Reserved {
			return bad("reservation totals")
		}
	}
	if d.Int(len(e.results)) != e.applied {
		return bad("result count")
	}
	sequences := make(map[d.Int]bool)
	for key, r := range e.results {
		s := r.result
		if r.command.Validate() != nil || key != r.command.Key() || key.RunID != e.runID || s.SchemaVersion != d.Version || s.RunID != key.RunID || s.AccountID != key.AccountID || s.Kind != key.Kind || s.RequestID != key.RequestID || s.CommandSeq < 1 || s.CommandSeq > e.applied || sequences[s.CommandSeq] {
			return bad("initial result identity/sequence")
		}
		sequences[s.CommandSeq] = true
	}
	return nil
}
