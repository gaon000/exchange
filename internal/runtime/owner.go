package runtime

import (
	"errors"
	"fmt"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type pendingCommand struct {
	record    wal.Record
	waiters   []*commandRequest
	batchedAt time.Time
}
type writeReply struct {
	durable d.Int
	err     error
	code    string
}
type owner struct {
	r                *Runtime
	pending          map[d.Key]*pendingCommand
	batch            []wal.Record
	payloadBytes     int
	held             *commandRequest // at most one dequeued command that starts the next group
	current          *commandRequest
	currentQuery     *queryRequest
	deadline         time.Time
	timer            *time.Timer
	busy             bool
	draining         bool
	jobs             chan []wal.Record
	writes           chan writeReply
	writerDone       chan struct{}
	durableSeq       d.Int // owner-observed WAL acknowledgment; never read store during I/O
	snapshotRequest  *snapshotRequest
	snapshotSequence d.Int
	snapshotJobs     chan snapshot.Document
	snapshotWrites   chan snapshotWriteReply
	snapshotDone     chan struct{}
}

func (r *Runtime) run() {
	o := &owner{r: r, pending: make(map[d.Key]*pendingCommand), jobs: make(chan []wal.Record), writes: make(chan writeReply, 1), writerDone: make(chan struct{})}
	o.durableSeq = r.engine.AppliedSeq()
	o.snapshotJobs = make(chan snapshot.Document, 1)
	o.snapshotWrites = make(chan snapshotWriteReply, 1)
	o.snapshotDone = make(chan struct{})
	go func() {
		defer close(o.snapshotDone)
		for doc := range o.snapshotJobs {
			started := time.Now()
			reply := writeSnapshot(r.snapshots, doc, r.hooks.beforeSnapshotSort)
			r.metrics.publish.Observe(time.Since(started))
			if reply.err != nil {
				r.metrics.snapshotErrors.Add(1)
			}
			doc = snapshot.Document{} // release the full copy while waiting for the next job
			o.snapshotWrites <- reply
		}
	}()
	go func() {
		defer close(o.writerDone)
		for batch := range o.jobs {
			started := time.Now()
			reply := writeGroup(r.store, batch)
			r.metrics.append.Observe(time.Since(started))
			o.writes <- reply
		}
	}()
	defer func() {
		if p := recover(); p != nil {
			o.fail(failure("INTERNAL_FAILURE", Unknown, fmt.Errorf("owner panic: %v", p)))
		}
		r.stopAdmission()
		o.stopTimer()
		close(o.jobs)
		close(o.snapshotJobs)
		<-o.writerDone   // never close a WAL handle while its writer is still running
		<-o.snapshotDone // the WAL lifetime lock also covers snapshot file publication
		r.terminal = errors.Join(r.terminal, r.store.Close())
		close(r.done)
	}()
	stop := r.stop
	for {
		// Observe publication failures before dispatching another batch or query.
		select {
		case reply := <-o.snapshotWrites:
			if !o.completeSnapshot(reply) {
				return
			}
		default:
		}
		if !o.draining {
			select {
			case <-stop:
				o.draining = true
				stop = nil
			default:
			}
		}
		if !o.busy {
			if len(o.batch) > 0 && (len(o.batch) >= r.opts.BatchCommands || o.payloadBytes >= r.opts.BatchBytes || o.held != nil || o.draining || !time.Now().Before(o.deadline)) {
				o.stopTimer()
				o.busy = true
				now := time.Now()
				for _, record := range o.batch {
					r.metrics.batchWait.Observe(now.Sub(o.pending[record.Command.Key()].batchedAt))
				}
				r.metrics.batches.Add(1)
				r.metrics.batchCommands.Add(uint64(len(o.batch)))
				r.metrics.batchBytes.Add(uint64(o.payloadBytes))
				peak(&r.metrics.batchPeak, uint64(len(o.batch)))
				o.jobs <- o.batch
			} else if o.held != nil {
				req := o.held
				o.held = nil
				o.current = req
				o.admit(req)
				o.current = nil
				continue
			} else if o.draining && len(o.batch) == 0 && len(r.commands) == 0 && len(r.queries) == 0 && len(r.snapshotRequests) == 0 && o.snapshotRequest == nil {
				if err := r.engine.CheckInvariants(); err != nil {
					o.fail(failure("INTERNAL_FAILURE", Unknown, err))
				}
				return
			}
		}
		if o.busy {
			// Prefer completion over further queries once the durable result arrives.
			select {
			case reply := <-o.writes:
				if !o.apply(reply) {
					return
				}
				continue
			default:
			}
			select {
			case reply := <-o.writes:
				if !o.apply(reply) {
					return
				}
			case req := <-r.snapshotRequests:
				o.captureSnapshot(req)
			case reply := <-o.snapshotWrites:
				if !o.completeSnapshot(reply) {
					return
				}
			case query := <-r.queries:
				o.currentQuery = query
				r.readOwner(query, o.pending)
				o.currentQuery = nil
			case <-stop:
				o.draining = true
				stop = nil
			}
			continue
		}
		var tick <-chan time.Time
		if o.timer != nil {
			tick = o.timer.C
		}
		select {
		case req := <-r.snapshotRequests:
			o.captureSnapshot(req)
		case reply := <-o.snapshotWrites:
			if !o.completeSnapshot(reply) {
				return
			}
		case req := <-r.commands:
			o.current = req
			o.admit(req)
			o.current = nil
		case query := <-r.queries:
			o.currentQuery = query
			r.readOwner(query, o.pending)
			o.currentQuery = nil
		case <-tick: // the deadline check at the top cannot be starved by arrivals
		case <-stop:
			o.draining = true
			stop = nil
		}
	}
}

func writeGroup(s journal, batch []wal.Record) (reply writeReply) {
	reply.code = "STORAGE_UNAVAILABLE"
	defer func() {
		if p := recover(); p != nil {
			reply.err = fmt.Errorf("WAL writer panic: %v", p)
			reply.code = "INTERNAL_FAILURE"
		}
	}()
	reply.err = s.Append(batch)
	if reply.err == nil {
		reply.durable = s.DurableSeq()
	}
	return reply
}

func (o *owner) stopTimer() {
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
}

func (o *owner) admit(req *commandRequest) {
	r := o.r
	if !req.dequeued {
		req.dequeued = true
		if !req.queuedAt.IsZero() {
			r.metrics.commandQueue.Observe(time.Since(req.queuedAt))
		}
	}
	c := req.command
	key := c.Key()
	if p := o.pending[key]; p != nil {
		if p.record.Command != c {
			replyCommand(req, d.Result{}, failure("IDEMPOTENCY_CONFLICT", NotAdmitted, engine.ErrConflict))
			return
		}
		alive := p.waiters[:0]
		for _, waiter := range p.waiters {
			if waiter.ctx.Err() == nil {
				alive = append(alive, waiter)
			}
		}
		clear(p.waiters[len(alive):])
		p.waiters = alive
		if req.ctx.Err() != nil {
			return
		}
		if len(p.waiters) >= r.opts.PendingWaiters {
			replyCommand(req, d.Result{}, failure("DUPLICATE_WAITERS_FULL", NotAdmitted, nil))
			return
		}
		p.waiters = append(p.waiters, req)
		r.metrics.pending.Add(1)
		return
	}
	result, found, err := r.engine.Lookup(c)
	if err != nil {
		replyCommand(req, d.Result{}, failure(engineFailureCode(err), NotAdmitted, err))
		return
	}
	if found {
		r.metrics.cached.Add(1)
		replyCommand(req, result, nil)
		return
	}
	// Include assigned-but-not-yet-applied commands in the experiment limit.
	if d.Int(len(o.batch)) >= r.genesis.Config.MaxCommands-r.engine.AppliedSeq() {
		replyCommand(req, d.Result{}, failure("RUN_LIMIT_REACHED", NotAdmitted, engine.ErrRunLimit))
		return
	}
	record := wal.Record{Seq: r.engine.AppliedSeq() + d.Int(len(o.batch)) + 1, Command: c}
	size, err := wal.PayloadBytes(record)
	if err != nil {
		replyCommand(req, d.Result{}, failure("INVALID_REQUEST", NotAdmitted, err))
		return
	}
	if o.payloadBytes+size > r.opts.BatchBytes {
		o.held = req
		return
	}
	p := &pendingCommand{record: record, batchedAt: time.Now()}
	if req.ctx.Err() == nil {
		p.waiters = []*commandRequest{req}
	}
	o.pending[key] = p
	r.metrics.unique.Add(1)
	o.batch = append(o.batch, record)
	o.payloadBytes += size
	if len(o.batch) == 1 {
		o.deadline = time.Now().Add(r.opts.BatchDelay)
		o.timer = time.NewTimer(r.opts.BatchDelay)
	}
}

func engineFailureCode(err error) string {
	switch {
	case errors.Is(err, engine.ErrConflict):
		return "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, engine.ErrRunLimit):
		return "RUN_LIMIT_REACHED"
	case errors.Is(err, engine.ErrRunMismatch):
		return "RUN_MISMATCH"
	case errors.Is(err, engine.ErrUnknownAccount):
		return "UNKNOWN_ACCOUNT"
	default:
		return "INVALID_REQUEST"
	}
}

func (o *owner) apply(reply writeReply) bool {
	if reply.err != nil {
		o.fail(failure(reply.code, Unknown, reply.err))
		return false
	}
	if reply.durable != o.batch[len(o.batch)-1].Seq {
		o.fail(failure("INTERNAL_FAILURE", Unknown, fmt.Errorf("durable sequence mismatch")))
		return false
	}
	o.durableSeq = reply.durable
	for _, record := range o.batch {
		if o.r.hooks.beforeApply != nil {
			o.r.hooks.beforeApply(record)
		}
		started := time.Now()
		result, err := o.r.engine.Apply(record.Seq, record.Command)
		o.r.metrics.apply.Observe(time.Since(started))
		if err != nil || o.r.engine.AppliedSeq() != record.Seq {
			o.fail(failure("INTERNAL_FAILURE", Unknown, errors.Join(err, engine.ErrSequence)))
			return false
		}
		if o.r.hooks.afterApply != nil {
			o.r.hooks.afterApply(record)
		}
		o.r.metrics.applied.Add(1)
		p := o.pending[record.Command.Key()]
		for _, waiter := range p.waiters {
			replyCommand(waiter, result.Clone(), nil)
		}
		delete(o.pending, record.Command.Key())
	}
	o.batch = nil
	o.payloadBytes = 0
	o.busy = false
	return true
}

func replyCommand(req *commandRequest, result d.Result, err error) {
	if req != nil {
		if f, ok := err.(*Failure); ok {
			copy := *f
			err = &copy
		}
		select {
		case req.reply <- commandReply{result, err}:
		default:
		}
	}
}

func (o *owner) fail(err *Failure) {
	o.r.metrics.failures.Add(1)
	o.r.stopAdmission()
	o.r.terminal = err
	o.failSnapshot(err)
	for _, p := range o.pending {
		for _, waiter := range p.waiters {
			replyCommand(waiter, d.Result{}, err)
		}
	}
	replyCommand(o.current, d.Result{}, err)
	replyCommand(o.held, d.Result{}, err)
	if o.currentQuery != nil {
		select {
		case o.currentQuery.reply <- queryReply{err: failure("NOT_READY", NotAdmitted, err)}:
		default:
		}
	}
	for {
		select {
		case req := <-o.r.commands:
			replyCommand(req, d.Result{}, err)
		default:
			goto queries
		}
	}
queries:
	for {
		select {
		case req := <-o.r.queries:
			req.reply <- queryReply{err: failure("NOT_READY", NotAdmitted, err)}
		default:
			return
		}
	}
}
