package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

const testRun = "11111111-1111-4111-8111-111111111111"

func baseArgs(endpoint, command string) []string {
	return []string{command, "--url", endpoint, "--run-id", testRun, "--account", "alice"}
}

func testRequest(endpoint string) savedRequest {
	return savedRequest{Version: 1, URL: endpoint, RunID: testRun, Account: "alice", Kind: d.PlaceOrder, Place: &d.Place{ClientOrderID: "buy", Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 6_000_000, QuantityLots: 100}}
}

func resultBytes(r savedRequest) []byte {
	_, _, id := r.paths()
	v := d.Result{SchemaVersion: 1, RunID: r.RunID, AccountID: r.Account, Kind: r.Kind, RequestID: id, CommandSeq: 1, Code: d.Accepted, Order: &d.Order{ClientOrderID: id, Symbol: d.Symbol, Side: d.Buy, PriceTicks: 6_000_000, OriginalQty: 100, OpenQty: 100, Status: d.Open, CreatedSeq: 1, UpdatedSeq: 1}}
	if r.Kind == d.CancelOrder {
		v.Code = d.OrderNotFound
		v.Order = nil
	}
	b, _ := json.Marshal(v)
	return b
}

func TestDecimalAndArgumentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		s      string
		places int
		want   d.Int
	}{{"60000", 2, 6000000}, {"0.01", 2, 1}, {"0001.20", 2, 120}, {"0.0001", 4, 1}, {"100", 4, 1000000}} {
		n, err := decimalUnits(tc.s, tc.places)
		if err != nil || n != tc.want {
			t.Fatal(tc, n, err)
		}
	}
	for _, s := range []string{"", ".01", "1.", "1.001", "-1", "+1", " 1", "1e2", "NaN", "１", "9223372036854775807"} {
		if _, err := decimalUnits(s, 2); err == nil {
			t.Fatal("invalid decimal", s)
		}
	}
	for _, args := range [][]string{
		{"bad"}, {"retry"}, {"retry", "--request", "x", "--account", "bob"},
		baseArgs("http://localhost:8080", "balances"),
		append(baseArgs("http://127.0.0.1:8080", "result"), "--request", "x"),
		append(baseArgs("http://127.0.0.1:8080", "book"), "--limit", "101"),
		append(baseArgs("http://127.0.0.1:8080", "trades"), "--after-seq", "1", "--after-index", "1"),
		append(baseArgs("http://127.0.0.1:8080", "trades"), "--through-seq", "0", "--after-seq", "0", "--after-index", "1"),
		append(baseArgs("http://127.0.0.1:8080", "order"), "--id", "../x"),
		append(baseArgs("http://127.0.0.1:8080", "balances"), "--timeout", "0"),
	} {
		if _, err := parse(args, io.Discard); err == nil {
			t.Fatal("accepted", args)
		}
	}
	for _, endpoint := range []string{"https://127.0.0.1:80", "http://user@127.0.0.1:80", "http://127.0.0.1:80/path", "http://127.0.0.1:80?x=1", "http://127.0.0.1:0", "http://192.0.2.1:80"} {
		if err := validateContext(endpoint, testRun, "alice"); err == nil {
			t.Fatal(endpoint)
		}
	}
	for _, args := range [][]string{{"-h"}, {"place", "-h"}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	for _, fields := range [][]string{{"--price", "0", "--quantity", "0.01"}, {"--price", "1000000.01", "--quantity", "0.01"}, {"--price", "1", "--quantity", "100.0001"}, {"--price", "1.001", "--quantity", "0.01"}} {
		o, err := parse(append(append(baseArgs("http://127.0.0.1:8080", "place"), "--id", "buy", "--side", "BUY"), fields...), io.Discard)
		if err == nil {
			_, err = newRequest(o)
		}
		if err == nil {
			t.Fatal(fields)
		}
	}
}

