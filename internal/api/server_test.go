package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

const testRun = "00000000-0000-4000-8000-000000000001"
const placeBody = `{"clientOrderId":"one","symbol":"BTC-USDT","side":"BUY","type":"LIMIT","timeInForce":"GTC","priceTicks":"100","quantityLots":"2"}`

type stubBackend struct {
	ready  atomic.Bool
	mu     sync.Mutex
	input  []d.Command
	query  []rt.Query
	submit func(context.Context, d.Command) (d.Result, error)
	read   func(context.Context, rt.Query) (rt.View, error)
}

func (b *stubBackend) Ready() bool { return b.ready.Load() }
func (b *stubBackend) Submit(ctx context.Context, c d.Command) (d.Result, error) {
	b.mu.Lock()
	b.input = append(b.input, c)
	b.mu.Unlock()
	if b.submit != nil {
		return b.submit(ctx, c)
	}
	return d.Result{SchemaVersion: 1, RunID: c.RunID, AccountID: c.AccountID, Kind: c.Kind, RequestID: c.Key().RequestID, CommandSeq: 1, Code: d.Accepted}, nil
}
func (b *stubBackend) Read(ctx context.Context, q rt.Query) (rt.View, error) {
	b.mu.Lock()
	b.query = append(b.query, q)
	b.mu.Unlock()
	if b.read != nil {
		return b.read(ctx, q)
	}
	return rt.View{RunID: q.RunID}, nil
}
func newStub(t *testing.T, options Options) (*Server, *stubBackend) {
	t.Helper()
	b := &stubBackend{}
	b.ready.Store(true)
	s, err := New(b, options)
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}
func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.Header.Set("X-Run-ID", testRun)
	r.Header.Set("X-Account-ID", "alice")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}
func serve(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func assertFailure(t *testing.T, w *httptest.ResponseRecorder, status int, code string, outcome rt.Outcome) {
	t.Helper()
	want := fmt.Sprintf(`{"error":{"code":%q,"outcome":%q}}`, code, outcome)
	if w.Code != status || w.Body.String() != want {
		t.Fatalf("want %d %s; got %d %s", status, want, w.Code, w.Body.String())
	}
}
func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("channel deadline")
		var zero T
		return zero
	}
}

func TestStrictCommandInputBeforeRuntime(t *testing.T) {
	bad := map[string]string{
		"duplicate":         strings.Replace(placeBody, `"priceTicks":"100"`, `"priceTicks":"100","priceTicks":"100"`, 1),
		"escaped duplicate": strings.Replace(placeBody, `"priceTicks":"100"`, `"priceTicks":"100","price\u0054icks":"100"`, 1),
		"wrong case":        strings.Replace(placeBody, "priceTicks", "PriceTicks", 1),
		"unknown":           strings.Replace(placeBody, "}", `,"accountId":"bob"}`, 1),
		"missing":           strings.Replace(placeBody, `"symbol":"BTC-USDT",`, "", 1),
		"number":            strings.Replace(placeBody, `"100"`, "100", 1),
		"null":              strings.Replace(placeBody, `"100"`, "null", 1),
		"fraction":          strings.Replace(placeBody, `"100"`, `"1.0"`, 1),
		"exponent":          strings.Replace(placeBody, `"100"`, `"1e2"`, 1),
		"plus":              strings.Replace(placeBody, `"100"`, `"+100"`, 1),
		"space":             strings.Replace(placeBody, `"100"`, `" 100"`, 1),
		"overflow":          strings.Replace(placeBody, `"100"`, `"9223372036854775808"`, 1),
		"zero":              strings.Replace(placeBody, `"100"`, `"0"`, 1),
		"range":             strings.Replace(placeBody, `"100"`, `"100000001"`, 1),
		"lower side":        strings.Replace(placeBody, "BUY", "buy", 1),
		"wrong type":        strings.Replace(placeBody, "LIMIT", "MARKET", 1),
		"bad ID":            strings.Replace(placeBody, "one", "a/b", 1),
		"invalid UTF8":      strings.Replace(placeBody, "one", string([]byte{0xff}), 1),
		"trailing":          placeBody + "{}",
		"array":             "[" + placeBody + "]", "empty": "", "null root": "null",
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, b := newStub(t, DefaultOptions())
			assertFailure(t, serve(s, request("POST", "/v1/orders", body)), 400, "INVALID_REQUEST", rt.NotAdmitted)
			if len(b.input) != 0 {
				t.Fatal("invalid input reached runtime")
			}
		})
	}
	for _, body := range []string{placeBody, strings.Replace(placeBody, `"100"`, `"000100"`, 1), " \n" + strings.Replace(placeBody, "one", `\u006fne`, 1) + " \n"} {
		s, b := newStub(t, DefaultOptions())
		w := serve(s, request("POST", "/v1/orders", body))
		if w.Code != 200 || len(b.input) != 1 || b.input[0].Place.PriceTicks != 100 || b.input[0].Place.ClientOrderID != "one" {
			t.Fatalf("normalization: %s", w.Body.String())
		}
	}
	s, b := newStub(t, DefaultOptions())
	w := serve(s, request("POST", "/v1/cancellations", `{"cancelRequestId":"cancel","clientOrderId":"one"}`))
	if w.Code != 200 || len(b.input) != 1 || b.input[0].Kind != d.CancelOrder || b.input[0].Place != (d.Place{}) {
		t.Fatal("cancel mapping")
	}
}

