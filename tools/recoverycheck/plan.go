package main

import (
	"fmt"
	"math/rand"
	"reflect"

	d "github.com/gaon000/exchange/internal/domain"
)

const maxActions = 256

type action struct {
	Kind    string               `json:"kind"`
	Command *d.CheckpointCommand `json:"command,omitempty"`
}

type plan struct {
	Version int      `json:"version"`
	Seed    int64    `json:"seed"`
	Actions []action `json:"actions"`
}

func config() d.Config {
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 100, QuoteAtoms: 100000},
		{AccountID: "bob", BaseLots: 100, QuoteAtoms: 100000}, {AccountID: "carol", BaseLots: 100, QuoteAtoms: 100000}})
	c.MaxCommands = maxActions
	return c
}

func place(account, id string, side d.Side, price, qty d.Int) action {
	return action{Kind: "command", Command: &d.CheckpointCommand{AccountID: account, Kind: d.PlaceOrder,
		Place: &d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}}}
}

func cancelOrder(account, id, target string) action {
	return action{Kind: "command", Command: &d.CheckpointCommand{AccountID: account, Kind: d.CancelOrder,
		Cancel: &d.Cancel{CancelRequestID: id, ClientOrderID: target}}}
}

func clone(a action) action {
	c := *a.Command
	if c.Place != nil {
		p := *c.Place
		c.Place = &p
	}
	if c.Cancel != nil {
		x := *c.Cancel
		c.Cancel = &x
	}
	return action{Kind: a.Kind, Command: &c}
}

func generate(seed int64, count int) (plan, error) {
	if count < 32 || count > maxActions {
		return plan{}, fmt.Errorf("actions must be 32..%d", maxActions)
	}
	// Mandatory prefix covers partial fill, both idempotency namespaces, a
	// conflict, self-trade prevention, rejection and a snapshot with a WAL tail.
	p := plan{Version: 1, Seed: seed, Actions: []action{
		place("bob", "maker", d.Sell, 100, 6), place("alice", "taker", d.Buy, 101, 2),
		place("bob", "maker", d.Sell, 100, 6), place("bob", "maker", d.Sell, 100, 7),
		cancelOrder("bob", "cancel-maker", "maker"), cancelOrder("bob", "cancel-maker", "maker"),
		cancelOrder("bob", "cancel-maker", "different"), place("carol", "self-maker", d.Buy, 99, 3),
		place("carol", "self-taker", d.Sell, 99, 1), place("alice", "rejected", d.Sell, 100, 1000),
		{Kind: "snapshot"}, place("bob", "tail", d.Sell, 101, 1), {Kind: "restart"},
	}}
	rng := rand.New(rand.NewSource(seed))
	var history []action
	for _, a := range p.Actions {
		if a.Command != nil {
			history = append(history, a)
		}
	}
	controls := 2
	for len(p.Actions) < count-1 {
		n := len(p.Actions)
		account := []string{"alice", "bob", "carol"}[rng.Intn(3)]
		var a action
		switch choice := rng.Intn(10); {
		case choice == 0 && controls < 30:
			a.Kind = "snapshot"
			controls++
		case choice == 1 && controls < 30:
			a.Kind = "restart"
			controls++
		case choice < 5:
			a = clone(history[rng.Intn(len(history))])
			if choice == 4 {
				if a.Command.Place != nil {
					a.Command.Place.QuantityLots++
				} else {
					a.Command.Cancel.ClientOrderID = "missing-target"
				}
			}
		case choice < 7:
			target := history[rng.Intn(len(history))].Command
			id := "missing-order"
			if target.Place != nil {
				id = target.Place.ClientOrderID
			}
			a = cancelOrder(account, fmt.Sprintf("cancel-%d", n), id)
		default:
			a = place(account, fmt.Sprintf("order-%d", n), []d.Side{d.Buy, d.Sell}[rng.Intn(2)], d.Int(95+rng.Intn(11)), d.Int(1+rng.Intn(12)))
		}
		p.Actions = append(p.Actions, a)
		if a.Command != nil {
			history = append(history, clone(a))
		}
	}
	p.Actions = append(p.Actions, action{Kind: "restart"})
	return p, validatePlan(p)
}

func validatePlan(p plan) error {
	if p.Version != 1 || len(p.Actions) < 1 || len(p.Actions) > maxActions {
		return fmt.Errorf("unsupported scenario version or action count")
	}
	controls := 0
	for i, a := range p.Actions {
		switch a.Kind {
		case "command":
			if a.Command == nil {
				return fmt.Errorf("action %d: missing command", i)
			}
			c, err := a.Command.Command("00000000-0000-4000-8000-000000000001")
			if err != nil || c.AccountID != "alice" && c.AccountID != "bob" && c.AccountID != "carol" {
				return fmt.Errorf("action %d: invalid command/account", i)
			}
		case "snapshot", "restart":
			controls++
			if a.Command != nil || controls > 32 {
				return fmt.Errorf("action %d: invalid control or more than 32 controls", i)
			}
		default:
			return fmt.Errorf("action %d: unknown kind", i)
		}
	}
	return nil
}

func same(want, got any, label string) error {
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("%s mismatch: want %+v; got %+v", label, want, got)
	}
	return nil
}
