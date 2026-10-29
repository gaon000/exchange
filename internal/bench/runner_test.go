package bench

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type executorFunc func(context.Context, Request) Execution

func (f executorFunc) Execute(ctx context.Context, r Request) Execution { return f(ctx, r) }

func testRunOptions() RunOptions {
	return RunOptions{MaxInFlight: 4, RequestTimeout: time.Second, MaxLateness: time.Second}
}

func scheduledPlan(t *testing.T, rate int, duration time.Duration) *Plan {
	s := testSpec()
	s.Rate, s.Warmup, s.Duration, s.Mix = rate, 0, duration, Mix{Rest: 100}
	return buildPlan(t, s)
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("runner test channel deadline")
		var zero T
		return zero
	}
}

func TestRunPreservesScheduleWhileWorkersAreBlocked(t *testing.T) {
	p := scheduledPlan(t, 200, 100*time.Millisecond)
	options := testRunOptions()
	options.MaxInFlight = 2
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	executor := executorFunc(func(ctx context.Context, r Request) Execution {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return Execution{Outcome: Outcome{Class: TransportTimeout, Certainty: "UNKNOWN"}}
		case <-release:
			return Execution{Outcome: Outcome{Class: Business}, WrittenAt: time.Now()}
		}
	})
	type finished struct {
		result RunResult
		err    error
	}
	done := make(chan finished, 1)
	go func() { r, err := Run(context.Background(), p, options, executor); done <- finished{r, err} }()
	receive(t, entered)
	receive(t, entered)
	// The offered window must finish independently while its two responses wait.
	time.Sleep(150 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("runner did not join outstanding executions: %+v", got)
	default:
	}
	once.Do(func() { close(release) })
	got := receive(t, done)
	if got.err != nil || got.result.Aborted || len(got.result.Samples) != 20 {
		t.Fatal(got)
	}
	started, capacity := 0, 0
	for i, sample := range got.result.Samples {
		if sample.Index != i || sample.FinishedAt < p.Request(i).Scheduled {
			t.Fatalf("missing or premature sample: %+v", sample)
		}
		if sample.Local == LocalCapacity {
			capacity++
			if sample.StartedAt != -1 || sample.WrittenAt != -1 {
				t.Fatal("local drop invented an execution timestamp")
			}
		} else if sample.Local == "" {
			started++
			if sample.StartedAt < p.Request(i).Scheduled || sample.WrittenAt < sample.StartedAt || sample.FinishedAt < sample.WrittenAt {
				t.Fatal("execution timestamps out of order")
			}
		} else {
			t.Fatal("unexpected local outcome", sample)
		}
	}
	if started != 2 || capacity != 18 {
		t.Fatalf("responses slowed or queued the offered load: started=%d capacity=%d", started, capacity)
	}
}

func TestRunRecordsEveryAttemptAndFixedWindow(t *testing.T) {
	p := scheduledPlan(t, 50, 100*time.Millisecond)
	result, err := Run(context.Background(), p, testRunOptions(), executorFunc(func(context.Context, Request) Execution {
		return Execution{Outcome: Outcome{Class: Business}, WrittenAt: time.Now()}
	}))
	if err != nil || result.Elapsed < p.Spec().Duration || result.ScheduleEpoch.IsZero() || result.FinishedUTC.Before(result.ScheduleEpoch) {
		t.Fatal("did not preserve the observation window", result, err)
	}
	for _, sample := range result.Samples {
		if sample.Local != "" || sample.Outcome.Class != Business || sample.StartedAt < p.Request(sample.Index).Scheduled {
			t.Fatal("low-rate request did not run as scheduled", sample)
		}
	}
}

type overrunClock struct{ current time.Time }

func (c *overrunClock) now() time.Time { return c.current }
func (c *overrunClock) wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay > 0 {
		c.current = c.current.Add(delay + 100*time.Millisecond)
	}
	return nil
}

func TestRunRecordsSchedulerLatenessInsteadOfCatchingUp(t *testing.T) {
	p := scheduledPlan(t, 1000, 20*time.Millisecond)
	options := testRunOptions()
	options.MaxLateness = 5 * time.Millisecond
	var calls atomic.Int64
	result, err := run(context.Background(), p, options, executorFunc(func(context.Context, Request) Execution {
		calls.Add(1)
		return Execution{}
	}), &overrunClock{current: time.Unix(0, 0)})
	if err != nil || calls.Load() != 0 || len(result.Samples) != 20 {
		t.Fatal("late scheduler executed a catch-up burst", calls.Load(), err)
	}
	for _, sample := range result.Samples {
		if sample.Local != LocalLate || sample.StartedAt != -1 || sample.FinishedAt-p.Request(sample.Index).Scheduled <= options.MaxLateness {
			t.Fatal("late request was hidden", sample)
		}
	}
}

