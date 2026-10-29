package api

import (
	"sync/atomic"

	"github.com/gaon000/exchange/internal/telemetry"
)

type measurements struct {
	handler, encode, write                                                           telemetry.Histogram
	responses, success, clientErrors, serverErrors, writeErrors, encodeErrors, bytes atomic.Uint64
}

// Statistics measures all HTTP requests, including health/read requests and
// admission failures. Write includes the final Flush but not client receipt.
// Bytes counts returned Write bytes, including partial writes; a subsequent
// Flush can still fail. It does not prove delivery to a client.
type Statistics struct {
	Handler      telemetry.Distribution `json:"handler"`
	Encode       telemetry.Distribution `json:"encode"`
	Write        telemetry.Distribution `json:"write"`
	Responses    uint64                 `json:"responses,string"`
	Status2xx    uint64                 `json:"status2xx,string"`
	Status4xx    uint64                 `json:"status4xx,string"`
	Status5xx    uint64                 `json:"status5xx,string"`
	WriteErrors  uint64                 `json:"writeErrors,string"`
	EncodeErrors uint64                 `json:"encodeErrors,string"`
	Bytes        uint64                 `json:"bytes,string"`
	Active       int                    `json:"active"`
}

// Statistics copies diagnostic instruments without invoking runtime queries.
func (s *Server) Statistics() Statistics {
	m := &s.metrics
	return Statistics{m.handler.Snapshot(), m.encode.Snapshot(), m.write.Snapshot(), m.responses.Load(), m.success.Load(), m.clientErrors.Load(), m.serverErrors.Load(), m.writeErrors.Load(), m.encodeErrors.Load(), m.bytes.Load(), len(s.slots)}
}
