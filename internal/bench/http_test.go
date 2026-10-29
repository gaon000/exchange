package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

func httpRequest(t *testing.T) Request {
	t.Helper()
	p, err := Build("12345678-1234-4234-8234-123456789abc", testSpec())
	if err != nil {
		t.Fatal(err)
	}
	return p.Preparation()[0]
}

func accepted(r Request) d.Result {
	p := r.Command.Place
	return d.Result{SchemaVersion: 1, RunID: r.Command.RunID, AccountID: r.Command.AccountID, Kind: d.PlaceOrder, RequestID: p.ClientOrderID, CommandSeq: 1, Code: d.Accepted, TradeCount: 0, Order: &d.Order{ClientOrderID: p.ClientOrderID, Symbol: d.Symbol, Side: p.Side, PriceTicks: p.PriceTicks, OriginalQty: p.QuantityLots, OpenQty: p.QuantityLots, Status: d.Open, CreatedSeq: 1, UpdatedSeq: 1}}
}

func TestHTTPValidatesCompleteBusinessResult(t *testing.T) {
	r := httpRequest(t)
	good, _ := json.Marshal(accepted(r))
	for name, edit := range map[string]func(*d.Result){
		"run":     func(v *d.Result) { v.RunID = "other" },
		"account": func(v *d.Result) { v.AccountID = "other" },
		"key":     func(v *d.Result) { v.RequestID = "other" },
		"version": func(v *d.Result) { v.SchemaVersion = 2 },
		"seq":     func(v *d.Result) { v.CommandSeq = 0 },
		"code":    func(v *d.Result) { v.Code = d.InsufficientBalance },
		"order":   func(v *d.Result) { v.Order = nil },
		"payload": func(v *d.Result) { v.Order.PriceTicks++ },
		"sum":     func(v *d.Result) { v.Order.OpenQty-- },
		"trades":  func(v *d.Result) { v.TradeCount = 1 },
		"reason":  func(v *d.Result) { v.Order.Reason = d.UserCancel },
		"updated": func(v *d.Result) { v.Order.UpdatedSeq++ },
	} {
		t.Run(name, func(t *testing.T) {
			v := accepted(r)
			edit(&v)
			b, _ := json.Marshal(v)
			o := classify(r, wireResponse{status: 200, body: b})
			if o.Class != InvalidResponse || o.Certainty != "UNKNOWN" {
				t.Fatalf("accepted: %+v", o)
			}
		})
	}
	for _, b := range [][]byte{[]byte(`{}`), []byte(`null`), append(append([]byte{}, good...), good...), []byte(strings.Replace(string(good), `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1)), []byte(strings.Replace(string(good), `"tradeCount":"0"`, `"tradeCount":0`, 1))} {
		if classify(r, wireResponse{status: 200, body: b}).Class != InvalidResponse {
			t.Fatalf("malformed response accepted: %s", b)
		}
	}
	if o := classify(r, wireResponse{status: 200, body: good}); o.Class != Business || len(o.Fingerprint) != 64 {
		t.Fatalf("valid result: %+v", o)
	}
}

func TestHTTPErrorContracts(t *testing.T) {
	r := httpRequest(t)
	for _, tc := range []struct {
		status          int
		code, certainty string
		class           ResponseClass
	}{
		{429, "QUEUE_FULL", "NOT_ADMITTED", Overload}, {504, "RESULT_TIMEOUT", "UNKNOWN", ServerTimeout}, {503, "STORAGE_UNAVAILABLE", "UNKNOWN", ServerFailure}, {503, "NOT_READY", "NOT_ADMITTED", ServerFailure}, {409, "IDEMPOTENCY_CONFLICT", "NOT_ADMITTED", ServerFailure}, {429, "QUEUE_FULL", "UNKNOWN", InvalidResponse}, {503, "STORAGE_UNAVAILABLE", "NOT_ADMITTED", InvalidResponse}, {200, "QUEUE_FULL", "NOT_ADMITTED", InvalidResponse}, {429, "mystery", "NOT_ADMITTED", InvalidResponse}, {302, "NOT_READY", "NOT_ADMITTED", InvalidResponse},
	} {
		body := []byte(fmt.Sprintf(`{"error":{"code":%q,"outcome":%q}}`, tc.code, tc.certainty))
		if o := classify(r, wireResponse{status: tc.status, body: body}); o.Class != tc.class {
			t.Fatalf("%+v: %+v", tc, o)
		}
	}
}

func TestHTTPTransportIdentityLimitsAndNoRedirect(t *testing.T) {
	r := httpRequest(t)
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	for _, mode := range []string{"ok", "partial", "large", "media", "redirect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("X-Run-ID") != r.Command.RunID || req.Header.Get("X-Account-ID") != r.Command.AccountID || req.URL.Path != r.Path || req.Method != "POST" {
					t.Errorf("wrong request identity")
				}
				body, _ := io.ReadAll(req.Body)
				if string(body) != r.Body {
					t.Errorf("changed body")
				}
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "ok":
					_ = json.NewEncoder(w).Encode(accepted(r))
				case "partial":
					w.Header().Set("Content-Length", "10000")
					_, _ = io.WriteString(w, `{"schemaVersion":1`)
				case "large":
					_, _ = io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
				case "media":
					w.Header().Set("Content-Type", "text/plain")
					_ = json.NewEncoder(w).Encode(accepted(r))
				case "redirect":
					http.Redirect(w, req, destination.URL, 307)
				case "timeout":
					<-req.Context().Done()
				}
			}))
			defer s.Close()
			e, err := NewHTTPExecutor(s.URL, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			x := e.Execute(ctx, r)
			want := InvalidResponse
			switch mode {
			case "ok":
				want = Business
			case "partial":
				want = TransportFailure
			case "timeout":
				want = TransportTimeout
			}
			if x.Outcome.Class != want {
				t.Fatalf("%+v", x)
			}
			if x.WrittenAt.IsZero() {
				t.Fatal("missing write observation")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestHTTPDestinationAndDeadline(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:1", "http://localhost:1", "http://0.0.0.0:1", "http://192.0.2.1:80", "http://127.0.0.1:0", "http://127.0.0.1:01", "http://user@127.0.0.1:80", "http://127.0.0.1:80/x", "http://127.0.0.1:80?", "http://127.0.0.1:80#x"} {
		if e, err := NewHTTPExecutor(raw, 2); err == nil {
			e.Close()
			t.Fatalf("accepted %s", raw)
		}
	}
	e, err := NewHTTPExecutor("http://[::1]:8080/", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if x := e.Execute(context.Background(), httpRequest(t)); x.Outcome.Class != InvalidResponse {
		t.Fatalf("unbounded execution: %+v", x)
	}
}