func TestRunCancellationKeepsFutureAndAcceptedAttempts(t *testing.T) {
	p := scheduledPlan(t, 100, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 1)
	done := make(chan RunResult, 1)
	errorsSeen := make(chan error, 1)
	go func() {
		result, err := Run(ctx, p, testRunOptions(), executorFunc(func(ctx context.Context, _ Request) Execution {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return Execution{Outcome: Outcome{Class: TransportFailure, Certainty: "UNKNOWN"}}
		}))
		done <- result
		errorsSeen <- err
	}()
	receive(t, entered)
	cancel()
	result := receive(t, done)
	if err := receive(t, errorsSeen); !errors.Is(err, context.Canceled) || !result.Aborted || len(result.Samples) != p.Count() {
		t.Fatal("cancellation lost the complete attempt inventory", err)
	}
	local, unknown := 0, 0
	for _, sample := range result.Samples {
		if sample.Local == LocalCanceled {
			local++
		} else if sample.Local == "" && sample.Outcome.Certainty == "UNKNOWN" {
			unknown++
		} else {
			t.Fatal("unexpected canceled outcome", sample)
		}
	}
	if local == 0 || unknown == 0 {
		t.Fatal("did not distinguish unsent from already executing requests")
	}
}

func TestRunTimeoutAndPanicRetainSamples(t *testing.T) {
	for _, panicExecutor := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "panic"}[panicExecutor], func(t *testing.T) {
			p := scheduledPlan(t, 10, 200*time.Millisecond)
			o := testRunOptions()
			o.RequestTimeout = 5 * time.Millisecond
			result, err := Run(context.Background(), p, o, executorFunc(func(ctx context.Context, _ Request) Execution {
				if panicExecutor {
					panic("fixture")
				}
				<-ctx.Done()
				return Execution{Outcome: Outcome{Class: TransportTimeout, Certainty: "UNKNOWN"}}
			}))
			if (err != nil) != panicExecutor || result.Aborted != panicExecutor || len(result.Samples) != 2 {
				t.Fatal("wrong run failure behavior", result, err)
			}
			for _, sample := range result.Samples {
				want := TransportTimeout
				if panicExecutor {
					want = InvalidResponse
				}
				if sample.Local != "" || sample.Outcome.Class != want || sample.Outcome.Certainty != "UNKNOWN" || sample.FinishedAt < sample.StartedAt {
					t.Fatal("failed execution disappeared", sample)
				}
			}
		})
	}
}

func TestRunValidationAndPreCanceledContext(t *testing.T) {
	p := scheduledPlan(t, 10, 100*time.Millisecond)
	for _, mutate := range []func(*RunOptions){func(o *RunOptions) { o.MaxInFlight = 0 }, func(o *RunOptions) { o.MaxInFlight = 65537 }, func(o *RunOptions) { o.RequestTimeout = 0 }, func(o *RunOptions) { o.RequestTimeout = 2 * time.Minute }, func(o *RunOptions) { o.MaxLateness = 0 }, func(o *RunOptions) { o.MaxLateness = 2 * time.Second }} {
		o := testRunOptions()
		mutate(&o)
		if _, err := Run(context.Background(), p, o, executorFunc(func(context.Context, Request) Execution { t.Fatal("invalid options executed"); return Execution{} })); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := Run(context.Background(), p, testRunOptions(), nil); err == nil {
		t.Fatal("nil executor accepted")
	}
	if _, err := Run(context.Background(), &Plan{}, testRunOptions(), executorFunc(func(context.Context, Request) Execution { return Execution{} })); err == nil {
		t.Fatal("unbuilt zero-value plan accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Run(ctx, p, testRunOptions(), executorFunc(func(context.Context, Request) Execution { t.Fatal("canceled run executed"); return Execution{} }))
	if !errors.Is(err, context.Canceled) || !result.Aborted || len(result.Samples) != 1 || result.Samples[0].Local != LocalCanceled {
		t.Fatal(result, err)
	}
}
