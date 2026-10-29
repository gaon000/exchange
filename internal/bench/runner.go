package bench

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

// ResponseClass separates durable business results from uncertain/admission
// failures. A transport error never implies that the server did not admit work.
type ResponseClass string

const (
	Business         ResponseClass = "BUSINESS"
	Overload         ResponseClass = "OVERLOAD"
	ServerTimeout    ResponseClass = "SERVER_TIMEOUT"
	ServerFailure    ResponseClass = "SERVER_FAILURE"
	TransportTimeout ResponseClass = "TRANSPORT_TIMEOUT"
	TransportFailure ResponseClass = "TRANSPORT_FAILURE"
	InvalidResponse  ResponseClass = "INVALID_RESPONSE"
)

// Outcome is a compact response record. Fingerprint is the lowercase SHA-256 of
// a validated complete business response, used to detect mutable retry results.
// Detail is diagnostic text, never a reason to infer non-admission.
type Outcome struct {
	Class       ResponseClass `json:"class"`
	HTTPStatus  int           `json:"httpStatus"`
	Code        string        `json:"code"`
	Certainty   string        `json:"certainty"`
	CommandSeq  d.Int         `json:"commandSeq"`
	TradeCount  d.Int         `json:"tradeCount"`
	OrderStatus d.Status      `json:"orderStatus"`
	Fingerprint string        `json:"fingerprint"`
	Detail      string        `json:"detail,omitempty"`
}

// Execution includes the time the request was written to the transport, when
// observable. Zero WrittenAt means unknown, not zero network/write latency.
type Execution struct {
	Outcome   Outcome
	WrittenAt time.Time
}

// Executor executes one prepared request. Implementations must honor context
// cancellation and only return Business for a validated, matching full response.
type Executor interface {
	Execute(context.Context, Request) Execution
}

// RunOptions bounds active work independently of the offered schedule.
// No generator job queue hides saturation: unavailable workers cause local drops.
type RunOptions struct {
	MaxInFlight    int           `json:"maxInFlight"`
	RequestTimeout time.Duration `json:"requestTimeoutNanos"`
	MaxLateness    time.Duration `json:"maxLatenessNanos"`
}

// Validate checks generator resource and deadline limits.
func (o RunOptions) Validate() error {
	if o.MaxInFlight < 1 || o.MaxInFlight > 65536 || o.RequestTimeout <= 0 || o.RequestTimeout > time.Minute || o.MaxLateness <= 0 || o.MaxLateness > time.Second {
		return fmt.Errorf("invalid in-flight, timeout or scheduler lateness limit")
	}
	return nil
}

// LocalFailure explains a scheduled request that was never handed to Executor.
type LocalFailure string

const (
	LocalCapacity LocalFailure = "GENERATOR_CAPACITY"
	LocalLate     LocalFailure = "GENERATOR_LATE"
	LocalCanceled LocalFailure = "GENERATOR_CANCELED"
)

// Sample belongs to Plan.Request(Index). Offsets are relative to ScheduleEpoch;
// -1 means not observed. Every planned index has one sample, including local drops.
// StartedAt is executor start, WrittenAt is transport write completion, and
// FinishedAt is complete response/error observation; they are distinct boundaries.
type Sample struct {
	Index      int           `json:"index"`
	Local      LocalFailure  `json:"localFailure,omitempty"`
	StartedAt  time.Duration `json:"startedNanos"`
	WrittenAt  time.Duration `json:"writtenNanos"`
	FinishedAt time.Duration `json:"finishedNanos"`
	Outcome    Outcome       `json:"outcome"`
}

// RunResult retains all samples for exact quantiles and later raw artifact output.
// No artifact serialization or per-request disk I/O happens on the timed path.
type RunResult struct {
	ScheduleEpoch time.Time     `json:"scheduleEpoch"`
	FinishedUTC   time.Time     `json:"finishedUTC"`
	Elapsed       time.Duration `json:"elapsedNanos"`
	Aborted       bool          `json:"aborted"`
	Options       RunOptions    `json:"options"`
	Samples       []Sample      `json:"-"`
}

type clock interface {
	now() time.Time
	wait(context.Context, time.Duration) error
}

type realClock struct{}

