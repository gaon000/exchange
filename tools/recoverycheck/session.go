package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	rt "github.com/gaon000/exchange/internal/runtime"
)

type session struct {
	runtime *rt.Runtime
	server  *api.Server
	client  *http.Client
	base    string
	done    chan error
	trace   *json.Encoder
	step    *int
}

func openSession(dir string, trace *json.Encoder, step *int) (*session, error) {
	opts := rt.DefaultOptions()
	opts.BatchCommands = 1
	r, err := rt.Open(dir, opts)
	if err != nil {
		return nil, err
	}
	s := &session{runtime: r, trace: trace, step: step, done: make(chan error, 1)}
	s.server, err = api.New(r, api.DefaultOptions())
	if err != nil {
		return nil, errors.Join(err, s.close())
	}
	listener, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		s.server = nil
		return nil, errors.Join(err, s.close())
	}
	s.base = "http://" + listener.Addr().String()
	s.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go func() { s.done <- s.server.Serve(listener) }()
	return s, nil
}

func (s *session) close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.server != nil {
		err = s.server.Close()
		select {
		case serveErr := <-s.done:
			if !errors.Is(serveErr, http.ErrServerClosed) {
				err = errors.Join(err, serveErr)
			}
		case <-time.After(5 * time.Second):
			err = errors.Join(err, errors.New("HTTP shutdown deadline"))
		}
		s.server = nil
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	if s.runtime != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = errors.Join(err, s.runtime.Shutdown(ctx))
		cancel()
		s.runtime = nil
	}
	return err
}

func (s *session) request(ctx context.Context, run, account, method, path string, payload any, status int) ([]byte, error) {
	var b []byte
	var err error
	if payload != nil {
		b, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Run-ID", run)
	req.Header.Set("X-Account-ID", account)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.client.Do(req)
	var body []byte
	gotStatus := 0
	if res != nil {
		gotStatus = res.StatusCode
		var readErr error
		body, readErr = io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		err = errors.Join(err, readErr, res.Body.Close())
		if len(body) > 1<<20 {
			err = errors.Join(err, errors.New("HTTP response exceeds 1MiB"))
		}
	}
	if gotStatus != status {
		err = errors.Join(err, fmt.Errorf("HTTP %s %s: want %d got %d", method, path, status, gotStatus))
	}
	record := map[string]any{"step": *s.step, "method": method, "path": path, "account": account,
		"request": string(b), "status": gotStatus, "response": string(body)}
	if err != nil {
		record["error"] = err.Error()
	}
	return body, errors.Join(err, s.trace.Encode(record))
}

func (s *session) expect(ctx context.Context, run, account, path string, want any) error {
	b, err := s.request(ctx, run, account, "GET", path, nil, 200)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, encoded) {
		return fmt.Errorf("GET %s mismatch: want %s; got %s", path, encoded, b)
	}
	return nil
}

func commandPayload(c d.Command) (string, any) {
	if c.Kind == d.PlaceOrder {
		return "/v1/orders", c.Place
	}
	return "/v1/cancellations", c.Cancel
}

func (s *session) checkViews(ctx context.Context, run string, model *engine.Engine, first map[d.Key][]byte) error {
	state := model.State()
	for _, a := range state.Accounts {
		want := api.BalancesView{RunID: run, AsOfSeq: state.AppliedSeq,
			BTC:  api.BaseBalance{AvailableLots: a.Balances.Base.Available, ReservedLots: a.Balances.Base.Reserved},
			USDT: api.QuoteBalance{AvailableAtoms: a.Balances.Quote.Available, ReservedAtoms: a.Balances.Quote.Reserved}}
		if err := s.expect(ctx, run, a.AccountID, "/v1/balances", want); err != nil {
			return err
		}
	}
	for _, o := range state.Orders {
		if err := s.expect(ctx, run, o.Ref.AccountID, "/v1/orders/"+o.Ref.ClientOrderID,
			api.OrderView{RunID: run, AsOfSeq: state.AppliedSeq, Order: &o.Order}); err != nil {
			return err
		}
	}
	bids, asks, err := model.Book(100)
	if err != nil {
		return err
	}
	book := api.BookView{RunID: run, AsOfSeq: state.AppliedSeq, Bids: []api.Level{}, Asks: []api.Level{}}
	for _, b := range bids {
		book.Bids = append(book.Bids, api.Level{PriceTicks: b.PriceTicks, QuantityLots: b.QuantityLots})
	}
	for _, a := range asks {
		book.Asks = append(book.Asks, api.Level{PriceTicks: a.PriceTicks, QuantityLots: a.QuantityLots})
	}
	if err := s.expect(ctx, run, "alice", "/v1/book?limit=100", book); err != nil {
		return err
	}
	trades := api.TradesView{RunID: run, AsOfSeq: state.AppliedSeq, ThroughSeq: state.AppliedSeq, Trades: []api.Trade{}}
	for _, t := range state.Trades {
		trades.Trades = append(trades.Trades, api.Trade{TradeID: t.TradeID, PriceTicks: t.PriceTicks, QuantityLots: t.QuantityLots, TakerSide: t.TakerSide})
	}
	if err := s.expect(ctx, run, "alice", "/v1/trades?limit=1000", trades); err != nil {
		return err
	}
	for _, result := range state.Results {
		c := result.Command
		kind := "place"
		if c.Kind == d.CancelOrder {
			kind = "cancel"
		}
		body, err := s.request(ctx, run, c.AccountID, "GET", "/v1/results/"+kind+"/"+c.Key().RequestID, nil, 200)
		if err != nil || !bytes.Equal(body, first[c.Key()]) {
			return fmt.Errorf("first GET result changed for %+v: %w", c.Key(), errors.Join(err, same(string(first[c.Key()]), string(body), "first response")))
		}
		path, payload := commandPayload(c)
		body, err = s.request(ctx, run, c.AccountID, "POST", path, payload, 200)
		if err != nil || !bytes.Equal(body, first[c.Key()]) {
			return fmt.Errorf("first POST retry changed for %+v: %w", c.Key(), errors.Join(err, same(string(first[c.Key()]), string(body), "first response")))
		}
	}
	return nil
}
