package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

func exchange(ctx context.Context, timeout time.Duration, endpoint, run, account, method, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Run-ID", run)
	req.Header.Set("X-Account-ID", account)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	err = errors.Join(readErr, resp.Body.Close())
	if len(b) > 1<<20 {
		err = errors.Join(err, errors.New("response exceeds 1 MiB"))
	}
	return resp.StatusCode, b, err
}

func printJSON(out io.Writer, b []byte) error {
	if !json.Valid(b) {
		return errors.New("server returned invalid JSON; outcome UNKNOWN")
	}
	n, err := out.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil && (len(b) == 0 || b[len(b)-1] != '\n') {
		_, err = io.WriteString(out, "\n")
	}
	return err
}

func responseError(status int, b []byte) error {
	var v api.ErrorResponse
	if err := strictjson.Decode(b, &v); err != nil || v.Error.Code == "" || v.Error.Outcome != "UNKNOWN" && v.Error.Outcome != "NOT_ADMITTED" {
		return fmt.Errorf("HTTP %d: invalid response; outcome UNKNOWN", status)
	}
	return fmt.Errorf("HTTP %d: %s (%s applies only to this attempt)", status, v.Error.Code, v.Error.Outcome)
}

func checkResult(b []byte, run, account string, kind d.Kind, id string) error {
	var r d.Result
	if err := strictjson.Decode(b, &r); err != nil {
		return err
	}
	if r.SchemaVersion != 1 || r.RunID != run || r.AccountID != account || r.Kind != kind || r.RequestID != id || r.CommandSeq < 1 {
		return errors.New("result identity mismatch")
	}
	switch r.Code {
	case d.Accepted, d.InsufficientBalance, d.CancelSucceeded, d.AlreadyFilled, d.AlreadyCancelled, d.OrderNotFound:
	default:
		return errors.New("unknown business result")
	}
	if kind == d.PlaceOrder && (r.Code != d.Accepted && r.Code != d.InsufficientBalance || r.Order == nil || r.Order.ClientOrderID != id) {
		return errors.New("invalid place result")
	}
	if kind == d.CancelOrder && (r.Code == d.Accepted || r.Code == d.InsufficientBalance || r.TradeCount != 0 || (r.Order == nil) != (r.Code == d.OrderNotFound)) {
		return errors.New("invalid cancel result")
	}
	return nil
}

func resultState(status int, b []byte) bool {
	var v struct {
		State string `json:"state"`
	}
	return strictjson.Decode(b, &v) == nil && (status == 202 && v.State == "PENDING" || status == 404 && v.State == "UNKNOWN")
}

func sendMutation(ctx context.Context, timeout time.Duration, r savedRequest, out io.Writer) error {
	post, _, id := r.paths()
	status, b, err := exchange(ctx, timeout, r.URL, r.RunID, r.Account, http.MethodPost, post, r.body())
	if err != nil {
		return fmt.Errorf("outcome UNKNOWN; use result/retry with the saved request, never a new ID: %w", err)
	}
	if status != http.StatusOK {
		if err := printJSON(out, b); err != nil {
			return err
		}
		return responseError(status, b)
	}
	if err := checkResult(b, r.RunID, r.Account, r.Kind, id); err != nil {
		return fmt.Errorf("outcome UNKNOWN; invalid result: %w", err)
	}
	return printJSON(out, b)
}

func runSaved(ctx context.Context, o options, r savedRequest, out, stderr io.Writer) error {
	_, lookup, id := r.paths()
	status, b, err := exchange(ctx, o.timeout, r.URL, r.RunID, r.Account, http.MethodGet, lookup, nil)
	if err != nil {
		return fmt.Errorf("result lookup failed; no retry POST sent: %w", err)
	}
	if status == http.StatusOK {
		if err := checkResult(b, r.RunID, r.Account, r.Kind, id); err != nil {
			return err
		}
	} else if !resultState(status, b) {
		if err := printJSON(out, b); err != nil {
			return err
		}
		return responseError(status, b)
	}
	if o.command == "result" {
		if err := printJSON(out, b); err != nil {
			return err
		}
		if status != 200 {
			return errors.New("result not final; PENDING/UNKNOWN does not prove rejection; retry with the same request")
		}
		return nil
	}
	if _, err := fmt.Fprintf(stderr, "lookup HTTP %d; retrying saved request with unchanged ID and content\n", status); err != nil {
		return err
	}
	// Even a completed result lookup cannot prove payload equality (e.g. a cancel
	// of a missing order). POST lets the server enforce its full duplicate key.
	return sendMutation(ctx, o.timeout, r, out)
}

func query(ctx context.Context, timeout time.Duration, endpoint, run, account, path, command string, out io.Writer) error {
	status, b, err := exchange(ctx, timeout, endpoint, run, account, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if command == "result" && resultState(status, b) {
		if err := printJSON(out, b); err != nil {
			return err
		}
		return errors.New("result not final; retry only with the same ID and content")
	}
	if status != 200 {
		if err := printJSON(out, b); err != nil {
			return err
		}
		return responseError(status, b)
	}
	var identity string
	var decodeErr error
	switch command {
	case "balances":
		var v api.BalancesView
		decodeErr = strictjson.Decode(b, &v)
		identity = v.RunID
	case "book":
		var v api.BookView
		decodeErr = strictjson.Decode(b, &v)
		identity = v.RunID
	case "trades":
		var v api.TradesView
		decodeErr = strictjson.Decode(b, &v)
		identity = v.RunID
	case "order":
		var v api.OrderView
		decodeErr = strictjson.Decode(b, &v)
		identity = v.RunID
		if decodeErr == nil && (v.Order == nil || v.Order.ClientOrderID != strings.TrimPrefix(path, "/v1/orders/")) {
			decodeErr = errors.New("order ID mismatch")
		}
	case "result":
		parts := strings.Split(path, "/")
		kind := d.PlaceOrder
		if parts[3] == "cancel" {
			kind = d.CancelOrder
		}
		decodeErr = checkResult(b, run, account, kind, parts[4])
		identity = run
	}
	if decodeErr != nil {
		return fmt.Errorf("invalid response: %w", decodeErr)
	}
	if identity != run {
		return errors.New("response run ID mismatch")
	}
	return printJSON(out, b)
}
