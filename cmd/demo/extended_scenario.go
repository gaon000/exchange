package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
)

func extendedCommands() []command {
	place := func(id string, side d.Side, price, qty d.Int) d.Place {
		return d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}
	}
	return append(commands(),
		command{"bob", "/v1/orders", "/v1/results/place/demo-maker", place("demo-maker", d.Sell, 5_000_000, 10)},
		command{"bob", "/v1/orders", "/v1/results/place/demo-self", place("demo-self", d.Buy, 5_000_000, 10)},
		command{"alice", "/v1/orders", "/v1/results/place/demo-improved", place("demo-improved", d.Buy, 6_000_000, 10)},
		command{"alice", "/v1/orders", "/v1/results/place/demo-tail", place("demo-tail", d.Buy, 4_000_000, 5)},
		command{"alice", "/v1/cancellations", "/v1/results/cancel/demo-tail-cancel", d.Cancel{CancelRequestID: "demo-tail-cancel", ClientOrderID: "demo-tail"}},
	)
}

// These fixed expectations use integer arithmetic independent of engine/replay.
func extendedOrder(number, asOf d.Int) d.Order {
	switch number {
	case 1:
		return buyOrder(asOf)
	case 2:
		return sellOrder()
	case 3:
		return buyOrder(3)
	case 8:
		number = 7
	}
	cmd := extendedCommands()[int(number)-1].body.(d.Place)
	o := d.Order{ClientOrderID: cmd.ClientOrderID, Symbol: d.Symbol, Side: cmd.Side, PriceTicks: cmd.PriceTicks, OriginalQty: cmd.QuantityLots, OpenQty: cmd.QuantityLots, Status: d.Open, CreatedSeq: number, UpdatedSeq: number}
	switch number {
	case 4:
		if asOf >= 6 {
			o.OpenQty, o.ExecutedQty, o.ExecutedQuoteAtoms, o.Status, o.UpdatedSeq = 0, 10, 50_000_000, d.Filled, 6
		}
	case 5:
		o.OpenQty, o.CancelledQty, o.Status, o.Reason = 0, 10, d.Cancelled, d.SelfTradePrevention
	case 6:
		o.OpenQty, o.ExecutedQty, o.ExecutedQuoteAtoms, o.Status = 0, 10, 50_000_000, d.Filled
	case 7:
		if asOf >= 8 {
			o.OpenQty, o.CancelledQty, o.Status, o.Reason, o.UpdatedSeq = 0, 5, d.Cancelled, d.UserCancel, 8
		}
	}
	return o
}

func extendedResult(run string, seq d.Int) d.Result {
	if seq <= 3 {
		return result(run, seq)
	}
	cmd := extendedCommands()[int(seq)-1]
	o := extendedOrder(seq, seq)
	r := d.Result{SchemaVersion: 1, RunID: run, AccountID: cmd.account, Kind: d.PlaceOrder, RequestID: o.ClientOrderID, CommandSeq: seq, Code: d.Accepted, Order: &o}
	if seq == 6 {
		r.TradeCount = 1
	}
	if seq == 8 {
		r.Kind, r.RequestID, r.Code = d.CancelOrder, "demo-tail-cancel", d.CancelSucceeded
	}
	return r
}

func extendedBalances(run string, seq d.Int, account string) api.BalancesView {
	v := balances(run, seq, account)
	if account == "bob" && seq >= 4 {
		v.BTC.AvailableLots, v.BTC.ReservedLots = 9_950, 10
	}
	if seq >= 6 {
		if account == "alice" {
			v.BTC.AvailableLots, v.USDT.AvailableAtoms = 50, 99_710_000_000
		} else {
			v.BTC.ReservedLots, v.USDT.AvailableAtoms = 0, 290_000_000
		}
	}
	if account == "alice" && seq == 7 {
		v.USDT.AvailableAtoms, v.USDT.ReservedAtoms = 99_690_000_000, 20_000_000
	}
	return v
}