func TestSavedRequestBeforeSendAndExactRetry(t *testing.T) {
	for _, lookup := range []string{"complete", "pending", "unknown", "mismatch", "unavailable", "timeout", "conflict"} {
		t.Run(lookup, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "request.json")
			var mu sync.Mutex
			var methods []string
			var bodies [][]byte
			var endpoint string
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				methods = append(methods, req.Method)
				if req.Header.Get("X-Run-ID") != testRun || req.Header.Get("X-Account-ID") != "alice" {
					t.Error("wrong context")
				}
				r, err := loadRequest(path)
				if err != nil {
					t.Error("request was not saved before send", err)
					w.WriteHeader(500)
					return
				}
				if req.Method == "GET" {
					switch lookup {
					case "pending":
						w.WriteHeader(202)
						fmt.Fprint(w, `{"state":"PENDING"}`)
						return
					case "unknown":
						w.WriteHeader(404)
						fmt.Fprint(w, `{"state":"UNKNOWN"}`)
						return
					case "mismatch":
						r.RunID = "22222222-2222-4222-8222-222222222222"
					case "unavailable":
						w.WriteHeader(503)
						fmt.Fprint(w, `{"error":{"code":"NOT_READY","outcome":"NOT_ADMITTED"}}`)
						return
					case "timeout":
						<-req.Context().Done()
						return
					}
				} else {
					b, _ := io.ReadAll(req.Body)
					bodies = append(bodies, b)
					if !bytes.Equal(b, r.body()) {
						t.Error("different retry body", string(b))
					}
					if lookup == "conflict" && len(bodies) > 1 {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"error":{"code":"IDEMPOTENCY_CONFLICT","outcome":"NOT_ADMITTED"}}`)
						return
					}
				}
				_, _ = w.Write(resultBytes(r))
			}))
			defer s.Close()
			endpoint = s.URL
			args := append(baseArgs(endpoint, "place"), "--id", "buy", "--side", "BUY", "--price", "60000", "--quantity", "0.01", "--request", path)
			var first bytes.Buffer
			if err := run(context.Background(), args, &first, io.Discard); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			var retried bytes.Buffer
			err := run(context.Background(), []string{"retry", "--request", path, "--timeout", "100ms"}, &retried, io.Discard)
			wantSuccess := lookup == "complete" || lookup == "pending" || lookup == "unknown"
			if (err == nil) != wantSuccess {
				t.Fatal(err)
			}
			if wantSuccess && !bytes.Equal(first.Bytes(), retried.Bytes()) {
				t.Fatal("first response changed")
			}
			mu.Lock()
			count := len(bodies)
			gotMethods := strings.Join(methods, ",")
			mu.Unlock()
			wantCount := 1
			if wantSuccess || lookup == "conflict" {
				wantCount = 2
			}
			if count != wantCount || !strings.HasPrefix(gotMethods, "POST,GET") {
				t.Fatal(gotMethods, count)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("saved request changed")
			}
			if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
				t.Fatal("overwrote request")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal(info.Mode())
			}
		})
	}
}

func TestTimeoutAndLostResponsePreserveRequest(t *testing.T) {
	for _, mode := range []string{"deadline", "disconnect", "truncated", "redirect", "too-large", "bad-identity", "not-admitted", "server-unknown"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "request.json")
			var mu sync.Mutex
			var stored []byte
			posts := 0
			redirected := false
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				_, _ = io.Copy(io.Discard, req.Body)
				if req.URL.Path == "/redirect" {
					redirected = true
					return
				}
				r, err := loadRequest(path)
				if err != nil {
					t.Error(err)
					return
				}
				if req.Method == "GET" {
					_, _ = w.Write(stored)
					return
				}
				posts++
				stored = resultBytes(r)
				if posts > 1 {
					_, _ = w.Write(stored)
					return
				}
				switch mode {
				case "deadline":
					<-req.Context().Done()
				case "disconnect":
					c, _, _ := w.(http.Hijacker).Hijack()
					_ = c.Close()
				case "truncated":
					w.Header().Set("Content-Length", "100000")
					fmt.Fprint(w, `{"schemaVersion":`)
				case "redirect":
					http.Redirect(w, req, "/redirect", 302)
				case "too-large":
					fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
				case "bad-identity":
					r.Account = "bob"
					_, _ = w.Write(resultBytes(r))
				case "not-admitted":
					w.WriteHeader(429)
					fmt.Fprint(w, `{"error":{"code":"QUEUE_FULL","outcome":"NOT_ADMITTED"}}`)
				case "server-unknown":
					w.WriteHeader(504)
					fmt.Fprint(w, `{"error":{"code":"RESULT_TIMEOUT","outcome":"UNKNOWN"}}`)
				}
			}))
			defer s.Close()
			args := append(baseArgs(s.URL, "place"), "--id", "buy", "--side", "BUY", "--price", "60000", "--quantity", "0.01", "--request", path, "--timeout", "100ms")
			if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
				t.Fatal("uncertain/error response accepted")
			}
			if _, err := loadRequest(path); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := run(context.Background(), []string{"result", "--request", path}, &got, io.Discard); err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background(), []string{"retry", "--request", path}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if posts != 2 || redirected || !bytes.Equal(bytes.TrimSpace(got.Bytes()), stored) {
				t.Fatal(posts, redirected)
			}
		})
	}
}

func TestRequestAndQueryValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	r := testRequest("http://127.0.0.1:8080")
	b, _ := json.Marshal(r)
	for _, data := range []string{`{}`, string(b) + "{}", strings.Replace(string(b), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(b), `"version":1`, `"version":2`, 1), strings.Replace(string(b), `"quantityLots":"100"`, `"quantityLots":"0"`, 1), strings.Repeat("x", 8193)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRequest(path); err == nil {
			t.Fatal("invalid request file accepted", data[:min(len(data), 80)])
		}
	}
	for _, response := range []string{`{"state":"UNKNOWN"}`, `{"state":"PENDING"}`, `{"runId":"wrong"}`, `<html>error</html>`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
		if err := run(context.Background(), baseArgs(s.URL, "balances"), io.Discard, io.Discard); err == nil {
			t.Fatal(response)
		}
		s.Close()
	}
	o, err := parse(append(baseArgs("http://127.0.0.1:8080", "trades"), "--limit", "2", "--through-seq", "3", "--after-seq", "2", "--after-index", "1"), io.Discard)
	if err != nil || o.queryPath() != "/v1/trades?afterIndex=1&afterSeq=2&limit=2&throughSeq=3" {
		t.Fatal(o, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, append(baseArgs("http://127.0.0.1:8080", "place"), "--id", "buy", "--side", "BUY", "--price", "1", "--quantity", "1", "--request", path), io.Discard, io.Discard); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestServerReportAndNoSendOnFileFailure(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "ready.json")
	if err := os.WriteFile(report, []byte(fmt.Sprintf(`{"event":"ready","runId":%q,"listen":"127.0.0.1:8080","recovery":{}}`, testRun)), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := parse([]string{"balances", "--server-report", report, "--account", "alice"}, io.Discard)
	if err != nil || o.runID != testRun || o.endpoint != "http://127.0.0.1:8080" {
		t.Fatal(o, err)
	}
	if _, err := parse([]string{"balances", "--server-report", report, "--account", "alice", "--run-id", testRun}, io.Discard); err == nil {
		t.Fatal("override accepted")
	}
	// An existing file must stop the command before any network call.
	err = run(context.Background(), append(baseArgs("http://127.0.0.1:1", "cancel"), "--id", "cancel-1", "--order-id", "buy", "--request", report), io.Discard, io.Discard)
	if !os.IsExist(err) {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := printJSON(&output, []byte(`not json`)); err == nil || output.Len() != 0 {
		t.Fatal("invalid JSON printed")
	}
}
