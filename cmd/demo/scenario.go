package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
)

var buy = d.Place{ClientOrderID: "demo-buy", Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 6_000_000, QuantityLots: 100}
var sell = d.Place{ClientOrderID: "demo-sell", Symbol: d.Symbol, Side: d.Sell, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 5_990_000, QuantityLots: 40}
var cancelOrder = d.Cancel{CancelRequestID: "demo-cancel", ClientOrderID: "demo-buy"}

// Expected values are explicit independent arithmetic, not a replay through the engine.
func buyOrder(seq d.Int) d.Order {
	o := d.Order{ClientOrderID: "demo-buy", Symbol: d.Symbol, Side: d.Buy, PriceTicks: 6_000_000, OriginalQty: 100, OpenQty: 100, Status: d.Open, CreatedSeq: 1, UpdatedSeq: 1}
	if seq >= 2 {
		o.ExecutedQty, o.OpenQty, o.ExecutedQuoteAtoms = 40, 60, 240_000_000
		o.Status, o.UpdatedSeq = d.PartiallyFilled, 2
	}
	if seq >= 3 {
		o.OpenQty, o.CancelledQty = 0, 60
		o.Status, o.Reason, o.UpdatedSeq = d.Cancelled, d.UserCancel, 3
	}
	return o
}

func sellOrder() d.Order {
	return d.Order{ClientOrderID: "demo-sell", Symbol: d.Symbol, Side: d.Sell, PriceTicks: 5_990_000, OriginalQty: 40, ExecutedQty: 40, ExecutedQuoteAtoms: 240_000_000, Status: d.Filled, CreatedSeq: 2, UpdatedSeq: 2}
}

func result(run string, seq d.Int) d.Result {
	o := buyOrder(1)
	r := d.Result{SchemaVersion: 1, RunID: run, AccountID: "alice", Kind: d.PlaceOrder, RequestID: buy.ClientOrderID, CommandSeq: seq, Code: d.Accepted, Order: &o}
	if seq == 2 {
		o = sellOrder()
		r.AccountID, r.RequestID, r.TradeCount = "bob", sell.ClientOrderID, 1
	}
	if seq == 3 {
		o = buyOrder(3)
		r.Kind, r.RequestID, r.Code = d.CancelOrder, cancelOrder.CancelRequestID, d.CancelSucceeded
	}
	return r
}

func balances(run string, seq d.Int, account string) api.BalancesView {
	v := api.BalancesView{RunID: run, AsOfSeq: seq}
	if account == "alice" {
		v.USDT.AvailableAtoms = 100_000_000_000
		if seq >= 1 {
			v.USDT.AvailableAtoms, v.USDT.ReservedAtoms = 99_400_000_000, 600_000_000
		}
		if seq >= 2 {
			v.BTC.AvailableLots, v.USDT.ReservedAtoms = 40, 360_000_000
		}
		if seq >= 3 {
			v.USDT.AvailableAtoms, v.USDT.ReservedAtoms = 99_760_000_000, 0
		}
	} else {
		v.BTC.AvailableLots = 10_000
		if seq >= 2 {
			v.BTC.AvailableLots, v.USDT.AvailableAtoms = 9_960, 240_000_000
		}
	}
	return v
}

func checkState(ctx context.Context, c *httpClient, seq d.Int) error {
	for _, account := range []string{"alice", "bob"} {
		if _, err := expect(ctx, c, account, http.MethodGet, "/v1/balances", nil, balances(c.runID, seq, account)); err != nil {
			return err
		}
	}
	book := api.BookView{RunID: c.runID, AsOfSeq: seq, Bids: []api.Level{}, Asks: []api.Level{}}
	if seq == 1 {
		book.Bids = []api.Level{{PriceTicks: 6_000_000, QuantityLots: 100}}
	} else if seq == 2 {
		book.Bids = []api.Level{{PriceTicks: 6_000_000, QuantityLots: 60}}
	}
	if _, err := expect(ctx, c, "alice", http.MethodGet, "/v1/book", nil, book); err != nil {
		return err
	}
	trades := api.TradesView{RunID: c.runID, AsOfSeq: seq, ThroughSeq: seq, Trades: []api.Trade{}}
	if seq >= 2 {
		trades.Trades = []api.Trade{{TradeID: c.runID + ":2:1", PriceTicks: 6_000_000, QuantityLots: 40, TakerSide: d.Sell}}
	}
	if _, err := expect(ctx, c, "alice", http.MethodGet, "/v1/trades", nil, trades); err != nil {
		return err
	}
	if seq >= 1 {
		o := buyOrder(seq)
		if _, err := expect(ctx, c, "alice", http.MethodGet, "/v1/orders/demo-buy", nil, api.OrderView{RunID: c.runID, AsOfSeq: seq, Order: &o}); err != nil {
			return err
		}
	}
	if seq >= 2 {
		o := sellOrder()
		if _, err := expect(ctx, c, "bob", http.MethodGet, "/v1/orders/demo-sell", nil, api.OrderView{RunID: c.runID, AsOfSeq: seq, Order: &o}); err != nil {
			return err
		}
	}
	return nil
}

type command struct {
	account, path, lookup string
	body                  any
}

func commands() []command {
	return []command{
		{"alice", "/v1/orders", "/v1/results/place/demo-buy", buy},
		{"bob", "/v1/orders", "/v1/results/place/demo-sell", sell},
		{"alice", "/v1/cancellations", "/v1/results/cancel/demo-cancel", cancelOrder},
	}
}

func checkResults(ctx context.Context, c *httpClient, original [][]byte, retry bool) error {
	for i, cmd := range commands() {
		method, path, body := http.MethodGet, cmd.lookup, any(nil)
		if retry {
			method, path, body = http.MethodPost, cmd.path, cmd.body
		}
		raw, err := expect(ctx, c, cmd.account, method, path, body, result(c.runID, d.Int(i+1)))
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, original[i]) {
			return fmt.Errorf("first response bytes changed for %s", cmd.lookup)
		}
	}
	return nil
}
