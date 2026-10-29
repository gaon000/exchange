package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

const maxResponseBytes = 64 << 10

// HTTPExecutor sends prepared Plan requests to one numeric loopback HTTP/1.1
// server. It uses no proxy, redirects, application retries or server internals.
// Call Close after all executions join. Methods support concurrent requests.
type HTTPExecutor struct {
	base      string
	client    *http.Client
	transport *http.Transport
}

// NewHTTPExecutor validates the destination before opening any connection.
// maxInFlight should equal the scheduler's limit to avoid an extra pool queue.
func NewHTTPExecutor(base string, maxInFlight int) (*HTTPExecutor, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return nil, fmt.Errorf("URL must be http://numeric-loopback:port without credentials, query or path")
	}
	host, port, err := net.SplitHostPort(u.Host)
	ip, ipErr := netip.ParseAddr(host)
	p, portErr := strconv.Atoi(port)
	if err != nil || ipErr != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() || portErr != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port || maxInFlight < 1 || maxInFlight > 65536 {
		return nil, fmt.Errorf("invalid loopback destination or concurrency limit")
	}
	t := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxConnsPerHost: maxInFlight, MaxIdleConns: maxInFlight, MaxIdleConnsPerHost: maxInFlight, IdleConnTimeout: 30 * time.Second, DisableCompression: true, MaxResponseHeaderBytes: 8 << 10}
	c := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &HTTPExecutor{base: "http://" + u.Host, client: c, transport: t}, nil
}

// Close releases idle connections. Active calls must already have joined.
func (e *HTTPExecutor) Close() { e.transport.CloseIdleConnections() }

type wireResponse struct {
	status  int
	body    []byte
	written time.Time
	err     error
	invalid string
}

func (e *HTTPExecutor) call(ctx context.Context, method, path, run, account, body string) wireResponse {
	// Trace callbacks may run after a failed RoundTrip returns. A separate atomic
	// cell prevents late callbacks from mutating the returned sample or racing it.
	var written atomic.Pointer[time.Time]
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			now := time.Now()
			written.CompareAndSwap(nil, &now)
		}
	}}
	r, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, e.base+path, strings.NewReader(body))
	if err != nil {
		return wireResponse{err: err}
	}
	r.Header.Set("X-Run-ID", run)
	r.Header.Set("X-Account-ID", account)
	r.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(r)
	w := wireResponse{err: err}
	if resp != nil {
		w.status = resp.StatusCode
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		closeErr := resp.Body.Close()
		w.body, w.err = data, errors.Join(w.err, readErr, closeErr)
		media, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if len(data) > maxResponseBytes {
			w.invalid = "response body exceeds 64 KiB"
		}
		if mediaErr != nil || media != "application/json" {
			w.invalid = "response content type is not JSON"
		}
		if resp.ProtoMajor != 1 || resp.ProtoMinor != 1 {
			w.invalid = "response protocol is not HTTP/1.1"
		}
	}
	if p := written.Load(); p != nil {
		w.written = *p
	}
	return w
}

// Execute requires an immutable Request returned by Build. It validates complete
// response identities and quantity/state invariants before reporting Business.
// The caller must set a context deadline; Run does this for every request.
func (e *HTTPExecutor) Execute(ctx context.Context, req Request) Execution {
	if _, ok := ctx.Deadline(); !ok {
		return Execution{Outcome: Outcome{Class: InvalidResponse, Certainty: "UNKNOWN", Detail: "request deadline required"}}
	}
	if req.Path != "/v1/orders" && req.Path != "/v1/cancellations" {
		return Execution{Outcome: Outcome{Class: InvalidResponse, Certainty: "UNKNOWN", Detail: "invalid prepared path"}}
	}
	w := e.call(ctx, http.MethodPost, req.Path, req.Command.RunID, req.Command.AccountID, req.Body)
	x := Execution{WrittenAt: w.written}
	x.Outcome = classify(req, w)
	return x
}

