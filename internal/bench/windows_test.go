package bench

import (
	"testing"
	"time"
)

func TestWindowsUseScheduledCohortsAndIncludeLateResponses(t *testing.T) {
	p, result := summaryFixture(t)
	w, err := SummarizeWindows(p, result, time.Second)
	if err != nil || len(w) != 2 {
		t.Fatal(w, err)
	}
	// Requests at 1.0/1.5s finish at 1.05/2.4s. The slow second response
	// stays in the first window even though it completes in the next one.
	if w[0].Start != time.Second || w[0].End != 2*time.Second || w[0].Scheduled != 2 || w[0].Started != 2 || w[0].RequestLatency.P99 != 900*time.Millisecond || w[0].RequestLatency.P50 != 50*time.Millisecond {
		t.Fatal(w[0])
	}
	if w[1].Scheduled != 2 || w[1].RequestLatency.P99 != 700*time.Millisecond {
		t.Fatal(w[1])
	}
	result.Samples[4] = Sample{Index: 4, Local: LocalCapacity, StartedAt: -1, WrittenAt: -1, FinishedAt: 2 * time.Second}
	result.Samples[5].Outcome = Outcome{Class: TransportTimeout, Certainty: "UNKNOWN"}
	w, err = SummarizeWindows(p, result, time.Second)
	if err != nil || w[1].LocalFailures != 1 || w[1].OperationalFailures != 1 || w[1].Started != 1 || w[1].ScheduledAttemptLatency.P99 != 700*time.Millisecond {
		t.Fatal(w, err)
	}
	result.Samples[5].StartedAt += 100 * time.Millisecond
	result.Samples[5].WrittenAt = -1
	w, err = SummarizeWindows(p, result, time.Second)
	if err != nil || w[1].RequestLatency.P99 != 600*time.Millisecond || w[1].ScheduledAttemptLatency.P99 != 700*time.Millisecond {
		t.Fatal(w, err)
	}
}

func TestWindowsRejectMissingSamplesAndInvalidBounds(t *testing.T) {
	p, result := summaryFixture(t)
	for _, width := range []time.Duration{0, -1, 3 * time.Second, time.Nanosecond} {
		if _, err := SummarizeWindows(p, result, width); err == nil {
			t.Fatal(width)
		}
	}
	if _, err := SummarizeWindows(nil, result, time.Second); err == nil {
		t.Fatal("nil plan accepted")
	}
	result.Samples = result.Samples[:len(result.Samples)-1]
	if _, err := SummarizeWindows(p, result, time.Second); err == nil {
		t.Fatal("missing sample accepted")
	}
}
