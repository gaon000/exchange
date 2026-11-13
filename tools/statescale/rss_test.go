package main

import (
	"testing"
	"time"
)

func TestRSSObservationSchedules(t *testing.T) {
	for _, smoke := range []bool{false, true} {
		idle, control := rssTimes(smoke)
		if len(idle) != 5 || len(control) != 3 || idle[0] != 0 || control[0] != 0 {
			t.Fatal("missing observation phase")
		}
		for _, times := range [][]time.Duration{idle, control} {
			for i := 1; i < len(times); i++ {
				if times[i] <= times[i-1] {
					t.Fatal("observation targets must increase")
				}
			}
		}
		if !smoke && (idle[4] != 15*time.Second || control[2] != time.Second) {
			t.Fatal("full observation window changed")
		}
		if smoke && idle[4] > 100*time.Millisecond {
			t.Fatal("smoke became a long experiment")
		}
	}
}
