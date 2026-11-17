package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaon000/exchange/internal/api"
	"github.com/gaon000/exchange/internal/bench"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

type onceGate struct {
	once sync.Once
	done chan struct{}
}

func gate() *onceGate     { return &onceGate{done: make(chan struct{})} }
func (g *onceGate) open() { g.once.Do(func() { close(g.done) }) }
func (g *onceGate) wait(ctx context.Context) error {
	select {
	case <-g.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type concurrentObservation struct {
	Phase               string              `json:"phase"`
	Client              int                 `json:"client"`
	Command             d.CheckpointCommand `json:"command"`
	Fault               string              `json:"fault,omitempty"`
	ExpectConflict      bool                `json:"expectConflict,omitempty"`
	Outcome             bench.Outcome       `json:"outcome"`
	RequestLatencyNanos int64               `json:"requestLatencyNanos"`
	UpstreamStatus      int                 `json:"upstreamStatus"`
	UpstreamResponse    string              `json:"upstreamResponse"`
	UpstreamError       string              `json:"upstreamError,omitempty"`
}
type concurrentTask struct {
	observation  concurrentObservation
	command      d.Command
	stop         func()
	upstreamDone chan struct{}
	clientDone   chan struct{}
	requests     atomic.Int32
}
type concurrentWave struct {
	tasks   []*concurrentTask
	release *onceGate
	entered chan struct{}
	cancel  context.CancelFunc
}

func startConcurrentWave(ctx context.Context, s *session, run, phase string, inputs []concurrentAttempt,
	marker int, ready chan<- error, markerRelease, late *onceGate, order *admissionOrder) (*concurrentWave, error) {
	ctx, cancel := context.WithCancel(ctx)
	w := &concurrentWave{release: gate(), entered: make(chan struct{}, len(inputs)), cancel: cancel}
	success := false
	defer func() {
		if !success {
			w.release.open()
			markerRelease.open()
			late.open()
			w.close()
		}
	}()
	for _, input := range inputs {
		c, err := input.Command.Command(run)
		if err != nil {
			return nil, err
		}
		path, payload := commandPayload(c)
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		task := &concurrentTask{command: c, upstreamDone: make(chan struct{}), clientDone: make(chan struct{}),
			observation: concurrentObservation{Phase: phase, Client: input.Client, Command: input.Command, Fault: input.Fault, ExpectConflict: input.ExpectConflict}}
		url, stop := startLossIntermediary(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			if task.requests.Add(1) != 1 {
				http.Error(rw, "unexpected repeated attempt", 500)
				return
			}
			defer close(task.upstreamDone)
			var failure error
			defer func() {
				if failure != nil {
					task.observation.UpstreamError = failure.Error()
				}
			}()
			cut := func() {
				conn, _, err := rw.(http.Hijacker).Hijack()
				if err == nil {
					err = conn.Close()
				}
				failure = errors.Join(failure, err)
			}
			b, err := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
			if err != nil || req.Method != "POST" || req.URL.Path != path || req.Header.Get("X-Run-ID") != run || req.Header.Get("X-Account-ID") != c.AccountID || !bytes.Equal(b, body) {
				failure = errors.Join(err, errors.New("concurrent intermediary request mismatch"))
				cut()
				return
			}
			w.entered <- struct{}{}
			if err := w.release.wait(ctx); err != nil {
				failure = err
				cut()
				return
			}
			if input.Fault == "held-original" {
				cut()
				if err := late.wait(ctx); err != nil {
					failure = errors.Join(failure, err)
					return
				}
			}
			if err := order.enter(ctx, c.Key()); err != nil {
				failure = err
				if input.Fault != "held-original" {
					cut()
				}
				return
			}
			upstream, err := http.NewRequestWithContext(ctx, "POST", s.base+path, bytes.NewReader(body))
			if err == nil {
				upstream.Header.Set("X-Run-ID", run)
				upstream.Header.Set("X-Account-ID", c.AccountID)
				upstream.Header.Set("Content-Type", "application/json")
				var resp *http.Response
				resp, err = s.client.Do(upstream)
				if resp != nil {
					task.observation.UpstreamStatus = resp.StatusCode
					data, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
					err = errors.Join(err, readErr, resp.Body.Close())
					task.observation.UpstreamResponse = string(data)
					wantStatus := http.StatusOK
					if input.ExpectConflict {
						wantStatus = http.StatusConflict
					}
					if len(data) > 1<<20 || resp.StatusCode != wantStatus {
						err = errors.Join(err, errors.New("unexpected upstream status/size"))
					}
				}
			}
			failure = errors.Join(failure, err)
			if failure == nil {
				order.complete(c.Key())
			}
			if input.Client == marker {
				ready <- failure
				if err := markerRelease.wait(ctx); err != nil {
					failure = errors.Join(failure, err)
				}
			}
			if input.Fault == "held-original" {
				return
			}
			if failure != nil {
				cut()
				return
			}
			switch input.Fault {
			case "drop-headers":
				cut()
			case "truncate-body":
				rw.Header().Set("Content-Type", "application/json")
				rw.Header().Set("Content-Length", strconv.Itoa(len(task.observation.UpstreamResponse)))
				_, err = io.WriteString(rw, task.observation.UpstreamResponse[:len(task.observation.UpstreamResponse)/2])
				failure = errors.Join(failure, err)
				rw.(http.Flusher).Flush()
				cut()
			default:
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(task.observation.UpstreamStatus)
				_, err = io.WriteString(rw, task.observation.UpstreamResponse)
				failure = errors.Join(failure, err)
			}
		}))
		task.stop = stop
		executor, err := bench.NewHTTPExecutor(url, 1)
		if err != nil {
			stop()
			return nil, err
		}
		w.tasks = append(w.tasks, task)
		go func() {
			defer close(task.clientDone)
			defer executor.Close()
			started := time.Now()
			task.observation.Outcome = executor.Execute(ctx, bench.Request{Command: c, Path: path, Body: string(body)}).Outcome
			task.observation.RequestLatencyNanos = time.Since(started).Nanoseconds()
		}()
	}
	success = true
	return w, nil
}

