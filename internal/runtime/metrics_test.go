package runtime

import (
	"testing"
)

func TestMetricsDuringBlockedStorageDoNotClaimApply(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f := fake(t)
		f.gate = make(chan error, 1)
		o := DefaultOptions()
		o.BatchCommands = 1
		r := startTest(t, f, o, hooks{})
		result := submitAsync(r, ctx(t), buy("one"))
		receive(t, f.calls)
		for i := 0; i < 20; i++ {
			s := r.Statistics()
			if s.Unique != 1 || s.BatchCommands != 1 || s.BatchWait.Count != 1 || s.CommandQueueWait.Count != 1 || s.Applied != 0 || s.Apply.Count != 0 || s.Append.Count != 0 || s.WAL != nil {
				t.Fatalf("early or fabricated progress: %+v", s)
			}
		}
		if _, err := r.Read(ctx(t), Query{RunID: runID, AccountID: "alice", Kind: BalancesQuery}); err != nil {
			t.Fatal(err)
		}
		if failed {
			f.gate <- injected
		} else {
			f.gate <- nil
		}
		reply := receive(t, result)
		if failed {
			checkFailure(t, reply.err, "STORAGE_UNAVAILABLE", Unknown)
		} else if reply.err != nil {
			t.Fatal(reply.err)
		}
		_ = r.Shutdown(ctx(t))
		s := r.Statistics()
		if s.Append.Count != 1 || s.Query.Count != 1 || s.QueryQueueWait.Count != 1 {
			t.Fatal(s)
		}
		if failed {
			if s.Applied != 0 || s.Apply.Count != 0 || s.Failures != 1 {
				t.Fatal(s)
			}
		} else if s.Applied != 1 || s.Apply.Count != 1 || s.Failures != 0 {
			t.Fatal(s)
		}
	}
}

func TestSnapshotMetricsSeparateCopyAndPublish(t *testing.T) {
	f := fake(t)
	o := DefaultOptions()
	o.BatchCommands = 1
	r, files := startSnapshotTest(t, f, o, hooks{}, true)
	if _, err := r.Submit(ctx(t), buy("one")); err != nil {
		t.Fatal(err)
	}
	request := snapshotAsync(r, ctx(t))
	receive(t, files.started)
	if s := r.Statistics(); s.SnapshotCapture.Count != 1 || s.SnapshotPublish.Count != 0 || s.Snapshots != 0 {
		t.Fatal(s)
	}
	files.gate <- nil
	if reply := receive(t, request); reply.err != nil {
		t.Fatal(reply.err)
	}
	if s := r.Statistics(); s.SnapshotPublish.Count != 1 || s.Snapshots != 1 || s.SnapshotErrors != 0 {
		t.Fatal(s)
	}
	request = snapshotAsync(r, ctx(t))
	receive(t, files.started)
	files.gate <- injected
	if reply := receive(t, request); reply.err == nil {
		t.Fatal("publication failure lost")
	}
	_ = r.Shutdown(ctx(t))
	if s := r.Statistics(); s.SnapshotCapture.Count != 2 || s.SnapshotPublish.Count != 2 || s.Snapshots != 1 || s.SnapshotErrors != 1 || s.Failures != 1 {
		t.Fatal(s)
	}
}
