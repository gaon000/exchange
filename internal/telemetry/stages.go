package telemetry

import (
	"sync"
	"time"
)

// StageReport contains completed stage attempts, including failed attempts.
// Nested stages overlap their parents; their durations must not be added twice.
type StageReport map[string]Distribution

// Clone returns a detached report, including each histogram's buckets.
func (r StageReport) Clone() StageReport {
	copy := make(StageReport, len(r))
	for name, value := range r {
		value.Buckets = append([]Bucket{}, value.Buckets...)
		copy[name] = value
	}
	return copy
}

// Stages collects concurrent, process-local stage timings. Names must come from
// a fixed set in code, never user input. The zero value is ready; nil disables
// timing. Do not copy after use. Work in progress is absent from Snapshot.
type Stages struct {
	mu     sync.Mutex
	values map[string]*Histogram
}

// Span measures sequential stages on one goroutine. End is idempotent; callers
// should defer End to include early errors and panics in the active stage.
type Span struct {
	stages  *Stages
	name    string
	started time.Time
}

// Begin starts one monotonic wall-clock measurement (including GC and I/O wait).
func (s *Stages) Begin(name string) *Span {
	p := &Span{stages: s}
	p.Next(name)
	return p
}

// Next finishes the previous stage and begins the named stage.
func (p *Span) Next(name string) {
	p.End()
	if p.stages != nil {
		p.name, p.started = name, time.Now()
	}
}

// End finishes the active stage once, without counting later calls again.
func (p *Span) End() {
	if p.stages == nil || p.started.IsZero() {
		return
	}
	elapsed := time.Since(p.started)
	p.stages.mu.Lock()
	defer p.stages.mu.Unlock()
	if p.stages.values == nil {
		p.stages.values = make(map[string]*Histogram)
	}
	h := p.stages.values[p.name]
	if h == nil {
		h = &Histogram{}
		p.stages.values[p.name] = h
	}
	h.Observe(elapsed)
	p.started = time.Time{}
}

// Snapshot returns detached stage distributions. Completed stages are coherent;
// a parent can still be running when some of its children have been recorded.
func (s *Stages) Snapshot() StageReport {
	r := make(StageReport)
	if s == nil {
		return r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, h := range s.values {
		r[name] = h.Snapshot()
	}
	return r
}
