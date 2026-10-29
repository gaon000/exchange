package runtime

import (
	"sync/atomic"

	"github.com/gaon000/exchange/internal/telemetry"
	"github.com/gaon000/exchange/internal/wal"
)

type measurements struct {
	commandQueue, queryQueue, batchWait, append, apply, query, capture, publish                                                               telemetry.Histogram
	enqueued, unique, applied, cached, pending, batches, batchCommands, batchBytes, batchPeak, queuePeak, failures, snapshots, snapshotErrors atomic.Uint64
}

// Statistics contains process-local cumulative work and approximate queue depth
// observations. Queue peak is sampled after enqueue and may miss a fast dequeue.
// It is not a transactional view of business state or a durable progress marker.
type Statistics struct {
	CommandQueueWait         telemetry.Distribution `json:"commandQueueWait"`
	QueryQueueWait           telemetry.Distribution `json:"queryQueueWait"`
	BatchWait                telemetry.Distribution `json:"batchWait"`
	Append                   telemetry.Distribution `json:"append"`
	Apply                    telemetry.Distribution `json:"apply"`
	Query                    telemetry.Distribution `json:"query"`
	SnapshotCapture          telemetry.Distribution `json:"snapshotCapture"` // detached copying on owner
	SnapshotPublish          telemetry.Distribution `json:"snapshotPublish"` // canonical sorting and publication on worker
	Enqueued                 uint64                 `json:"enqueued,string"`
	Unique                   uint64                 `json:"unique,string"`
	Applied                  uint64                 `json:"applied,string"`
	CachedRetries            uint64                 `json:"cachedRetries,string"`
	PendingRetries           uint64                 `json:"pendingRetries,string"`
	Batches                  uint64                 `json:"batches,string"`
	BatchCommands            uint64                 `json:"batchCommands,string"`
	BatchPayloadBytes        uint64                 `json:"batchPayloadBytes,string"`
	BatchPeak                uint64                 `json:"batchPeak,string"`
	ObservedCommandQueuePeak uint64                 `json:"observedCommandQueuePeak,string"`
	CommandQueueDepth        int                    `json:"commandQueueDepth"`
	QueryQueueDepth          int                    `json:"queryQueueDepth"`
	AwaitingCalls            int                    `json:"awaitingCalls"`
	Failures                 uint64                 `json:"failures,string"`
	Snapshots                uint64                 `json:"snapshots,string"`
	SnapshotErrors           uint64                 `json:"snapshotErrors,string"`
	WAL                      *wal.Statistics        `json:"wal"`
}

// Statistics reads only concurrency-safe instruments and channel lengths. A nil
// WAL report means the injected journal does not provide native measurements.
func (r *Runtime) Statistics() Statistics {
	m := &r.metrics
	s := Statistics{CommandQueueWait: m.commandQueue.Snapshot(), QueryQueueWait: m.queryQueue.Snapshot(), BatchWait: m.batchWait.Snapshot(), Append: m.append.Snapshot(), Apply: m.apply.Snapshot(), Query: m.query.Snapshot(), SnapshotCapture: m.capture.Snapshot(), SnapshotPublish: m.publish.Snapshot(), Enqueued: m.enqueued.Load(), Unique: m.unique.Load(), Applied: m.applied.Load(), CachedRetries: m.cached.Load(), PendingRetries: m.pending.Load(), Batches: m.batches.Load(), BatchCommands: m.batchCommands.Load(), BatchPayloadBytes: m.batchBytes.Load(), BatchPeak: m.batchPeak.Load(), ObservedCommandQueuePeak: m.queuePeak.Load(), CommandQueueDepth: len(r.commands), QueryQueueDepth: len(r.queries), AwaitingCalls: len(r.slots), Failures: m.failures.Load(), Snapshots: m.snapshots.Load(), SnapshotErrors: m.snapshotErrors.Load()}
	if source, ok := r.store.(interface{ Statistics() wal.Statistics }); ok {
		w := source.Statistics()
		s.WAL = &w
	}
	return s
}

func peak(counter *atomic.Uint64, n uint64) {
	for old := counter.Load(); n > old; old = counter.Load() {
		if counter.CompareAndSwap(old, n) {
			return
		}
	}
}
