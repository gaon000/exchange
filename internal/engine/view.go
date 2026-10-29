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
