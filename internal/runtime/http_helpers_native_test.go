//go:build darwin && cgo

package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/wal"
)

type httpGate struct {
	ch   chan struct{}
	once sync.Once
}

func newHTTPGate() *httpGate { return &httpGate{ch: make(chan struct{})} }
func (g *httpGate) open()    { g.once.Do(func() { close(g.ch) }) }
func (g *httpGate) wait()    { <-g.ch }
func httpContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func httpAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(8 * time.Second):
		t.Fatal("HTTP integration channel deadline")
		var zero T
		return zero
	}
}

func httpEventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("HTTP integration condition deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func httpDecode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return v
}

type httpReply struct {
	status int
	body   []byte
	err    error
}

func httpRequest(client *http.Client, base, run, account, method, path, body string) httpReply {
	req, err := http.NewRequest(method, base+path, bytes.NewBufferString(body))
	if err != nil {
		return httpReply{err: err}
	}
	req.Header.Set("X-Run-ID", run)
	req.Header.Set("X-Account-ID", account)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return httpReply{err: err}
	}
	data, readErr := io.ReadAll(res.Body)
	return httpReply{res.StatusCode, data, errors.Join(readErr, res.Body.Close())}
}

func httpAsync(client *http.Client, base, run, account, method, path, body string) <-chan httpReply {
	out := make(chan httpReply, 1)
	go func() { out <- httpRequest(client, base, run, account, method, path, body) }()
	return out
}

func httpStatus(t *testing.T, got httpReply, want int) []byte {
	t.Helper()
	if got.err != nil || got.status != want {
		t.Fatalf("HTTP want %d got %d %s: %v", want, got.status, got.body, got.err)
	}
	return got.body
}

func httpFailure(t *testing.T, got httpReply, status int, code string, outcome rt.Outcome) {
	t.Helper()
	f := httpDecode[api.ErrorResponse](t, httpStatus(t, got, status)).Error
	if f.Code != code || f.Outcome != outcome {
		t.Fatalf("want %s/%s got %+v", code, outcome, f)
	}
}

func httpOrder(id, side string, price, qty int) string {
	return fmt.Sprintf(`{"clientOrderId":%q,"symbol":"BTC-USDT","side":%q,"type":"LIMIT","timeInForce":"GTC","priceTicks":%q,"quantityLots":%q}`, id, side, fmt.Sprint(price), fmt.Sprint(qty))
}

func httpOptions() rt.Options { o := rt.DefaultOptions(); o.BatchCommands = 1; return o }

func httpOpen(t *testing.T, dir string, o rt.Options, h rt.HTTPTestHooks) *rt.Runtime {
	t.Helper()
	r, err := rt.OpenHTTPTestRuntime(dir, o, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c, cancel := httpContext(); defer cancel(); _ = r.Shutdown(c) })
	return r
}

func httpStop(t *testing.T, r *rt.Runtime) {
	t.Helper()
	c, cancel := httpContext()
	defer cancel()
	if err := r.Shutdown(c); err != nil {
		t.Fatal(err)
	}
}

type httpEndpoint struct {
	server *api.Server
	client *http.Client
	base   string
	done   chan error
	once   sync.Once
}

