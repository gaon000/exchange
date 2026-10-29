package wal

import (
	"sync/atomic"

	"github.com/gaon000/exchange/internal/telemetry"
)

type measurements struct {
	encode, write, sync             telemetry.Histogram
	groups, commands, bytes, errors atomic.Uint64
}

// Statistics describes append activity in this Store instance, excluding init,
// recovery sync and replay. EncodedBytes includes frames attempted before I/O;
// it is not disk growth or proof of durability. Groups counts successful syncs;
// IOErrors counts Write/Sync failures, excluding validation and later Seek errors.
type Statistics struct {
	Encode       telemetry.Distribution `json:"encode"`
	Write        telemetry.Distribution `json:"write"`
	Sync         telemetry.Distribution `json:"sync"`
	Groups       uint64                 `json:"groups,string"`
	Commands     uint64                 `json:"commands,string"`
	EncodedBytes uint64                 `json:"encodedBytes,string"`
	IOErrors     uint64                 `json:"ioErrors,string"`
}

// Statistics can be read concurrently with Append or after Close. No file or
// writer state is accessed; individual histogram snapshots are consistent.
func (s *Store) Statistics() Statistics {
	m := &s.metrics
	return Statistics{m.encode.Snapshot(), m.write.Snapshot(), m.sync.Snapshot(), m.groups.Load(), m.commands.Load(), m.bytes.Load(), m.errors.Load()}
}