func (realClock) now() time.Time { return time.Now() }
func (realClock) wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Run offers every request at its absolute planned offset, without using response
// completion to advance the schedule. Late/full/canceled attempts remain in the
// result. Accepted executions are joined even after cancellation; server work may
// outlive its client deadline. Preparation is explicitly the caller's prior step.
func Run(ctx context.Context, p *Plan, options RunOptions, executor Executor) (RunResult, error) {
	return run(ctx, p, options, executor, realClock{})
}

func run(ctx context.Context, p *Plan, options RunOptions, executor Executor, clk clock) (RunResult, error) {
	if p == nil || p.digest == "" || len(p.requests) == 0 || executor == nil {
		return RunResult{}, fmt.Errorf("plan and executor are required")
	}
	if err := options.Validate(); err != nil {
		return RunResult{}, err
	}
	result := RunResult{Options: options, Samples: make([]Sample, len(p.requests))}
	for i := range result.Samples {
		result.Samples[i] = Sample{Index: i, StartedAt: -1, WrittenAt: -1, FinishedAt: -1}
	}
	// Start the schedule only after worker startup. Publication through jobs
	// happens after epoch is set, so workers cannot observe an uninitialized time.
	var epoch time.Time
	jobs := make(chan int) // deliberately no queue
	ready := make(chan struct{}, options.MaxInFlight)
	var workers sync.WaitGroup
	var panicked atomic.Bool
	for worker := 0; worker < options.MaxInFlight; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ready <- struct{}{}
			for index := range jobs {
				req := p.requests[index]
				started := clk.now()
				sample := Sample{Index: index, StartedAt: started.Sub(epoch), WrittenAt: -1, FinishedAt: -1}
				if ctx.Err() != nil {
					sample.Local, sample.StartedAt = LocalCanceled, -1
				} else if started.Sub(epoch.Add(req.Scheduled)) > options.MaxLateness {
					// A handoff may succeed before this goroutine gets CPU time.
					sample.Local, sample.StartedAt = LocalLate, -1
				} else {
					requestContext, cancel := context.WithTimeout(ctx, options.RequestTimeout)
					execution, failed := execute(requestContext, executor, req)
					cancel()
					if failed {
						panicked.Store(true)
					}
					sample.Outcome = execution.Outcome
					if !execution.WrittenAt.IsZero() {
						sample.WrittenAt = execution.WrittenAt.Sub(epoch)
					}
				}
				sample.FinishedAt = max(0, clk.now().Sub(epoch))
				result.Samples[index] = sample // unique index ownership after handoff
			}
		}()
	}
	for worker := 0; worker < options.MaxInFlight; worker++ {
		<-ready
	}
	epoch = clk.now().Add(20 * time.Millisecond)
	result.ScheduleEpoch = epoch.UTC()
	for index, req := range p.requests {
		if err := clk.wait(ctx, epoch.Add(req.Scheduled).Sub(clk.now())); err != nil {
			for remaining := index; remaining < len(p.requests); remaining++ {
				result.Samples[remaining].Local = LocalCanceled
				result.Samples[remaining].FinishedAt = max(0, clk.now().Sub(epoch))
			}
			break
		}
		if now := clk.now(); now.Sub(epoch.Add(req.Scheduled)) > options.MaxLateness {
			result.Samples[index].Local = LocalLate
			result.Samples[index].FinishedAt = now.Sub(epoch)
			continue
		}
		select {
		case jobs <- index:
		default:
			result.Samples[index].Local = LocalCapacity
			result.Samples[index].FinishedAt = clk.now().Sub(epoch)
		}
	}
	close(jobs)
	// Keep the fixed observation window even if the final response arrives early.
	_ = clk.wait(ctx, epoch.Add(p.spec.Warmup+p.spec.Duration).Sub(clk.now()))
	workers.Wait()
	finished := clk.now()
	result.FinishedUTC, result.Elapsed = finished.UTC(), max(0, finished.Sub(epoch))
	result.Aborted = ctx.Err() != nil || panicked.Load()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if panicked.Load() {
		return result, fmt.Errorf("executor panic; retained samples are not a valid benchmark")
	}
	return result, nil
}

func execute(ctx context.Context, executor Executor, req Request) (execution Execution, panicked bool) {
	defer func() {
		if recover() != nil {
			execution = Execution{Outcome: Outcome{Class: InvalidResponse, Certainty: "UNKNOWN", Detail: "executor panic"}}
			panicked = true
		}
	}()
	return executor.Execute(ctx, req), false
}
