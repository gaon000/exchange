package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

// Runtime owns the engine and WAL lifecycle. Public methods may be called
// concurrently. Readiness opens only after replay, invariant checks, and sync.
type Runtime struct {
	genesis          wal.Genesis
	accounts         map[string]bool
	opts             Options
	engine           *engine.Engine
	store            journal
	commands         chan *commandRequest
	queries          chan *queryRequest
	slots            chan struct{}
	stop             chan struct{}
	done             chan struct{}
	ready            atomic.Bool
	admission        sync.Mutex
	closing          bool
	terminal         error // written before done closes; readers wait for done
	hooks            hooks
	recovery         RecoveryReport
	snapshots        checkpoints
	snapshotRequests chan *snapshotRequest
	snapshotBusy     bool // admission mutex; remains set after a waiting caller times out
	snapshotSeq      atomic.Int64
}
type hooks struct {
	beforeApply   func(wal.Record)
	afterApply    func(wal.Record)
	beforeCapture func()
	afterCapture  func(snapshot.Document)
}
type commandRequest struct {
	command d.Command
	ctx     context.Context
	reply   chan commandReply
}
type commandReply struct {
	result d.Result
	err    error
}
type queryRequest struct {
	query Query
	reply chan queryReply
}
type queryReply struct {
	view View
	err  error
}

// Open recovers an existing run without reinitializing it, then starts its owner.
func Open(dir string, options Options) (*Runtime, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s, err := wal.Open(dir)
	if err != nil {
		return nil, err
	}
	return startWithSnapshots(s, diskCheckpoints{dir}, options, hooks{})
}

func start(s journal, options Options, h hooks) (*Runtime, error) {
	return startWithSnapshots(s, nil, options, h)
}

func startWithSnapshots(s journal, files checkpoints, options Options, h hooks) (_ *Runtime, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("runtime recovery panic: %v", p)
		}
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	if err := options.Validate(); err != nil {
		return nil, err
	}
	g := s.Genesis()
	e, report, err := recoverState(s, files)
	if err != nil {
		return nil, err
	}
	if err := s.ConfirmRecovery(); err != nil {
		return nil, err
	}
	if s.DurableSeq() != e.AppliedSeq() {
		return nil, fmt.Errorf("recovery durable sequence mismatch")
	}
	r := &Runtime{genesis: g, accounts: make(map[string]bool), opts: options, engine: e, store: s, commands: make(chan *commandRequest, options.CommandQueue), queries: make(chan *queryRequest, options.QueryQueue), slots: make(chan struct{}, options.RequestLimit), stop: make(chan struct{}), done: make(chan struct{}), hooks: h}
	r.recovery, r.snapshots = report, files
	r.snapshotRequests = make(chan *snapshotRequest, 1)
	r.snapshotSeq.Store(int64(report.SnapshotSeq))
	for _, a := range g.Config.Accounts {
		r.accounts[a.AccountID] = true
	}
	r.ready.Store(true)
	go r.run()
	return r, nil
}

// Ready reports whether new requests may enter the recovered run.
func (r *Runtime) Ready() bool { return r.ready.Load() }

// RunID returns the immutable run identity required on requests.
func (r *Runtime) RunID() string { return r.genesis.RunID }

// Done closes after the owner and both file workers stop and the run lock is
// released. It does not initiate shutdown; use Wait to inspect the terminal error.
func (r *Runtime) Done() <-chan struct{} { return r.done }

// Wait observes completion without changing admission or cancelling accepted
// work. A waiting context may expire while the runtime keeps serving requests.
func (r *Runtime) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		return r.terminal
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) identity(runID, accountID string) error {
	if runID != r.genesis.RunID {
		return failure("RUN_MISMATCH", NotAdmitted, engine.ErrRunMismatch)
	}
	if !r.accounts[accountID] {
		return failure("UNKNOWN_ACCOUNT", NotAdmitted, engine.ErrUnknownAccount)
	}
	return nil
}

func (r *Runtime) reserve(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closing || !r.ready.Load() {
		return failure("NOT_READY", NotAdmitted, nil)
	}
	select {
	case r.slots <- struct{}{}:
		return nil
	default:
		return failure("TOO_MANY_REQUESTS", NotAdmitted, nil)
	}
}

// Submit returns the immutable initial result after durable storage and apply.
// Cancellation only stops waiting; a request already enqueued remains accepted.
func (r *Runtime) Submit(ctx context.Context, c d.Command) (d.Result, error) {
	if err := c.Validate(); err != nil {
		return d.Result{}, failure("INVALID_REQUEST", NotAdmitted, err)
	}
	if err := r.identity(c.RunID, c.AccountID); err != nil {
		return d.Result{}, err
	}
	req := &commandRequest{command: c, ctx: ctx, reply: make(chan commandReply, 1)}
	r.admission.Lock()
	if err := r.reserve(ctx); err != nil {
		r.admission.Unlock()
		return d.Result{}, err
	}
	select {
	case r.commands <- req:
		r.admission.Unlock()
	default:
		<-r.slots
		r.admission.Unlock()
		return d.Result{}, failure("QUEUE_FULL", NotAdmitted, nil)
	}
	defer func() { <-r.slots }()
	select {
	case res := <-req.reply:
		return res.result, res.err
	case <-ctx.Done():
		return d.Result{}, failure("RESULT_TIMEOUT", Unknown, ctx.Err())
	}
}

// Read copies the requested state at a complete command boundary. A timed-out
// query never exposes a live engine reference and has no state-changing effect.
func (r *Runtime) Read(ctx context.Context, q Query) (View, error) {
	if err := r.identity(q.RunID, q.AccountID); err != nil {
		return View{}, err
	}
	if q.ThroughSeq != nil {
		v := *q.ThroughSeq
		q.ThroughSeq = &v
	}
	if err := validateQuery(q); err != nil {
		return View{}, failure("INVALID_REQUEST", NotAdmitted, err)
	}
	req := &queryRequest{query: q, reply: make(chan queryReply, 1)}
	r.admission.Lock()
	if err := r.reserve(ctx); err != nil {
		r.admission.Unlock()
		return View{}, err
	}
	select {
	case r.queries <- req:
		r.admission.Unlock()
	default:
		<-r.slots
		r.admission.Unlock()
		return View{}, failure("QUEUE_FULL", NotAdmitted, nil)
	}
	defer func() { <-r.slots }()
	select {
	case res := <-req.reply:
		return res.view, res.err
	case <-ctx.Done():
		return View{}, failure("RESULT_TIMEOUT", Unknown, ctx.Err())
	}
}

// Shutdown stops new admission and drains accepted commands, queries, and the
// outstanding snapshot. A context timeout leaves the drain running; both file
// workers finish before the run lock is released. Repeated calls may wait for
// the same completion.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.stopAdmission()
	return r.Wait(ctx)
}

func (r *Runtime) stopAdmission() {
	r.admission.Lock()
	defer r.admission.Unlock()
	r.ready.Store(false)
	if !r.closing {
		r.closing = true
		close(r.stop)
	}
}
