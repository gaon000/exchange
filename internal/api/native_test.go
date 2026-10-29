//go:build darwin && cgo

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/wal"
)

func nativeRuntime(t *testing.T, config d.Config, options rt.Options) (string, *rt.Runtime) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	s, err := wal.Init(dir, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, reopenRuntime(t, dir, options)
}
func reopenRuntime(t *testing.T, dir string, options rt.Options) *rt.Runtime {
	t.Helper()
	r, err := rt.Open(dir, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(c)
	})
	return r
}
func testConfig() d.Config {
	return d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 100, QuoteAtoms: 100000}, {AccountID: "bob", BaseLots: 100, QuoteAtoms: 100000}})
}
func orderBody(id, side, price, quantity string) string {
	return fmt.Sprintf(`{"clientOrderId":%q,"symbol":"BTC-USDT","side":%q,"type":"LIMIT","timeInForce":"GTC","priceTicks":%q,"quantityLots":%q}`, id, side, price, quantity)
}
func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("JSON: %s: %v", data, err)
	}
	return value
}

func TestNativeHTTPTradingSnapshotsAndRestart(t *testing.T) {
	dir, runtime := nativeRuntime(t, testConfig(), rt.DefaultOptions())
	s, base, client := startHTTP(t, runtime, DefaultOptions())
	run := runtime.RunID()
	type original struct {
		account, path, body string
		response            []byte
	}
	var originals []original
	post := func(account, path, body string) d.Result {
		t.Helper()
		status, data := call(t, client, base, run, account, "POST", path, body)
		if status != 200 {
			t.Fatalf("post: %d %s", status, data)
		}
		originals = append(originals, original{account, path, body, data})
		return decode[d.Result](t, data)
	}
	get := func(account, path string, status int) []byte {
		t.Helper()
		got, data := call(t, client, base, run, account, "GET", path, "")
		if got != status {
			t.Fatalf("get %s: %d %s", path, got, data)
		}
		return data
	}
	ask := post("bob", "/v1/orders", orderBody("ask", "SELL", "100", "4"))
	buy := post("alice", "/v1/orders", orderBody("buy", "BUY", "105", "2"))
	if ask.Order.Status != d.Open || buy.Order.Status != d.Filled || buy.Order.ExecutedQuoteAtoms != 200 || buy.TradeCount != 1 {
		t.Fatal("resting-price fill", ask, buy)
	}
	status, retry := call(t, client, base, run, "alice", "POST", "/v1/orders", orderBody("buy", "BUY", "000105", "0002"))
	if status != 200 || !bytes.Equal(retry, originals[1].response) {
		t.Fatal("normalized retry changed")
	}
	status, data := call(t, client, base, run, "alice", "POST", "/v1/orders", orderBody("buy", "BUY", "106", "2"))
	if status != 409 || decode[ErrorResponse](t, data).Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatal("conflicting retry", string(data))
	}
	balances := decode[BalancesView](t, get("alice", "/v1/balances", 200))
	if balances.AsOfSeq != 2 || balances.BTC.AvailableLots != 102 || balances.USDT.AvailableAtoms != 99800 || balances.USDT.ReservedAtoms != 0 {
		t.Fatal("price improvement settlement", balances)
	}
	book := decode[BookView](t, get("alice", "/v1/book", 200))
	if len(book.Bids) != 0 || len(book.Asks) != 1 || book.Asks[0] != (Level{100, 2}) {
		t.Fatal(book)
	}
	get("alice", "/v1/orders/ask", 404)
	if string(get("alice", "/v1/results/place/ask", 404)) != `{"state":"UNKNOWN"}` {
		t.Fatal("cross-account result")
	}
	foreignCancel := post("alice", "/v1/cancellations", `{"cancelRequestId":"cancel-foreign","clientOrderId":"ask"}`)
	if foreignCancel.Code != d.OrderNotFound || foreignCancel.Order != nil {
		t.Fatal("cross-account cancel", foreignCancel)
	}
	cancel := post("bob", "/v1/cancellations", `{"cancelRequestId":"cancel-ask","clientOrderId":"ask"}`)
	if cancel.Code != d.CancelSucceeded || cancel.Order.CancelledQty != 2 {
		t.Fatal(cancel)
	}
	if !bytes.Equal(get("bob", "/v1/results/place/ask", 200), originals[0].response) {
		t.Fatal("first OPEN response changed after cancel")
	}
	current := decode[OrderView](t, get("bob", "/v1/orders/ask", 200))
	if current.Order.Status != d.Cancelled || current.AsOfSeq != 4 {
		t.Fatal(current)
	}
	post("alice", "/v1/orders", orderBody("own-ask", "SELL", "110", "1"))
	stp := post("alice", "/v1/orders", orderBody("stp", "BUY", "110", "2"))
	if stp.Order.Status != d.Cancelled || stp.Order.CancelledQty != 2 || stp.Order.Reason != d.SelfTradePrevention || stp.TradeCount != 0 {
		t.Fatal(stp)
	}
	post("alice", "/v1/cancellations", `{"cancelRequestId":"cancel-own","clientOrderId":"own-ask"}`)
	rejected := post("alice", "/v1/orders", orderBody("no-funds", "BUY", "100000000", "1"))
	if rejected.Code != d.InsufficientBalance || rejected.Order.Status != d.Rejected {
		t.Fatal(rejected)
	}
	if view := decode[OrderView](t, get("alice", "/v1/orders/no-funds", 200)); view.Order.Status != d.Rejected {
		t.Fatal(view)
	}
	post("alice", "/v1/cancellations", `{"cancelRequestId":"missing","clientOrderId":"no-funds"}`)
	post("bob", "/v1/orders", orderBody("ask-2", "SELL", "100", "2"))
	post("alice", "/v1/orders", orderBody("buy-2", "BUY", "100", "2"))
	page := decode[TradesView](t, get("alice", "/v1/trades?limit=1", 200))
	if page.ThroughSeq != 11 || len(page.Trades) != 1 || page.NextCursor == nil || page.NextCursor.AfterSeq != 2 {
		t.Fatal(page)
	}
	post("bob", "/v1/orders", orderBody("ask-3", "SELL", "120", "1"))
	post("alice", "/v1/orders", orderBody("buy-3", "BUY", "120", "1"))
	next := fmt.Sprintf("/v1/trades?limit=1&throughSeq=%s&afterSeq=%s&afterIndex=%s", page.ThroughSeq, page.NextCursor.AfterSeq, page.NextCursor.AfterIndex)
	page2 := decode[TradesView](t, get("bob", next, 200))
	if page2.AsOfSeq != 13 || page2.ThroughSeq != 11 || len(page2.Trades) != 1 || page2.NextCursor == nil || page2.NextCursor.AfterSeq != 11 {
		t.Fatal(page2)
	}
	last := decode[TradesView](t, get("alice", "/v1/trades?limit=1&throughSeq=11&afterSeq=11&afterIndex=1", 200))
	if len(last.Trades) != 0 || last.NextCursor != nil {
		t.Fatal("new trade crossed fixed window", last)
	}
	get("alice", "/v1/trades?throughSeq=14", 400)
	get("alice", "/v1/trades?throughSeq=13&afterSeq=2&afterIndex=2", 400)
	for _, which := range []string{"unknown-account", "wrong-run"} {
		id, account, want := run, "nobody", 403
		if which == "wrong-run" {
			id, account, want = testRun, "alice", 409
		}
		got, _ := call(t, client, base, id, account, "GET", "/v1/balances", "")
		if got != want {
			t.Fatal(which, got)
		}
	}
	before := get("alice", "/v1/balances", 200)
	if result, err := runtime.Snapshot(context.Background()); err != nil || result.SnapshotSeq != 13 {
		t.Fatal(result, err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime = reopenRuntime(t, dir, rt.DefaultOptions())
	_, base, client = startHTTP(t, runtime, DefaultOptions())
	if runtime.RunID() != run || runtime.Recovery().SnapshotSeq != 13 {
		t.Fatal("wrong recovered run/boundary")
	}
	if after := get("alice", "/v1/balances", 200); !bytes.Equal(before, after) {
		t.Fatal("balances changed/reseeded")
	}
	for _, item := range originals {
		status, data := call(t, client, base, run, item.account, "POST", item.path, item.body)
		if status != 200 || !bytes.Equal(data, item.response) {
			t.Fatal("first response changed on restart", item.body, string(data))
		}
	}
	if after := get("alice", "/v1/balances", 200); !bytes.Equal(before, after) {
		t.Fatal("retries consumed commands")
	}
}

func TestNativeHTTPTimeoutAndDisconnectDoNotCancelAcceptedWork(t *testing.T) {
	for _, mode := range []string{"timeout", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			opts := rt.DefaultOptions()
			opts.BatchCommands = 2
			opts.BatchDelay = time.Hour
			_, runtime := nativeRuntime(t, testConfig(), opts)
			httpOpts := DefaultOptions()
			httpOpts.ResultTimeout = 200 * time.Millisecond
			_, base, client := startHTTP(t, runtime, httpOpts)
			run := runtime.RunID()
			c := rawConnection(t, base)
			_, err := fmt.Fprintf(c, "POST /v1/orders HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Run-ID: %s\r\nX-Account-ID: alice\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", run, len(placeBody), placeBody)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				status, data := call(t, client, base, run, "alice", "GET", "/v1/results/place/one", "")
				if status == 202 && string(data) == `{"state":"PENDING"}` {
					break
				}
				if status != 404 || time.Now().After(deadline) {
					t.Fatalf("pending lookup: %d %s", status, data)
				}
			}
			if mode == "disconnect" {
				_ = c.Close()
			} else {
				response, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode != 504 || decode[ErrorResponse](t, data).Error.Outcome != rt.Unknown {
					t.Fatalf("%d %s", response.StatusCode, data)
				}
			}
			// The second distinct command closes the durable group. The original
			// waiter has gone, but its accepted command must still be applied once.
			status, data := call(t, client, base, run, "bob", "POST", "/v1/orders", orderBody("two", "SELL", "100", "1"))
			if status != 200 || decode[d.Result](t, data).CommandSeq != 2 {
				t.Fatalf("trigger group: %d %s", status, data)
			}
			status, first := call(t, client, base, run, "alice", "GET", "/v1/results/place/one", "")
			if status != 200 || decode[d.Result](t, first).CommandSeq != 1 {
				t.Fatal("accepted order lost", string(first))
			}
			status, retry := call(t, client, base, run, "alice", "POST", "/v1/orders", placeBody)
			if status != 200 || !bytes.Equal(first, retry) {
				t.Fatal("retry changed result")
			}
			status, current := call(t, client, base, run, "alice", "GET", "/v1/orders/one", "")
			if status != 200 || decode[OrderView](t, current).Order.Status != d.PartiallyFilled || !strings.Contains(string(first), `"status":"OPEN"`) {
				t.Fatal("first result confused with current order")
			}
		})
	}
}

