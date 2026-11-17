package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"syscall"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingBody struct{ readErr, closeErr error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.readErr }
func (b failingBody) Close() error             { return b.closeErr }

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return false }

func TestTransportErrorTaxonomyAndHistoricalSamples(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want TransportErrorType
	}{
		{context.DeadlineExceeded, ErrorDeadlineExceeded}, {context.Canceled, ErrorCanceled},
		{syscall.ECONNREFUSED, ErrorConnectionRefused}, {syscall.ECONNRESET, ErrorConnectionReset},
		{syscall.EPIPE, ErrorBrokenPipe}, {io.ErrUnexpectedEOF, ErrorUnexpectedEOF}, {io.EOF, ErrorEOF},
		{net.ErrClosed, ErrorClosed}, {syscall.ENETUNREACH, ErrorNetworkUnreachable}, {syscall.EHOSTUNREACH, ErrorNetworkUnreachable},
		{timeoutError{}, ErrorTimeout}, {errors.New("private URL/body must never be copied"), ErrorOther},
	} {
		err := fmt.Errorf("wrapped: %w", &net.OpError{Op: "read", Net: "tcp", Err: tc.err})
		if got := transportErrorType(err); got != tc.want {
			t.Fatalf("%v: %s != %s", err, got, tc.want)
		}
		o := classify(httpRequest(t), wireResponse{err: err})
		if o.Certainty != "UNKNOWN" || o.FailureStage != FailureRoundTrip || o.ErrorType != tc.want {
			t.Fatal(o)
		}
		b, _ := json.Marshal(o)
		var decoded Outcome
		if err := json.Unmarshal(b, &decoded); err != nil || decoded != o {
			t.Fatal("metadata round trip", err, string(b))
		}
		if strings.Contains(string(b), "private") || strings.Contains(string(b), "wrapped") {
			t.Fatal("raw error exposed", string(b))
		}
	}
	req := httpRequest(t)
	legacy := Outcome{Class: TransportFailure, Certainty: "UNKNOWN", Detail: "transport failed before a complete response"}
	if err := validateOutcome(req, legacy); err != nil {
		t.Fatal("historical sample rejected", err)
	}
	for _, fields := range []Outcome{
		{FailureStage: FailureConnect}, {ErrorType: ErrorEOF},
		{FailureStage: "secret-stage", ErrorType: ErrorEOF}, {FailureStage: FailureConnect, ErrorType: "secret-error"},
	} {
		fields.Class, fields.Certainty = TransportFailure, "UNKNOWN"
		if validateOutcome(req, fields) == nil {
			t.Fatal("invalid metadata accepted", fields)
		}
	}
	legacy.FailureStage, legacy.ErrorType = FailureConnect, ErrorConnectionRefused
	if err := validateOutcome(req, legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Class = InvalidResponse
	if validateOutcome(req, legacy) == nil {
		t.Fatal("metadata on non-transport outcome")
	}
}

func TestTransportFailureStagesAndLateTraceLifetime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stage   FailureStage
		kind    TransportErrorType
		perform transportFunc
	}{
		{"connect", FailureConnect, ErrorConnectionRefused, func(r *http.Request) (*http.Response, error) {
			err := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			httptrace.ContextClientTrace(r.Context()).ConnectDone("tcp", "127.0.0.1:1", err)
			return nil, err
		}},
		{"write", FailureRequestWrite, ErrorBrokenPipe, func(r *http.Request) (*http.Response, error) {
			httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{Err: syscall.EPIPE})
			return nil, syscall.EPIPE
		}},
		{"headers", FailureResponseHeader, ErrorEOF, func(r *http.Request) (*http.Response, error) {
			httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
			return nil, io.EOF
		}},
		{"unrelated-connect", FailureRoundTrip, ErrorCanceled, func(r *http.Request) (*http.Response, error) {
			httptrace.ContextClientTrace(r.Context()).ConnectDone("tcp", "127.0.0.1:1", syscall.ECONNREFUSED)
			return nil, context.Canceled
		}},
		{"read", FailureResponseRead, ErrorUnexpectedEOF, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: failingBody{readErr: io.ErrUnexpectedEOF}, Header: http.Header{"Content-Type": {"application/json"}}, ProtoMajor: 1, ProtoMinor: 1}, nil
		}},
		{"close", FailureResponseClose, ErrorClosed, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: failingBody{readErr: io.EOF, closeErr: net.ErrClosed}, Header: http.Header{"Content-Type": {"application/json"}}, ProtoMajor: 1, ProtoMinor: 1}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &HTTPExecutor{base: "http://127.0.0.1:1", client: &http.Client{Transport: tc.perform}}
			w := e.call(context.Background(), "POST", "/v1/orders", "run", "alice", "{}")
			o := classify(httpRequest(t), w)
			if o.FailureStage != tc.stage || o.ErrorType != tc.kind || o.Certainty != "UNKNOWN" {
				t.Fatal(o)
			}
		})
	}
	gate, done := make(chan struct{}), make(chan struct{})
	e := &HTTPExecutor{base: "http://127.0.0.1:1", client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		go func() {
			defer close(done)
			<-gate
			trace.ConnectDone("tcp", "127.0.0.1:1", syscall.ECONNREFUSED)
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			trace.WroteRequest(httptrace.WroteRequestInfo{Err: syscall.EPIPE})
		}()
		return nil, context.Canceled
	})}}
	w := e.call(context.Background(), "POST", "/v1/orders", "run", "alice", "{}")
	before := classify(httpRequest(t), w)
	close(gate)
	<-done
	if after := classify(httpRequest(t), w); before != after || !w.written.IsZero() || before.FailureStage != FailureRoundTrip {
		t.Fatal("late callbacks mutated sample", before, after)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w = e.call(ctx, "\n", "/v1/orders", "run", "alice", "{}")
	if w.failureStage != FailureRequestBuild {
		t.Fatal("invalid request not localized", w)
	}
}