func (w *concurrentWave) dispatch(ctx context.Context) error {
	for range w.tasks {
		select {
		case <-w.entered:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.release.open()
	return nil
}
func (w *concurrentWave) clients(ctx context.Context) error {
	for _, t := range w.tasks {
		select {
		case <-t.clientDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (w *concurrentWave) join(ctx context.Context) error {
	for _, t := range w.tasks {
		select {
		case <-t.upstreamDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-t.clientDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (w *concurrentWave) close() {
	if w == nil {
		return
	}
	w.cancel()
	w.release.open()
	for _, t := range w.tasks {
		t.stop()
		<-t.clientDone
	}
}
func (w *concurrentWave) observations() []concurrentObservation {
	result := make([]concurrentObservation, 0, len(w.tasks))
	for _, t := range w.tasks {
		if t.requests.Load() != 1 {
			t.observation.UpstreamError = "intermediary request count was not one"
		}
		result = append(result, t.observation)
	}
	return result
}
func validateConcurrentOutcome(o concurrentObservation) error {
	if o.ExpectConflict {
		var response api.ErrorResponse
		if o.Fault != "" || o.UpstreamError != "" || o.UpstreamStatus != 409 || json.Unmarshal([]byte(o.UpstreamResponse), &response) != nil || response.Error.Code != "IDEMPOTENCY_CONFLICT" || response.Error.Outcome != rt.NotAdmitted || o.Outcome.HTTPStatus != 409 || o.Outcome.Class != bench.ServerFailure || o.Outcome.Certainty != "NOT_ADMITTED" || o.Outcome.Code != "IDEMPOTENCY_CONFLICT" {
			return fmt.Errorf("client %d invalid conflict observation", o.Client)
		}
		return nil
	}
	if o.UpstreamError != "" || o.UpstreamStatus != 200 {
		return fmt.Errorf("client %d upstream failure: %s", o.Client, o.UpstreamError)
	}
	if o.Fault == "" {
		if o.Outcome.Class != bench.Business || o.Outcome.Certainty != "COMPLETED" {
			return fmt.Errorf("client %d incomplete business result", o.Client)
		}
		return nil
	}
	stage, kind := bench.FailureResponseHeader, bench.ErrorEOF
	if o.Fault == "truncate-body" {
		stage, kind = bench.FailureResponseRead, bench.ErrorUnexpectedEOF
	}
	if o.Outcome.Class != bench.TransportFailure || o.Outcome.Certainty != "UNKNOWN" || o.Outcome.FailureStage != stage || o.Outcome.ErrorType != kind {
		return fmt.Errorf("unexpected concurrent fault outcome: %+v", o)
	}
	return nil
}