func classify(req Request, w wireResponse) Outcome {
	o := Outcome{HTTPStatus: w.status, Certainty: "UNKNOWN"}
	if w.err != nil {
		o.Class, o.Detail = TransportFailure, "transport failed before a complete response"
		var timeout net.Error
		if errors.Is(w.err, context.DeadlineExceeded) || errors.As(w.err, &timeout) && timeout.Timeout() {
			o.Class, o.Detail = TransportTimeout, "transport deadline expired"
		}
		return o
	}
	o.Class = InvalidResponse
	if w.invalid != "" {
		o.Detail = w.invalid
		return o
	}
	if w.status == http.StatusOK {
		var result d.Result
		if err := strictjson.Decode(w.body, &result); err != nil {
			o.Detail = "invalid business JSON"
			return o
		}
		if err := validateResult(req.Command, result); err != nil {
			o.Detail = "invalid business identity or order"
			return o
		}
		hash := sha256.Sum256(w.body)
		o = Outcome{Class: Business, HTTPStatus: w.status, Code: string(result.Code), Certainty: "COMPLETED", CommandSeq: result.CommandSeq, TradeCount: result.TradeCount, Fingerprint: hex.EncodeToString(hash[:])}
		if result.Order != nil {
			o.OrderStatus = result.Order.Status
		}
		if err := validateOutcome(req, o); err != nil {
			return Outcome{Class: InvalidResponse, HTTPStatus: w.status, Certainty: "UNKNOWN", Detail: "invalid business code or count"}
		}
		return o
	}
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Outcome string `json:"outcome"`
		} `json:"error"`
	}
	if err := strictjson.Decode(w.body, &failure); err != nil {
		o.Detail = "invalid error JSON"
		return o
	}
	wantStatus, certainty := errorContract(failure.Error.Code)
	if wantStatus != w.status || certainty == "" || certainty != failure.Error.Outcome {
		o.Detail = "invalid error status or certainty"
		return o
	}
	o.Class, o.Code, o.Certainty = ServerFailure, failure.Error.Code, certainty
	if w.status == 429 {
		o.Class = Overload
	}
	if w.status == 504 {
		o.Class = ServerTimeout
	}
	return o
}

func errorContract(code string) (int, string) {
	status := 0
	switch code {
	case "INVALID_REQUEST":
		status = 400
	case "UNKNOWN_ACCOUNT", "HOST_FORBIDDEN", "ORIGIN_FORBIDDEN":
		status = 403
	case "RUN_MISMATCH", "IDEMPOTENCY_CONFLICT", "RUN_LIMIT_REACHED":
		status = 409
	case "BODY_TOO_LARGE":
		status = 413
	case "UNSUPPORTED_MEDIA_TYPE":
		status = 415
	case "QUEUE_FULL", "TOO_MANY_REQUESTS", "DUPLICATE_WAITERS_FULL":
		status = 429
	case "REQUEST_TIMEOUT":
		status = 408
	case "ORDER_NOT_FOUND", "ROUTE_NOT_FOUND":
		status = 404
	case "METHOD_NOT_ALLOWED":
		status = 405
	case "PROTOCOL_UNSUPPORTED":
		status = 505
	case "NOT_READY":
		status = 503
	case "RESULT_TIMEOUT":
		return 504, "UNKNOWN"
	case "STORAGE_UNAVAILABLE", "INTERNAL_FAILURE":
		return 503, "UNKNOWN"
	default:
		return 0, ""
	}
	return status, "NOT_ADMITTED"
}

