package telemetry

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

func TestHistogramBoundsCopiesAndOverflow(t *testing.T) {
	var h Histogram
	if s := h.Snapshot(); s.Count != 0 || len(s.Buckets) != 0 {
		t.Fatal(s)
	}
	for _, n := range []time.Duration{0, 1, 2, 3, 5, 6, 10, 500, time.Millisecond} {
		h.Observe(n)
	}
	h.Observe(-1)
	s := h.Snapshot()
	if s.Count != 9 || s.MinNanos != 0 || s.MaxNanos != 1_000_000 || s.SumNanos != 1_000_527 || s.NegativeSamples != 1 {
		t.Fatalf("%+v", s)
	}
	var count uint64
	var upper uint64
	for i, b := range s.Buckets {
		count += b.Count
		if i > 0 && b.UpperNanos <= upper {
			t.Fatal("unordered bounds")
		}
		upper = b.UpperNanos
	}
	if count != s.Count {
		t.Fatal("lost observations")
	}
	s.Buckets[0].Count = 999
	if h.Snapshot().Buckets[0].Count != 1 {
		t.Fatal("shared snapshot")
	}
	var huge Histogram
	for i := 0; i < 3; i++ {
		huge.Observe(time.Duration(math.MaxInt64))
	}
	if s := huge.Snapshot(); !s.SumOverflow || s.SumNanos != math.MaxUint64 || s.Count != 3 {
		t.Fatal(s)
	}
}

func TestHistogramConcurrentSnapshotsAreInternallyConsistent(t *testing.T) {
	var h Histogram
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				h.Observe(time.Duration(j))
				s := h.Snapshot()
				var n uint64
				for _, b := range s.Buckets {
					n += b.Count
				}
				if n != s.Count {
					t.Error("inconsistent snapshot")
					return
				}
			}
		}()
	}
	wg.Wait()
	if h.Snapshot().Count != 8000 {
		t.Fatal("lost increments")
	}
}

func TestProcessMetricsAreAvailableAndJSONSafe(t *testing.T) {
	p := ReadProcess()
	if len(p.RuntimeUnavailable) != 0 || p.GoHeapObjectsBytes == 0 || p.GoTotalMemoryBytes < p.GoHeapObjectsBytes || p.GoGoroutines == 0 {
		t.Fatalf("runtime metrics: %+v", p)
	}
	if !p.OSAvailable && p.OSError == "" {
		t.Fatal("unavailable OS metrics were not explained")
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(PauseBucket{UpperSeconds: "+Inf", Count: 1}); err != nil {
		t.Fatal(err)
	}
}
