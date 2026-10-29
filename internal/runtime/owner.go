package runtime

import (
	"errors"
	"fmt"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/wal"
)

type pendingCommand struct {
	record  wal.Record
	waiters []*commandRequest
}
type writeReply struct {
	durable d.Int
	err     error
	code    string
}
type owner struct {
	r            *Runtime
	pending      map[d.Key]*pendingCommand
	batch        []wal.Record
	payloadBytes int
	held         *commandRequest // at most one dequeued command that starts the next group
	current      *commandRequest
	currentQuery *queryRequest
	deadline     time.Time
	timer        *time.Timer
	busy         bool
	draining     bool
	jobs         chan []wal.Record
	writes       chan writeReply
	writerDone   chan struct{}
}

func (r *Runtime) run() {
	o := &owner{r: r, pending: make(map[d.Key]*pendingCommand), jobs: make(chan []wal.Record), writes: make(chan writeReply, 1), writerDone: make(chan struct{})}
	go func() {
		defer close(o.writerDone)
		for batch := range o.jobs {
			o.writes <- writeGroup(r.store, batch)
		}
	}()
	defer func() {
		if p := recover(); p != nil {
			o.fail(failure("INTERNAL_FAILURE", Unknown, fmt.Errorf("owner panic: %v", p)))
		}
		r.stopAdmission()
		o.stopTimer()
		close(o.jobs)
		<-o.writerDone // never close a WAL handle while its writer is still running
		r.terminal = errors.Join(r.terminal, r.store.Close())
		close(r.done)
	}()
	stop := r.stop
	for {
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
				o.jobs <- o.batch
			} else if o.held != nil {
				req := o.held
				o.held = nil
				o.current = req
				o.admit(req)
				o.current = nil
				continue
			} else if o.draining && len(o.batch) == 0 && len(r.commands) == 0 && len(r.queries) == 0 {
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
		return
	}
	result, found, err := r.engine.Lookup(c)
	if err != nil {
		replyCommand(req, d.Result{}, failure(engineFailureCode(err), NotAdmitted, err))
		return
	}
	if found {
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
	p := &pendingCommand{record: record}
	if req.ctx.Err() == nil {
		p.waiters = []*commandRequest{req}
	}
	o.pending[key] = p
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
	for _, record := range o.batch {
		if o.r.hooks.beforeApply != nil {
			o.r.hooks.beforeApply(record)
		}
		result, err := o.r.engine.Apply(record.Seq, record.Command)
		if err != nil || o.r.engine.AppliedSeq() != record.Seq {
			o.fail(failure("INTERNAL_FAILURE", Unknown, errors.Join(err, engine.ErrSequence)))
			return false
		}
		if o.r.hooks.afterApply != nil {
			o.r.hooks.afterApply(record)
		}
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
	o.r.stopAdmission()
	o.r.terminal = err
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
