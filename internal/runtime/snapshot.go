package runtime

import (
	"context"
	"fmt"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/snapshot"
)

// SnapshotResult identifies the command boundary durably published on disk.
// It does not include commands still queued, batched, or awaiting WAL sync when
// the owner captured the state. Snapshot does not act as a command-queue barrier.
type SnapshotResult struct {
	RunID       string
	SnapshotSeq d.Int
}

type snapshotRequest struct{ reply chan snapshotReply }
type snapshotReply struct {
	result SnapshotResult
	err    error
}
type snapshotWriteReply struct {
	seq  d.Int
	err  error
	code string
}

// Snapshot requests one detached owner-boundary copy and waits for strict file
// and directory sync. At most one request, copy, or publication is outstanding.
// Context cancellation stops waiting, not the already accepted publication.
func (r *Runtime) Snapshot(ctx context.Context) (SnapshotResult, error) {
	req := &snapshotRequest{reply: make(chan snapshotReply, 1)}
	r.admission.Lock()
	if err := r.reserve(ctx); err != nil {
		r.admission.Unlock()
		return SnapshotResult{}, err
	}
	if r.snapshots == nil || r.snapshotBusy {
		code := "SNAPSHOT_BUSY"
		if r.snapshots == nil {
			code = "SNAPSHOT_UNAVAILABLE"
		}
		<-r.slots
		r.admission.Unlock()
		return SnapshotResult{}, failure(code, NotAdmitted, nil)
	}
	r.snapshotBusy = true
	r.snapshotRequests <- req // capacity 1; guarded by snapshotBusy
	r.admission.Unlock()
	defer func() { <-r.slots }()
	select {
	case reply := <-req.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		return SnapshotResult{}, failure("SNAPSHOT_TIMEOUT", Unknown, ctx.Err())
	}
}

// SnapshotSeq is the last recovered or successfully acknowledged publication.
// Zero is also the Genesis boundary; Recovery distinguishes an initial file.
// A timeout/error does not prove a file is absent. Restart validates actual bytes.
func (r *Runtime) SnapshotSeq() d.Int { return d.Int(r.snapshotSeq.Load()) }

func (o *owner) captureSnapshot(req *snapshotRequest) {
	if o.snapshotRequest != nil {
		panic("multiple snapshot requests reached owner")
	}
	o.snapshotRequest = req // track before any copying hook/allocation can panic
	if o.r.hooks.beforeCapture != nil {
		o.r.hooks.beforeCapture()
	}
	seq := o.r.engine.AppliedSeq()
	if seq < o.r.SnapshotSeq() || seq > o.durableSeq {
		panic("snapshot sequence outside applied/durable boundary")
	}
	doc := snapshot.Document{Identity: snapshotIdentity(o.r.genesis), SnapshotSeq: seq, State: o.r.engine.Capture()}
	if o.r.hooks.afterCapture != nil {
		o.r.hooks.afterCapture(doc)
	}
	o.snapshotSequence = seq
	o.snapshotJobs <- doc // one bounded slot; publisher never reads the engine
}

func writeSnapshot(files checkpoints, doc snapshot.Document) (reply snapshotWriteReply) {
	reply.seq, reply.code = doc.SnapshotSeq, "STORAGE_UNAVAILABLE"
	defer func() {
		if p := recover(); p != nil {
			reply.err = fmt.Errorf("snapshot worker panic: %v", p)
			reply.code = "INTERNAL_FAILURE"
		}
	}()
	reply.err = files.Publish(doc)
	if reply.err == snapshot.ErrInvalid {
		reply.code = "INTERNAL_FAILURE"
	}
	return reply
}

func (o *owner) completeSnapshot(reply snapshotWriteReply) bool {
	if o.snapshotRequest == nil || reply.seq != o.snapshotSequence || reply.seq > o.r.engine.AppliedSeq() || reply.seq > o.durableSeq || reply.seq < o.r.SnapshotSeq() {
		o.fail(failure("INTERNAL_FAILURE", Unknown, fmt.Errorf("snapshot completion sequence mismatch")))
		return false
	}
	if reply.err != nil {
		// Native Publish returns this exact error before any filesystem access.
		// Do not use errors.Is: a joined cleanup/I/O failure must remain fatal.
		if reply.err == snapshot.ErrTooLarge {
			o.finishSnapshot(SnapshotResult{}, fmt.Errorf("snapshot size limit: %w", reply.err))
			return true
		}
		o.fail(failure(reply.code, Unknown, reply.err))
		return false
	}
	o.r.snapshotSeq.Store(int64(reply.seq))
	o.finishSnapshot(SnapshotResult{RunID: o.r.RunID(), SnapshotSeq: reply.seq}, nil)
	return true
}

func (o *owner) finishSnapshot(result SnapshotResult, err error) {
	req := o.snapshotRequest
	o.snapshotRequest = nil
	o.r.admission.Lock()
	o.r.snapshotBusy = false
	o.r.admission.Unlock()
	replySnapshot(req, result, err)
}

func replySnapshot(req *snapshotRequest, result SnapshotResult, err error) {
	if req == nil {
		return
	}
	if f, ok := err.(*Failure); ok {
		copy := *f
		err = &copy
	}
	select {
	case req.reply <- snapshotReply{result: result, err: err}:
	default:
	}
}

func (o *owner) failSnapshot(err error) {
	// Admission has already closed. There can be a queued request OR one owned
	// request. Reply without waiting for I/O, but retain the run lock until the
	// snapshot worker exits in run's deferred cleanup.
	o.finishSnapshot(SnapshotResult{}, err)
	select {
	case req := <-o.r.snapshotRequests:
		replySnapshot(req, SnapshotResult{}, err)
	default:
	}
}