func TestNativeHTTPRunLimitKeepsResultsReadable(t *testing.T) {
	config := testConfig()
	config.MaxCommands = 1
	_, runtime := nativeRuntime(t, config, rt.DefaultOptions())
	_, base, client := startHTTP(t, runtime, DefaultOptions())
	run := runtime.RunID()
	status, first := call(t, client, base, run, "alice", "POST", "/v1/orders", placeBody)
	if status != 200 {
		t.Fatal(string(first))
	}
	status, body := call(t, client, base, run, "alice", "POST", "/v1/orders", orderBody("next", "BUY", "100", "2"))
	if status != 409 || decode[ErrorResponse](t, body).Error.Code != "RUN_LIMIT_REACHED" {
		t.Fatal(string(body))
	}
	status, retry := call(t, client, base, run, "alice", "POST", "/v1/orders", placeBody)
	if status != 200 || !bytes.Equal(first, retry) {
		t.Fatal("retry at run limit")
	}
	status, lookup := call(t, client, base, run, "alice", "GET", "/v1/results/place/one", "")
	if status != 200 || !bytes.Equal(first, lookup) {
		t.Fatal("result at run limit")
	}
}

func TestNativeHTTPConcurrentDuplicateAndCancelMatchRace(t *testing.T) {
	_, runtime := nativeRuntime(t, testConfig(), rt.DefaultOptions())
	_, base, client := startHTTP(t, runtime, DefaultOptions())
	run := runtime.RunID()
	type reply struct {
		status int
		data   []byte
		err    error
	}
	post := func(account, path, body string) reply {
		r, err := http.NewRequest("POST", base+path, strings.NewReader(body))
		if err != nil {
			return reply{err: err}
		}
		r.Header.Set("X-Run-ID", run)
		r.Header.Set("X-Account-ID", account)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			return reply{err: err}
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return reply{response.StatusCode, data, err}
	}
	start, results := make(chan struct{}), make(chan reply, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { <-start; results <- post("alice", "/v1/orders", placeBody) })
	}
	close(start)
	wg.Wait()
	close(results)
	var first []byte
	for result := range results {
		if result.err != nil || result.status != 200 {
			t.Fatalf("duplicate submit: %d %s %v", result.status, result.data, result.err)
		}
		if first == nil {
			first = result.data
		} else if !bytes.Equal(first, result.data) {
			t.Fatal("concurrent first responses differ")
		}
		if decode[d.Result](t, result.data).CommandSeq != 1 {
			t.Fatal("duplicate consumed sequence")
		}
	}
	raceStart := make(chan struct{})
	sell, cancel := make(chan reply, 1), make(chan reply, 1)
	go func() { <-raceStart; sell <- post("bob", "/v1/orders", orderBody("match", "SELL", "100", "2")) }()
	go func() {
		<-raceStart
		cancel <- post("alice", "/v1/cancellations", `{"cancelRequestId":"race","clientOrderId":"one"}`)
	}()
	close(raceStart)
	a, b := receive(t, sell), receive(t, cancel)
	if a.err != nil || b.err != nil || a.status != 200 || b.status != 200 {
		t.Fatalf("race replies: %+v %+v", a, b)
	}
	sellResult, cancelResult := decode[d.Result](t, a.data), decode[d.Result](t, b.data)
	if cancelResult.CommandSeq == 2 {
		if cancelResult.Code != d.CancelSucceeded || sellResult.CommandSeq != 3 || sellResult.Order.Status != d.Open {
			t.Fatal("cancel-first ordering", sellResult, cancelResult)
		}
	} else if cancelResult.CommandSeq == 3 {
		if sellResult.CommandSeq != 2 || sellResult.Order.Status != d.Filled || cancelResult.Code != d.AlreadyFilled {
			t.Fatal("match-first ordering", sellResult, cancelResult)
		}
	} else {
		t.Fatal("unexpected command sequence")
	}
	status, result := call(t, client, base, run, "alice", "GET", "/v1/results/place/one", "")
	if status != 200 || !bytes.Equal(result, first) {
		t.Fatal("race replaced first response")
	}
	status, result = call(t, client, base, run, "alice", "GET", "/v1/balances", "")
	balances := decode[BalancesView](t, result)
	if status != 200 || balances.AsOfSeq != 3 || balances.USDT.ReservedAtoms != 0 {
		t.Fatal("race left reservation", balances)
	}
}
