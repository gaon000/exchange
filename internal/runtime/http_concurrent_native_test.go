//go:build darwin && cgo

package runtime_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type httpAttempt struct {
	command    d.Command
	path, body string
	result     d.Result
	response   []byte
	balance    api.BalancesView
}

func httpWorkload(run string, worker int) []httpAttempt {
	rng := rand.New(rand.NewSource(701 + int64(worker)))
	account := []string{"alice", "bob"}[worker%2]
	var work []httpAttempt
	var previous string
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("w%d-%02d", worker, i)
		a := httpAttempt{command: d.Command{RunID: run, AccountID: account, Kind: d.PlaceOrder}, path: "/v1/orders"}
		if i%4 == 3 {
			a.command.Kind, a.path = d.CancelOrder, "/v1/cancellations"
			a.command.Cancel = d.Cancel{CancelRequestID: id, ClientOrderID: previous}
			body, _ := json.Marshal(a.command.Cancel)
			a.body = string(body)
		} else {
			side := []d.Side{d.Buy, d.Sell}[rng.Intn(2)]
			quantity := 1 + rng.Intn(8)
			if i == 8 {
				quantity = 10000 // deliberate balance rejection, also retried
			}
			a.command.Place = d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: d.Int(95 + rng.Intn(11)), QuantityLots: d.Int(quantity)}
			body, _ := json.Marshal(a.command.Place)
			a.body, previous = string(body), id
		}
		work = append(work, a)
	}
	return work
}

// The sequential engine oracle checks network ordering/replay, not an independent
// matching implementation. P1's separately implemented reference model covers
// matching correctness; this adds concurrent HTTP requests and asOfSeq samples.
func TestNativeHTTPConcurrentSeededReplayAndQueries(t *testing.T) {
	f := newHTTPFixture(t)
	o := httpOptions()
	o.BatchCommands = 16
	r := httpOpen(t, f.dir, o, rt.HTTPTestHooks{})
	e := httpServe(t, r, nil)
	type completed struct {
		attempts []httpAttempt
		err      error
	}
	results := make(chan completed, 4)
	for worker := 0; worker < 4; worker++ {
		work := httpWorkload(f.run, worker)
		go func() {
			for i := range work {
				a := &work[i]
				first := httpAsync(e.client, e.base, f.run, a.command.AccountID, "POST", a.path, a.body)
				second := httpRequest(e.client, e.base, f.run, a.command.AccountID, "POST", a.path, a.body)
				got := <-first // client deadline bounds both calls
				if got.err != nil || second.err != nil || got.status != 200 || second.status != 200 || !bytes.Equal(got.body, second.body) {
					results <- completed{err: fmt.Errorf("concurrent duplicate: %+v %+v", got, second)}
					return
				}
				a.response = got.body
				if err := json.Unmarshal(got.body, &a.result); err != nil {
					results <- completed{err: err}
					return
				}
				query := e.call(f.run, a.command.AccountID, "GET", "/v1/balances", "")
				if query.err != nil || query.status != 200 {
					results <- completed{err: fmt.Errorf("concurrent query: %+v", query)}
					return
				}
				if err := json.Unmarshal(query.body, &a.balance); err != nil {
					results <- completed{err: err}
					return
				}
				if a.balance.AsOfSeq < a.result.CommandSeq || i > 0 && a.balance.AsOfSeq < work[i-1].balance.AsOfSeq {
					results <- completed{err: fmt.Errorf("query sequence moved backwards: %+v", a.balance)}
					return
				}
			}
			results <- completed{attempts: work}
		}()
	}
	var attempts []httpAttempt
	var failures []error
	for worker := 0; worker < 4; worker++ {
		got := httpAwait(t, results)
		attempts = append(attempts, got.attempts...)
		if got.err != nil {
			failures = append(failures, got.err)
		}
	}
	if len(failures) > 0 || len(attempts) != 64 {
		t.Fatalf("workload completed %d/64: %v", len(attempts), failures)
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].result.CommandSeq < attempts[j].result.CommandSeq })
	model, err := engine.New(f.run, f.config)
	if err != nil {
		t.Fatal(err)
	}
	seed := d.Command{RunID: f.run, AccountID: "bob", Kind: d.PlaceOrder, Place: httpDecode[d.Place](t, []byte(httpOrder("seed", "SELL", 100, 4)))}
	if _, err := model.Apply(1, seed); err != nil {
		t.Fatal(err)
	}
	balances := make(map[d.Int]map[string]d.Balances)
	for i, a := range attempts {
		seq := d.Int(i + 2)
		if a.result.CommandSeq != seq {
			t.Fatalf("duplicate/gap at sequence %d: %d", seq, a.result.CommandSeq)
		}
		want, err := model.Apply(seq, a.command)
		encoded, marshalErr := json.Marshal(want)
		if err != nil || marshalErr != nil || !bytes.Equal(a.response, encoded) {
			t.Fatalf("HTTP result differs from sequential input at %d: %v %v", seq, err, marshalErr)
		}
		balances[seq] = map[string]d.Balances{}
		for _, account := range []string{"alice", "bob"} {
			balances[seq][account], _ = model.Balances(account)
		}
	}
	for _, a := range attempts {
		want, found := balances[a.balance.AsOfSeq][a.command.AccountID]
		if !found || a.balance.BTC.AvailableLots != want.Base.Available || a.balance.BTC.ReservedLots != want.Base.Reserved || a.balance.USDT.AvailableAtoms != want.Quote.Available || a.balance.USDT.ReservedAtoms != want.Quote.Reserved {
			t.Fatalf("query is not its advertised command boundary: %+v want %+v", a.balance, want)
		}
	}
	c, cancel := httpContext()
	saved, err := r.Snapshot(c)
	cancel()
	if err != nil || saved.SnapshotSeq != 65 {
		t.Fatalf("snapshot after all HTTP results: %+v %v", saved, err)
	}
	e.close(t)
	httpStop(t, r)
	candidates, err := snapshot.List(f.dir)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("snapshot candidates: %+v %v", candidates, err)
	}
	before, err := os.ReadFile(filepath.Join(f.dir, "commands.wal"))
	if err != nil {
		t.Fatal(err)
	}
	r = httpOpen(t, f.dir, o, rt.HTTPTestHooks{})
	e = httpServe(t, r, nil)
	if r.Recovery().SnapshotSeq != 65 || r.Recovery().ReplayedCommands != 0 {
		t.Fatal("did not recover final snapshot", r.Recovery())
	}
	for _, a := range attempts {
		got := httpStatus(t, e.call(f.run, a.command.AccountID, "POST", a.path, a.body), 200)
		if !bytes.Equal(got, a.response) {
			t.Fatalf("recovered initial response changed at %d", a.result.CommandSeq)
		}
	}
	// Capture after restart and compare every order, FIFO, trade and cached input.
	c, cancel = httpContext()
	_, err = r.Snapshot(c)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	e.close(t)
	httpStop(t, r)
	after, err := os.ReadFile(filepath.Join(f.dir, "commands.wal"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retries changed WAL", err)
	}
	// Compare the republished complete checkpoint with sequential input replay.
	store, err := wal.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	g := store.Genesis()
	id := snapshot.Identity{RunID: f.run, ConfigHash: g.ConfigHash, RulesVersion: f.config.RulesVersion, ResponseVersion: f.config.ResponseVersion}
	doc, err := snapshot.Load(f.dir, candidates[0], id, 65)
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("snapshot read: %v %v", err, closeErr)
	}
	if !reflect.DeepEqual(doc.State, model.Capture()) {
		t.Fatal("recovered HTTP state differs from sequential input replay")
	}
}