func TestBodyLimitsAndMedia(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		for _, chunked := range []bool{false, true} {
			s, b := newStub(t, DefaultOptions())
			r := request("POST", "/v1/orders", placeBody+strings.Repeat(" ", size-len(placeBody)))
			if chunked {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			w := serve(s, r)
			if size == 4097 {
				assertFailure(t, w, 413, "BODY_TOO_LARGE", rt.NotAdmitted)
				if len(b.input) != 0 {
					t.Fatal("oversize body admitted")
				}
			} else if w.Code != 200 || len(b.input) != 1 {
				t.Fatal("exact body limit rejected")
			}
		}
	}
	for _, media := range []string{"", "text/plain", "application/json; charset=latin1", "application/json; other=utf-8", "application/json; charset=utf-8; x=1"} {
		s, b := newStub(t, DefaultOptions())
		r := request("POST", "/v1/orders", placeBody)
		r.Header.Set("Content-Type", media)
		assertFailure(t, serve(s, r), 415, "UNSUPPORTED_MEDIA_TYPE", rt.NotAdmitted)
		if len(b.input) != 0 {
			t.Fatal("media rejection admitted")
		}
	}
	for _, header := range []string{"Content-Type", "Content-Encoding"} {
		s, _ := newStub(t, DefaultOptions())
		r := request("POST", "/v1/orders", placeBody)
		r.Header.Add(header, "application/json")
		assertFailure(t, serve(s, r), 415, "UNSUPPORTED_MEDIA_TYPE", rt.NotAdmitted)
	}
	s, _ := newStub(t, DefaultOptions())
	r := request("POST", "/v1/orders", placeBody)
	r.Header.Set("Content-Type", "Application/JSON; charset=UTF-8")
	r.Header.Set("Content-Encoding", "identity")
	if w := serve(s, r); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestLocalIdentityReadinessAndRoutes(t *testing.T) {
	for _, host := range []string{"example.com", "localhost", "0.0.0.0", "192.168.1.1:8080", "127.0.0.1.evil", "127.0.0.1:bad", "[::1%lo0]:8080", "[::]:8080", "127.0.0.1:65536"} {
		s, b := newStub(t, DefaultOptions())
		r := request("POST", "/v1/orders", placeBody)
		r.Host = host
		assertFailure(t, serve(s, r), 403, "HOST_FORBIDDEN", rt.NotAdmitted)
		if len(b.input) != 0 {
			t.Fatal("forbidden host admitted")
		}
	}
	for _, host := range []string{"127.0.0.1", "127.0.0.2:8080", "[::1]", "[::1]:8080", "[::ffff:127.0.0.1]:8080"} {
		s, _ := newStub(t, DefaultOptions())
		r := request("GET", "/health/live", "")
		r.Host = host
		if serve(s, r).Code != 200 {
			t.Fatal(host)
		}
	}
	for _, origin := range []string{"", "null", "http://127.0.0.1"} {
		s, _ := newStub(t, DefaultOptions())
		r := request("GET", "/health/live", "")
		r.Header.Set("Origin", origin)
		assertFailure(t, serve(s, r), 403, "ORIGIN_FORBIDDEN", rt.NotAdmitted)
	}
	for _, header := range []string{"X-Run-ID", "X-Account-ID"} {
		for _, value := range []string{"", "bad value", "duplicate"} {
			s, b := newStub(t, DefaultOptions())
			r := request("POST", "/v1/orders", placeBody)
			if value == "duplicate" {
				r.Header.Add(header, r.Header.Get(header))
			} else {
				r.Header.Set(header, value)
			}
			assertFailure(t, serve(s, r), 400, "INVALID_REQUEST", rt.NotAdmitted)
			if len(b.input) != 0 {
				t.Fatal("bad identity admitted")
			}
		}
	}
	s, b := newStub(t, DefaultOptions())
	b.ready.Store(false)
	for _, path := range []string{"/v1/orders", "/v1/balances", "/v1/results/place/one", "/health/ready"} {
		method, body := "GET", ""
		if path == "/v1/orders" {
			method, body = "POST", placeBody
		}
		assertFailure(t, serve(s, request(method, path, body)), 503, "NOT_READY", rt.NotAdmitted)
	}
	if w := serve(s, request("GET", "/health/live", "")); w.Code != 200 || w.Body.String() != `{"state":"LIVE"}` {
		t.Fatal(w.Body.String())
	}
	if len(b.input)+len(b.query) != 0 {
		t.Fatal("unready backend consulted")
	}
	assertFailure(t, serve(s, request("GET", "/unknown", "")), 404, "ROUTE_NOT_FOUND", rt.NotAdmitted)
	w := serve(s, request("DELETE", "/v1/orders", ""))
	assertFailure(t, w, 405, "METHOD_NOT_ALLOWED", rt.NotAdmitted)
	if w.Header().Get("Allow") != "POST" {
		t.Fatal("missing Allow")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, serve(s, request("GET", "/health/live", "")), 503, "NOT_READY", rt.NotAdmitted)
}

func TestQueryParsingAndPublicViews(t *testing.T) {
	for _, path := range []string{
		"/v1/orders/", "/v1/orders/a/b", "/v1/results/place/", "/v1/results/cancel/bad.id",
		"/v1/book?limit=0", "/v1/book?limit=101", "/v1/book?limit=1&limit=2", "/v1/book?limit=1.0", "/v1/book?limit=%ff",
		"/v1/book?other=1", "/v1/balances?limit=1", "/v1/orders/one?x=1", "/v1/trades?limit=1001", "/v1/trades?afterSeq=1",
		"/v1/trades?afterSeq=1&afterIndex=1", "/v1/trades?afterSeq=1&afterIndex=0&throughSeq=1", "/v1/trades?afterSeq=2&afterIndex=1&throughSeq=1",
		"/v1/trades?afterSeq=0&afterIndex=0&throughSeq=1", "/v1/trades?throughSeq=1;limit=2", "/health/live?limit=1",
	} {
		s, b := newStub(t, DefaultOptions())
		assertFailure(t, serve(s, request("GET", path, "")), 400, "INVALID_REQUEST", rt.NotAdmitted)
		if len(b.query) != 0 {
			t.Fatal("invalid query forwarded", path)
		}
	}
	s, b := newStub(t, DefaultOptions())
	assertFailure(t, serve(s, request("GET", "/v1/balances", "body")), 400, "INVALID_REQUEST", rt.NotAdmitted)
	for _, path := range []string{"/v1/book", "/v1/trades", "/v1/trades?afterSeq=0002&afterIndex=01&throughSeq=3&limit=1"} {
		if w := serve(s, request("GET", path, "")); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if b.query[0].Limit != 20 || b.query[1].Limit != 100 || b.query[2].AfterSeq != 2 || b.query[2].AfterIndex != 1 || *b.query[2].ThroughSeq != 3 {
		t.Fatal("query normalization")
	}
	b.read = func(_ context.Context, q rt.Query) (rt.View, error) {
		return rt.View{RunID: testRun, AsOfSeq: 7, ThroughSeq: 4, Trades: []d.Trade{{TradeID: testRun + ":4:1", CommandSeq: 4, MatchIndex: 1, TakerSide: d.Sell, PriceTicks: 100, QuantityLots: 2, Maker: d.OrderRef{AccountID: "private-maker"}, Taker: d.OrderRef{AccountID: "private-taker"}}}}, nil
	}
	w := serve(s, request("GET", "/v1/trades?limit=1&throughSeq=4", ""))
	want := `{"runId":"` + testRun + `","asOfSeq":"7","throughSeq":"4","trades":[{"tradeId":"` + testRun + `:4:1","priceTicks":"100","quantityLots":"2","takerSide":"SELL"}],"nextCursor":{"afterSeq":"4","afterIndex":"1"}}`
	if w.Body.String() != want {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private data/cache")
	}
	b.read = func(_ context.Context, q rt.Query) (rt.View, error) { return rt.View{RunID: testRun}, nil }
	for _, path := range []string{"/v1/book", "/v1/trades"} {
		w := serve(s, request("GET", path, ""))
		if bytes.Contains(w.Body.Bytes(), []byte(`:null`)) && path == "/v1/book" || !bytes.Contains(w.Body.Bytes(), []byte(`:[]`)) {
			t.Fatal("empty arrays", w.Body.String())
		}
	}
}

func TestRuntimeErrorsAndOriginalResultWire(t *testing.T) {
	for code, status := range map[string]int{"INVALID_REQUEST": 400, "UNKNOWN_ACCOUNT": 403, "RUN_MISMATCH": 409, "IDEMPOTENCY_CONFLICT": 409, "RUN_LIMIT_REACHED": 409, "QUEUE_FULL": 429, "TOO_MANY_REQUESTS": 429, "DUPLICATE_WAITERS_FULL": 429, "NOT_READY": 503, "STORAGE_UNAVAILABLE": 503, "INTERNAL_FAILURE": 503, "RESULT_TIMEOUT": 504} {
		s, b := newStub(t, DefaultOptions())
		outcome := rt.NotAdmitted
		if code == "RESULT_TIMEOUT" || code == "STORAGE_UNAVAILABLE" || code == "INTERNAL_FAILURE" {
			outcome = rt.Unknown
		}
		b.submit = func(context.Context, d.Command) (d.Result, error) {
			return d.Result{}, &rt.Failure{Code: code, Outcome: outcome, Cause: errors.New("private path/token diagnostic")}
		}
		assertFailure(t, serve(s, request("POST", "/v1/orders", placeBody)), status, code, outcome)
	}
	s, b := newStub(t, DefaultOptions())
	for _, state := range []string{"PENDING", "UNKNOWN"} {
		b.read = func(context.Context, rt.Query) (rt.View, error) { return rt.View{ResultState: state}, nil }
		w := serve(s, request("GET", "/v1/results/place/one", ""))
		status := 202
		if state == "UNKNOWN" {
			status = 404
		}
		if w.Code != status || w.Body.String() != fmt.Sprintf(`{"state":%q}`, state) {
			t.Fatal(w.Body.String())
		}
	}
	first := d.Result{SchemaVersion: 1, RunID: testRun, AccountID: "alice", Kind: d.PlaceOrder, RequestID: "one", CommandSeq: 1, Code: d.InsufficientBalance, Order: &d.Order{Status: d.Rejected, Reason: d.InsufficientFunds}}
	b.submit = func(context.Context, d.Command) (d.Result, error) { return first, nil }
	b.read = func(context.Context, rt.Query) (rt.View, error) { return rt.View{AsOfSeq: 99, Result: &first}, nil }
	a := serve(s, request("POST", "/v1/orders", placeBody))
	c := serve(s, request("GET", "/v1/results/place/one", ""))
	expected, _ := json.Marshal(first)
	if a.Code != 200 || c.Code != 200 || !bytes.Equal(a.Body.Bytes(), expected) || !bytes.Equal(c.Body.Bytes(), expected) {
		t.Fatal("first body changed or rejection mapped to HTTP error")
	}
}

type blockedWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

type blockedFlushWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

func (w *blockedFlushWriter) FlushError() error {
	close(w.entered)
	<-w.release
	w.ResponseRecorder.Flush()
	return nil
}

func TestAdmissionSlotHeldUntilBufferedResponseFlushed(t *testing.T) {
	opts := DefaultOptions()
	opts.RequestLimit = 1
	s, _ := newStub(t, opts)
	w := &blockedFlushWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(w.release) })
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(w, request("POST", "/v1/orders", placeBody)) }()
	receive(t, w.entered)
	assertFailure(t, serve(s, request("GET", "/v1/balances", "")), 429, "TOO_MANY_REQUESTS", rt.NotAdmitted)
	once.Do(func() { close(w.release) })
	receive(t, done)
	if len(s.slots) != 0 || !w.Flushed {
		t.Fatal("buffered response released admission before flush")
	}
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestAdmissionSlotHeldThroughSendingAndResultTimeout(t *testing.T) {
	opts := DefaultOptions()
	opts.RequestLimit = 1
	opts.ResultTimeout = 10 * time.Millisecond
	s, b := newStub(t, opts)
	w := &blockedWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(w.release) })
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(w, request("POST", "/v1/orders", placeBody)) }()
	receive(t, w.entered)
	assertFailure(t, serve(s, request("GET", "/v1/balances", "")), 429, "TOO_MANY_REQUESTS", rt.NotAdmitted)
	once.Do(func() { close(w.release) })
	receive(t, done)
	if len(s.slots) != 0 {
		t.Fatal("slot not released")
	}
	b.submit = func(ctx context.Context, _ d.Command) (d.Result, error) {
		<-ctx.Done()
		return d.Result{}, &rt.Failure{Code: "RESULT_TIMEOUT", Outcome: rt.Unknown, Cause: ctx.Err()}
	}
	assertFailure(t, serve(s, request("POST", "/v1/orders", placeBody)), 504, "RESULT_TIMEOUT", rt.Unknown)
	if len(s.slots) != 0 {
		t.Fatal("timeout leaked slot")
	}
}

func TestOptionsAndLoopbackValidation(t *testing.T) {
	for _, modify := range []func(*Options){func(o *Options) { o.RequestLimit = 0 }, func(o *Options) { o.ReadHeaderTimeout = 0 }, func(o *Options) { o.ReadBodyTimeout = -1 }, func(o *Options) { o.ResultTimeout = 2 * time.Hour }, func(o *Options) { o.WriteTimeout = 0 }} {
		o := DefaultOptions()
		modify(&o)
		if _, err := New(&stubBackend{}, o); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := New(nil, DefaultOptions()); err == nil {
		t.Fatal("nil backend")
	}
	for _, addr := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "localhost:8080", "192.168.1.2:8080", "127.0.0.1:bad", "[::1%lo0]:8080"} {
		if l, err := ListenLoopback(addr); err == nil {
			_ = l.Close()
			t.Fatal("invalid listen address", addr)
		}
	}
}
