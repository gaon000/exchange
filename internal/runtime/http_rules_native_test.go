//go:build darwin && cgo

package runtime_test

import (
	"bytes"
	"testing"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

func TestNativeHTTPPriceFIFOAndPartialSelfTradePrevention(t *testing.T) {
	f := newHTTPFixture(t)
	r := httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{})
	e := httpServe(t, r, nil)
	type original struct {
		account, body string
		response      []byte
	}
	var originals []original
	post := func(account, id, side string, price, quantity int) d.Result {
		t.Helper()
		body := httpOrder(id, side, price, quantity)
		response := httpStatus(t, e.call(f.run, account, "POST", "/v1/orders", body), 200)
		originals = append(originals, original{account, body, response})
		return httpDecode[d.Result](t, response)
	}
	post("bob", "same-price", "SELL", 100, 2)
	post("bob", "worse-price", "SELL", 102, 1)
	post("alice", "own-ask", "SELL", 103, 1)
	taker := post("alice", "sweep", "BUY", 105, 10)
	if taker.CommandSeq != 5 || taker.TradeCount != 3 || taker.Order.ExecutedQty != 7 || taker.Order.CancelledQty != 3 || taker.Order.ExecutedQuoteAtoms != 702 || taker.Order.Status != d.Cancelled || taker.Order.Reason != d.SelfTradePrevention {
		t.Fatalf("partial STP lost prior trades or cancelled the wrong quantity: %+v", taker)
	}
	trades := httpDecode[api.TradesView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/trades", ""), 200))
	if len(trades.Trades) != 3 {
		t.Fatal("wrong trade count", trades)
	}
	for i, want := range [][2]d.Int{{100, 4}, {100, 2}, {102, 1}} {
		if got := trades.Trades[i]; got.PriceTicks != want[0] || got.QuantityLots != want[1] {
			t.Fatalf("price/FIFO/resting-price order %d: %+v", i, got)
		}
	}
	alice := httpDecode[api.BalancesView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 200))
	bob := httpDecode[api.BalancesView](t, httpStatus(t, e.call(f.run, "bob", "GET", "/v1/balances", ""), 200))
	if alice.BTC.AvailableLots != 106 || alice.BTC.ReservedLots != 1 || alice.USDT.AvailableAtoms != 99298 || alice.USDT.ReservedAtoms != 0 || bob.BTC.AvailableLots != 93 || bob.BTC.ReservedLots != 0 || bob.USDT.AvailableAtoms != 100702 {
		t.Fatalf("partial-STP reservation/release arithmetic: %+v %+v", alice, bob)
	}
	own := httpDecode[api.OrderView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/orders/own-ask", ""), 200))
	if own.Order.Status != d.Open || own.Order.OpenQty != 1 || own.Order.UpdatedSeq != 4 {
		t.Fatal("STP changed the resting own order", own)
	}
	e.close(t)
	httpStop(t, r)
	r = httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{})
	e = httpServe(t, r, nil)
	for _, want := range originals {
		got := httpStatus(t, e.call(f.run, want.account, "POST", "/v1/orders", want.body), 200)
		if !bytes.Equal(got, want.response) {
			t.Fatal("replay changed the original result")
		}
	}
	got := httpStatus(t, e.call(f.run, "bob", "POST", "/v1/orders", httpOrder("seed", "SELL", 100, 4)), 200)
	if !bytes.Equal(got, f.seed) {
		t.Fatal("filled order lost its initial OPEN response")
	}
}

func TestNativeHTTPConcurrentOrdersCannotOverspend(t *testing.T) {
	f := newHTTPFixture(t)
	r := httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{})
	e := httpServe(t, r, nil)
	first := httpAsync(e.client, e.base, f.run, "alice", "POST", "/v1/orders", httpOrder("budget-a", "BUY", 50, 1500))
	second := httpAsync(e.client, e.base, f.run, "alice", "POST", "/v1/orders", httpOrder("budget-b", "BUY", 50, 1500))
	a := httpDecode[d.Result](t, httpStatus(t, httpAwait(t, first), 200))
	b := httpDecode[d.Result](t, httpStatus(t, httpAwait(t, second), 200))
	if a.CommandSeq > b.CommandSeq {
		a, b = b, a
	}
	if a.CommandSeq != 2 || b.CommandSeq != 3 || a.Code != d.Accepted || b.Code != d.InsufficientBalance {
		t.Fatalf("same-account overspend was not serialized: %+v %+v", a, b)
	}
	balance := httpDecode[api.BalancesView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 200))
	if balance.AsOfSeq != 3 || balance.USDT.AvailableAtoms != 25000 || balance.USDT.ReservedAtoms != 75000 {
		t.Fatal("overspend or duplicate reservation", balance)
	}
}