func httpServe(t *testing.T, backend api.Backend, listener net.Listener) *httpEndpoint {
	t.Helper()
	s, err := api.New(backend, api.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if listener == nil {
		listener, err = api.ListenLoopback("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
	}
	transport := &http.Transport{Proxy: nil}
	e := &httpEndpoint{s, &http.Client{Transport: transport, Timeout: 10 * time.Second}, "http://" + listener.Addr().String(), make(chan error, 1), sync.Once{}}
	go func() { e.done <- s.Serve(listener) }()
	t.Cleanup(func() { e.close(t) })
	return e
}

func (e *httpEndpoint) close(t *testing.T) {
	t.Helper()
	e.once.Do(func() {
		if err := e.server.Close(); err != nil {
			t.Error(err)
		}
		e.client.CloseIdleConnections()
		if err := httpAwait(t, e.done); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
}

func (e *httpEndpoint) call(run, account, method, path, body string) httpReply {
	return httpRequest(e.client, e.base, run, account, method, path, body)
}

type httpExpectation struct {
	account, path string
	body          []byte
}

type httpFixture struct {
	dir, run      string
	config        d.Config
	prefix, group []byte
	seed, target  []byte
	views         []httpExpectation
}

// A separately completed run supplies the expected full state and immutable
// result. Fixed arithmetic below also checks the matching/balance oracle.
func newHTTPFixture(t *testing.T) httpFixture {
	t.Helper()
	f := httpFixture{dir: filepath.Join(t.TempDir(), "run")}
	config := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 100, QuoteAtoms: 100000}, {AccountID: "bob", BaseLots: 100, QuoteAtoms: 100000}})
	f.config = config
	s, err := wal.Init(f.dir, config)
	if err != nil {
		t.Fatal(err)
	}
	f.run = s.Genesis().RunID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r := httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{})
	e := httpServe(t, r, nil)
	f.seed = httpStatus(t, e.call(f.run, "bob", "POST", "/v1/orders", httpOrder("seed", "SELL", 100, 4)), 200)
	c, cancel := httpContext()
	_, err = r.Snapshot(c)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	e.close(t)
	httpStop(t, r)
	f.prefix, err = os.ReadFile(filepath.Join(f.dir, "commands.wal"))
	if err != nil {
		t.Fatal(err)
	}
	// Copy only this closed, disposable fixture; each copy gets its own lock inode.
	reference := t.TempDir()
	for _, name := range []string{".lock", "genesis.bin", "commands.wal"} {
		data, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(reference, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	r = httpOpen(t, reference, httpOptions(), rt.HTTPTestHooks{})
	e = httpServe(t, r, nil)
	f.target = httpStatus(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2)), 200)
	result := httpDecode[d.Result](t, f.target)
	if result.CommandSeq != 2 || result.Order.Status != d.Filled || result.Order.ExecutedQuoteAtoms != 200 || result.TradeCount != 1 {
		t.Fatalf("unexpected baseline trade: %+v", result)
	}
	for _, q := range []httpExpectation{{account: "alice", path: "/v1/balances"}, {account: "bob", path: "/v1/balances"}, {account: "bob", path: "/v1/orders/seed"}, {account: "alice", path: "/v1/orders/target"}, {account: "alice", path: "/v1/book"}, {account: "alice", path: "/v1/trades"}} {
		q.body = httpStatus(t, e.call(f.run, q.account, "GET", q.path, ""), 200)
		f.views = append(f.views, q)
	}
	alice := httpDecode[api.BalancesView](t, f.views[0].body)
	bob := httpDecode[api.BalancesView](t, f.views[1].body)
	if alice.BTC.AvailableLots != 102 || alice.USDT.AvailableAtoms != 99800 || alice.USDT.ReservedAtoms != 0 || bob.BTC.AvailableLots != 96 || bob.BTC.ReservedLots != 2 || bob.USDT.AvailableAtoms != 100200 {
		t.Fatalf("baseline balances: %+v %+v", alice, bob)
	}
	e.close(t)
	httpStop(t, r)
	all, err := os.ReadFile(filepath.Join(reference, "commands.wal"))
	if err != nil || !bytes.HasPrefix(all, f.prefix) || len(all) <= len(f.prefix) {
		t.Fatalf("baseline WAL: %v", err)
	}
	f.group = append([]byte(nil), all[len(f.prefix):]...)
	return f
}

func assertHTTPRecovered(t *testing.T, f httpFixture, expectedSeq d.Int) {
	t.Helper()
	assertHTTPRecoveredFrom(t, f, expectedSeq, 1)
}

func assertHTTPRecoveredFrom(t *testing.T, f httpFixture, expectedSeq, snapshotSeq d.Int) {
	t.Helper()
	r := httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{})
	e := httpServe(t, r, nil)
	if r.RunID() != f.run || r.Recovery().SnapshotSeq != snapshotSeq || r.Recovery().ReplayedCommands != expectedSeq-snapshotSeq {
		t.Fatalf("recovery metadata: %+v", r.Recovery())
	}
	before := httpDecode[api.BalancesView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 200))
	if before.AsOfSeq != expectedSeq {
		t.Fatalf("recovered seq %d want %d", before.AsOfSeq, expectedSeq)
	}
	state := e.call(f.run, "alice", "GET", "/v1/results/place/target", "")
	if expectedSeq == 1 {
		httpStatus(t, state, 404)
	} else if got := httpStatus(t, state, 200); !bytes.Equal(got, f.target) {
		t.Fatalf("recovered first result: %s", got)
	}
	for i := 0; i < 2; i++ {
		got := httpStatus(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2)), 200)
		if !bytes.Equal(got, f.target) {
			t.Fatalf("retry changed first result: %s", got)
		}
	}
	seed := httpStatus(t, e.call(f.run, "bob", "POST", "/v1/orders", httpOrder("seed", "SELL", 100, 4)), 200)
	if !bytes.Equal(seed, f.seed) {
		t.Fatal("partially filled seed changed its initial OPEN result")
	}
	for _, want := range f.views {
		got := httpStatus(t, e.call(f.run, want.account, "GET", want.path, ""), 200)
		if !bytes.Equal(got, want.body) {
			t.Fatalf("recovered %s %s: %s want %s", want.account, want.path, got, want.body)
		}
	}
	e.close(t)
	httpStop(t, r)
	data, err := os.ReadFile(filepath.Join(f.dir, "commands.wal"))
	want := append(append([]byte(nil), f.prefix...), f.group...)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("retries added or changed WAL: %v", err)
	}
}
