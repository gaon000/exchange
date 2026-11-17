package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/wal"
)

type forwardedResponse struct {
	body []byte
	err  error
}

// startLossIntermediary owns handler lifetime as well as connections. Hijack
// removes a connection from httptest.Server.Close's wait group before its
// handler returns, so callers must unblock handlers before invoking stop.
func startLossIntermediary(handler http.Handler) (url string, stop func()) {
	var handlers sync.WaitGroup
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		handler.ServeHTTP(w, req)
	}))
	return proxy.URL, func() {
		// Stop accepting requests and finish tracked connections before Wait,
		// so no handler can add itself after this wait begins.
		proxy.Close()
		handlers.Wait()
	}
}

// responseLoss uses a separate loopback intermediary, never storage/server hooks.
// The held original is forwarded with our context after its client disconnects:
// loss of the downstream connection cannot establish that admission is impossible.
func (r *scenarioRun) responseLoss(ctx context.Context, a action) error {
	c, err := a.Command.Command(r.g.RunID)
	if err != nil {
		return err
	}
	if _, found, err := r.model.Lookup(c); err != nil || found {
		return errors.New("response-loss requires a new nonconflicting command")
	}
	path, payload := commandPayload(c)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	done := make(chan forwardedResponse, 1)
	var calls atomic.Int32
	proxyURL, stop := startLossIntermediary(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) != 1 {
			http.Error(w, "unexpected repeated request", 500)
			return
		}
		result := forwardedResponse{}
		defer func() { done <- result }()
		cut := func() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				err = conn.Close()
			}
			result.err = errors.Join(result.err, err)
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
		if err != nil || req.Method != "POST" || req.URL.Path != path || req.Header.Get("X-Run-ID") != c.RunID || req.Header.Get("X-Account-ID") != c.AccountID || !bytes.Equal(body, encoded) {
			result.err = errors.Join(err, errors.New("intermediary received changed request"))
			cut()
			return
		}
		if a.Fault == "held-original" {
			cut()
			select {
			case <-gate:
			case <-ctx.Done():
				result.err = errors.Join(result.err, ctx.Err())
				return
			}
		}
		upstream, err := http.NewRequestWithContext(ctx, "POST", r.s.base+path, bytes.NewReader(encoded))
		if err != nil {
			result.err = err
			if a.Fault != "held-original" {
				cut()
			}
			return
		}
		upstream.Header.Set("X-Run-ID", c.RunID)
		upstream.Header.Set("X-Account-ID", c.AccountID)
		upstream.Header.Set("Content-Type", "application/json")
		resp, err := r.s.client.Do(upstream)
		result.err = errors.Join(result.err, err)
		if resp != nil {
			var readErr error
			result.body, readErr = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
			result.err = errors.Join(result.err, readErr, resp.Body.Close())
			if resp.StatusCode != 200 || len(result.body) > 1<<20 {
				result.err = errors.Join(result.err, errors.New("upstream response not bounded HTTP 200"))
			}
		}
		switch a.Fault {
		case "drop-headers":
			cut()
		case "truncate-body":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(result.body)))
			_, writeErr := w.Write(result.body[:len(result.body)/2])
			result.err = errors.Join(result.err, writeErr)
			w.(http.Flusher).Flush()
			cut()
		}
	}))
	defer func() { cancel(); release(); stop() }()
	executor, err := bench.NewHTTPExecutor(proxyURL, 1)
	if err != nil {
		return err
	}
	defer executor.Close()
	started := time.Now()
	outcome := executor.Execute(ctx, bench.Request{Command: c, Path: path, Body: string(encoded)}).Outcome
	requestLatency := time.Since(started).Nanoseconds()
	wantStage, wantType := bench.FailureResponseHeader, bench.ErrorEOF
	if a.Fault == "truncate-body" {
		wantStage, wantType = bench.FailureResponseRead, bench.ErrorUnexpectedEOF
	}
	// Save the observed failure before verifying it, so failed runs retain its cause.
	if err := r.events.Encode(map[string]any{"step": *r.step, "phase": "transport-observed", "fault": a.Fault, "outcome": outcome, "requestLatencyNanos": requestLatency}); err != nil {
		return err
	}
	if outcome.Class != bench.TransportFailure || outcome.Certainty != "UNKNOWN" || outcome.FailureStage != wantStage || outcome.ErrorType != wantType {
		return fmt.Errorf("unexpected injected transport outcome: %+v", outcome)
	}
	kind := "place"
	if c.Kind == d.CancelOrder {
		kind = "cancel"
	}
	lookup := "/v1/results/" + kind + "/" + c.Key().RequestID
	var unknown []byte
	if a.Fault == "held-original" {
		unknown, err = r.s.request(ctx, c.RunID, c.AccountID, "GET", lookup, nil, 404)
		if err != nil || !bytes.Equal(unknown, []byte(`{"state":"UNKNOWN"}`)) {
			return fmt.Errorf("held-original UNKNOWN witness: %s: %w", unknown, errors.Join(err, errors.New("expected 404 UNKNOWN")))
		}
		r.unknownLookups++
		// The retry commits while the original is still held. Releasing it must
		// return this same first response, without a second command/settlement.
		if err := r.submit(ctx, *a.Command); err != nil {
			return err
		}
		release()
	}
	var forwarded forwardedResponse
	select {
	case forwarded = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if forwarded.err != nil {
		return forwarded.err
	}
	if calls.Load() != 1 {
		return errors.New("HTTP executor retried a lost request")
	}
	if a.Fault != "held-original" {
		want, err := r.model.Apply(r.model.AppliedSeq()+1, c)
		if err != nil {
			return err
		}
		body, err := json.Marshal(want)
		if err != nil || !bytes.Equal(body, forwarded.body) {
			return errors.New("committed upstream response/model mismatch")
		}
		r.first[c.Key()] = append([]byte(nil), forwarded.body...)
		r.records = append(r.records, wal.Record{Seq: want.CommandSeq, Command: c})
	}
	if !bytes.Equal(r.first[c.Key()], forwarded.body) {
		return errors.New("late original changed cached response")
	}
	got, err := r.s.request(ctx, c.RunID, c.AccountID, "GET", lookup, nil, 200)
	if err != nil || !bytes.Equal(got, r.first[c.Key()]) {
		return errors.Join(err, errors.New("lookup after loss changed first response"))
	}
	if err := r.submit(ctx, *a.Command); err != nil {
		return err
	}
	if err := r.s.checkViews(ctx, c.RunID, r.model, r.first); err != nil {
		return err
	}
	r.responseLosses++
	return writeJSON(filepath.Join(r.output, fmt.Sprintf("transport-%03d.json", *r.step)), map[string]any{
		"fault": a.Fault, "outcome": outcome, "unknownLookup": string(unknown), "upstreamFirstResponse": json.RawMessage(forwarded.body),
		"appliedSeq": r.model.AppliedSeq(), "forwardedRequests": calls.Load(), "sameIDVerified": true,
		"requestLatencyNanos": requestLatency,
	})
}