func checkExtendedState(ctx context.Context, c *httpClient, seq d.Int) error {
	for _, account := range []string{"alice", "bob"} {
		if _, err := expect(ctx, c, account, http.MethodGet, "/v1/balances", nil, extendedBalances(c.runID, seq, account)); err != nil {
			return err
		}
	}
	book := api.BookView{RunID: c.runID, AsOfSeq: seq, Bids: []api.Level{}, Asks: []api.Level{}}
	switch seq {
	case 1:
		book.Bids = []api.Level{{PriceTicks: 6_000_000, QuantityLots: 100}}
	case 2:
		book.Bids = []api.Level{{PriceTicks: 6_000_000, QuantityLots: 60}}
	case 4, 5:
		book.Asks = []api.Level{{PriceTicks: 5_000_000, QuantityLots: 10}}
	case 7:
		book.Bids = []api.Level{{PriceTicks: 4_000_000, QuantityLots: 5}}
	}
	if _, err := expect(ctx, c, "alice", http.MethodGet, "/v1/book", nil, book); err != nil {
		return err
	}
	trades := api.TradesView{RunID: c.runID, AsOfSeq: seq, ThroughSeq: seq, Trades: []api.Trade{}}
	if seq >= 2 {
		trades.Trades = append(trades.Trades, api.Trade{TradeID: c.runID + ":2:1", PriceTicks: 6_000_000, QuantityLots: 40, TakerSide: d.Sell})
	}
	if seq >= 6 {
		trades.Trades = append(trades.Trades, api.Trade{TradeID: c.runID + ":6:1", PriceTicks: 5_000_000, QuantityLots: 10, TakerSide: d.Buy})
	}
	if _, err := expect(ctx, c, "alice", http.MethodGet, "/v1/trades", nil, trades); err != nil {
		return err
	}
	for _, n := range []d.Int{1, 2, 4, 5, 6, 7} {
		if n > seq {
			continue
		}
		o := extendedOrder(n, seq)
		cmd := extendedCommands()[int(n)-1]
		if _, err := expect(ctx, c, cmd.account, http.MethodGet, "/v1/orders/"+o.ClientOrderID, nil, api.OrderView{RunID: c.runID, AsOfSeq: seq, Order: &o}); err != nil {
			return err
		}
	}
	return nil
}

func checkExtendedResults(ctx context.Context, c *httpClient, original [][]byte, retry bool) error {
	if len(original) > len(extendedCommands()) {
		return errors.New("invalid original response count")
	}
	for i, raw := range original {
		cmd := extendedCommands()[i]
		method, path, body := http.MethodGet, cmd.lookup, any(nil)
		if retry {
			method, path, body = http.MethodPost, cmd.path, cmd.body
		}
		got, err := expect(ctx, c, cmd.account, method, path, body, extendedResult(c.runID, d.Int(i+1)))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, raw) {
			return fmt.Errorf("first response bytes changed for %s", cmd.lookup)
		}
	}
	return nil
}

func checkConflicts(ctx context.Context, c *httpClient, seq d.Int, original [][]byte) error {
	differentBuy := buy
	differentBuy.QuantityLots++
	want := api.ErrorResponse{}
	want.Error.Code, want.Error.Outcome = "IDEMPOTENCY_CONFLICT", "NOT_ADMITTED"
	for _, cmd := range []command{
		{account: "alice", path: "/v1/orders", body: differentBuy},
		{account: "alice", path: "/v1/cancellations", body: d.Cancel{CancelRequestID: cancelOrder.CancelRequestID, ClientOrderID: "demo-tail"}},
	} {
		if _, err := expectStatus(ctx, c, cmd.account, http.MethodPost, cmd.path, cmd.body, http.StatusConflict, want); err != nil {
			return err
		}
	}
	if err := checkExtendedState(ctx, c, seq); err != nil {
		return err
	}
	return checkExtendedResults(ctx, c, original, false)
}
