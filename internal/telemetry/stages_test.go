package telemetry

import (
	"sync"
	"testing"
)

func TestStagesAttemptsConcurrencyAndDetachedReports(t *testing.T) {
	var disabled *Stages
	p := disabled.Begin("unused")
	p.Next("alsoUnused")
	p.End()
	if len(disabled.Snapshot()) != 0 {
		t.Fatal("disabled collector recorded work")
	}
	var stages Stages
	active := stages.Begin("inProgress")
	if len(stages.Snapshot()) != 0 {
		t.Fatal("unfinished work reported as completed")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			span := stages.Begin("first")
			defer span.End()
			span.Next("second")
			span.End()
			span.End()
			_ = stages.Snapshot()
		})
	}
	wg.Wait()
	active.End()
	report := stages.Snapshot()
	if report["first"].Count != 20 || report["second"].Count != 20 || report["inProgress"].Count != 1 {
		t.Fatal(report)
	}
	clone := report.Clone()
	clone["first"].Buckets[0].Count = 999
	delete(clone, "second")
	report["first"].Buckets[0].Count = 888
	fresh := stages.Snapshot()
	if fresh["first"].Buckets[0].Count > 20 || fresh["second"].Count != 20 || report["second"].Count != 20 {
		t.Fatal("report alias")
	}
}
