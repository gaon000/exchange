package engine

import (
	"sort"

	d "github.com/gaon000/exchange/internal/domain"
)

// AccountState is an account's copied balances.
type AccountState struct {
	AccountID string
	Balances  d.Balances
}

// OwnedOrder pairs an order value with its account scope.
type OwnedOrder struct {
	Ref   d.OrderRef
	Order d.Order
}

// Outcome pairs the original command with its initial result.
type Outcome struct {
	Command d.Command
	Result  d.Result
}

// State is a deterministic, detached diagnostic view, not the snapshot file DTO.
type State struct {
	RunID      string
	AppliedSeq d.Int
	Accounts   []AccountState
	Orders     []OwnedOrder
	Bids       []Level
	Asks       []Level
	Trades     []d.Trade
	Results    []Outcome
}

// Order returns a value copy scoped to its owner, including rejected orders.
func (e *Engine) Order(accountID, clientOrderID string) (d.Order, bool) {
	x, ok := e.orders[d.OrderRef{AccountID: accountID, ClientOrderID: clientOrderID}]
	if !ok {
		return d.Order{}, false
	}
	return x.order, true
}

// Balances returns a value copy; queries must run on the engine's owner.
func (e *Engine) Balances(accountID string) (d.Balances, bool) {
	b, ok := e.accounts[accountID]
	return b, ok
}

// Result returns a detached initial outcome by key, without requiring its input.
// Runtime validates the querying account and run before calling this method.
func (e *Engine) Result(key d.Key) (d.Result, bool) {
	r, ok := e.results[key]
	return r.result.Clone(), ok
}

// Book returns up to limit aggregate levels per side, in price priority order.
func (e *Engine) Book(limit int) (bids, asks []Level, err error) {
	if limit < 1 || limit > 100 {
		return nil, nil, d.ErrInvalid
	}
	return e.bids.top(limit), e.asks.top(limit), nil
}

// TradePage returns copied trades after an exact cursor through an applied
// command boundary. Zero/zero means the first page. No newer trade enters it.
func (e *Engine) TradePage(afterSeq, afterIndex, throughSeq d.Int, limit int) ([]d.Trade, error) {
	if afterSeq < 0 || afterIndex < 0 || (afterSeq == 0) != (afterIndex == 0) || throughSeq < 0 || throughSeq > e.applied || afterSeq > throughSeq || limit < 1 || limit > 1000 {
		return nil, d.ErrInvalid
	}
	start := 0
	if afterSeq != 0 {
		start = sort.Search(len(e.trades), func(i int) bool {
			x := e.trades[i]
			return x.CommandSeq > afterSeq || x.CommandSeq == afterSeq && x.MatchIndex >= afterIndex
		})
		if start == len(e.trades) || e.trades[start].CommandSeq != afterSeq || e.trades[start].MatchIndex != afterIndex {
			return nil, d.ErrInvalid
		}
		start++
	}
	end := start
	for end < len(e.trades) && end-start < limit && e.trades[end].CommandSeq <= throughSeq {
		end++
	}
	return append([]d.Trade{}, e.trades[start:end]...), nil
}

// State copies all state in stable order for audit and replay comparisons.
// Callers may mutate the returned slices and nested results safely.
func (e *Engine) State() State {
	s := State{RunID: e.runID, AppliedSeq: e.applied, Accounts: make([]AccountState, 0, len(e.accounts)), Orders: make([]OwnedOrder, 0, len(e.orders)), Bids: e.bids.view(), Asks: e.asks.view(), Trades: append([]d.Trade{}, e.trades...), Results: make([]Outcome, 0, len(e.results))}
	for id, b := range e.accounts {
		s.Accounts = append(s.Accounts, AccountState{id, b})
	}
	for ref, x := range e.orders {
		s.Orders = append(s.Orders, OwnedOrder{ref, x.order})
	}
	for _, r := range e.results {
		s.Results = append(s.Results, Outcome{r.command, r.result.Clone()})
	}
	sort.Slice(s.Accounts, func(i, j int) bool { return s.Accounts[i].AccountID < s.Accounts[j].AccountID })
	sort.Slice(s.Orders, func(i, j int) bool {
		a, b := s.Orders[i].Ref, s.Orders[j].Ref
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.ClientOrderID < b.ClientOrderID
	})
	sort.Slice(s.Results, func(i, j int) bool { return s.Results[i].Result.CommandSeq < s.Results[j].Result.CommandSeq })
	return s
}