func validateResult(c d.Command, r d.Result) error {
	if r.SchemaVersion != d.Version || r.RunID != c.RunID || r.AccountID != c.AccountID || r.Kind != c.Kind || r.RequestID != c.Key().RequestID || r.CommandSeq < 1 {
		return fmt.Errorf("identity")
	}
	o := r.Order
	if o == nil {
		if c.Kind == d.CancelOrder && r.Code == d.OrderNotFound && r.TradeCount == 0 {
			return nil
		}
		return fmt.Errorf("missing order")
	}
	if o.Symbol != d.Symbol || (o.Side != d.Buy && o.Side != d.Sell) || o.PriceTicks < 1 || o.PriceTicks > d.MaxPriceTicks || o.OriginalQty < 1 || o.OriginalQty > d.MaxQuantityLots || o.ExecutedQty < 0 || o.ExecutedQty > o.OriginalQty || o.OpenQty < 0 || o.OpenQty > o.OriginalQty || o.CancelledQty < 0 || o.CancelledQty > o.OriginalQty || o.ExecutedQuoteAtoms < 0 || o.ExecutedQuoteAtoms > d.MaxNotionalAtoms || o.CreatedSeq < 1 || o.UpdatedSeq < o.CreatedSeq || o.UpdatedSeq > r.CommandSeq {
		return fmt.Errorf("order bounds")
	}
	if c.Kind == d.PlaceOrder {
		p := c.Place
		if o.ClientOrderID != p.ClientOrderID || o.Side != p.Side || o.PriceTicks != p.PriceTicks || o.OriginalQty != p.QuantityLots || o.CreatedSeq != r.CommandSeq || o.UpdatedSeq != r.CommandSeq || (r.TradeCount == 0) != (o.ExecutedQty == 0) || r.TradeCount > o.ExecutedQty {
			return fmt.Errorf("place mismatch")
		}
		if o.Status == d.Cancelled && o.Reason != d.SelfTradePrevention {
			return fmt.Errorf("place cancellation")
		}
	} else {
		if o.ClientOrderID != c.Cancel.ClientOrderID || o.CreatedSeq >= r.CommandSeq || r.Code == d.CancelSucceeded && (o.UpdatedSeq != r.CommandSeq || o.Reason != d.UserCancel) || r.Code != d.CancelSucceeded && o.UpdatedSeq >= r.CommandSeq {
			return fmt.Errorf("cancel mismatch")
		}
	}
	if o.Status == d.Rejected {
		if r.Code != d.InsufficientBalance || o.Reason != d.InsufficientFunds || o.ExecutedQty != 0 || o.OpenQty != 0 || o.CancelledQty != 0 || o.ExecutedQuoteAtoms != 0 {
			return fmt.Errorf("rejection")
		}
		return nil
	}
	if o.ExecutedQty+o.OpenQty+o.CancelledQty != o.OriginalQty || o.ExecutedQuoteAtoms < o.ExecutedQty || o.ExecutedQuoteAtoms > o.ExecutedQty*d.MaxPriceTicks || o.Side == d.Buy && o.ExecutedQuoteAtoms > o.ExecutedQty*o.PriceTicks || o.Side == d.Sell && o.ExecutedQuoteAtoms < o.ExecutedQty*o.PriceTicks {
		return fmt.Errorf("quantity conservation")
	}
	switch o.Status {
	case d.Open:
		if o.ExecutedQty != 0 || o.OpenQty != o.OriginalQty || o.Reason != "" {
			return fmt.Errorf("open state")
		}
	case d.PartiallyFilled:
		if o.ExecutedQty == 0 || o.OpenQty == 0 || o.CancelledQty != 0 || o.Reason != "" {
			return fmt.Errorf("partial state")
		}
	case d.Filled:
		if o.ExecutedQty != o.OriginalQty || o.Reason != "" {
			return fmt.Errorf("filled state")
		}
	case d.Cancelled:
		if o.OpenQty != 0 || o.CancelledQty == 0 || (o.Reason != d.UserCancel && o.Reason != d.SelfTradePrevention) {
			return fmt.Errorf("cancelled state")
		}
	default:
		return fmt.Errorf("status")
	}
	return nil
}
