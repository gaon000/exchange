// Package telemetry provides bounded process-local diagnostic measurements.
// It never changes command admission, persistence or business results.
package telemetry

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Fixed bounds are 0, 1/2/5 times powers of ten nanoseconds, then MaxInt64.
// Sparse snapshots retain nonempty buckets; omitted buckets have count zero.
var bounds = func() []uint64 {
	b := []uint64{0}
	for n := uint64(1); n <= 1_000_000_000_000_000_000; n *= 10 {
		for _, m := range []uint64{1, 2, 5} {
			if n*m <= math.MaxInt64 {
				b = append(b, n*m)
			}
		}
		if n == 1_000_000_000_000_000_000 {
			break
		}
	}
	return append(b, math.MaxInt64)
}()

// Bucket counts durations <= UpperNanos and > the preceding fixed bound.
type Bucket struct {
	UpperNanos uint64 `json:"upperNanos,string"`
	Count      uint64 `json:"count,string"`
}

// Distribution is a coherent snapshot of one histogram, not an exact quantile
// list. Sums saturate on overflow, which is explicit; count zero is unobserved.
type Distribution struct {
	Count           uint64   `json:"count,string"`
	SumNanos        uint64   `json:"sumNanos,string"`
	MinNanos        uint64   `json:"minNanos,string"`
	MaxNanos        uint64   `json:"maxNanos,string"`
	NegativeSamples uint64   `json:"negativeSamples,string"`
	SumOverflow     bool     `json:"sumOverflow"`
	Buckets         []Bucket `json:"buckets"`
}

// Histogram has fixed storage and no per-observation allocation. Zero is ready
// to use. Do not copy it after use; readers may run concurrently with writers.
type Histogram struct {
	mu                             sync.Mutex
	counts                         [60]uint64
	count, sum, min, max, negative uint64
	overflow                       bool
}

// Observe records a monotonic duration; negative inputs are reported separately.
func (h *Histogram) Observe(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d < 0 {
		h.negative++
		return
	}
	n := uint64(d)
	i := sort.Search(len(bounds), func(i int) bool { return bounds[i] >= n })
	h.counts[i]++
	if h.count == 0 || n < h.min {
		h.min = n
	}
	if n > h.max {
		h.max = n
	}
	h.count++
	if math.MaxUint64-h.sum < n {
		h.sum = math.MaxUint64
		h.overflow = true
	} else {
		h.sum += n
	}
}

// Snapshot copies one consistent distribution. Different histograms may be
// observed at slightly different instants while the server continues to run.
func (h *Histogram) Snapshot() Distribution {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := Distribution{Count: h.count, SumNanos: h.sum, MinNanos: h.min, MaxNanos: h.max, NegativeSamples: h.negative, SumOverflow: h.overflow, Buckets: []Bucket{}}
	for i, b := range bounds {
		if h.counts[i] != 0 {
			s.Buckets = append(s.Buckets, Bucket{b, h.counts[i]})
		}
	}
	return s
}
