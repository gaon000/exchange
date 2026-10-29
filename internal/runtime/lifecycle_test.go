package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestWaitObservesTerminationWithoutStoppingAdmission(t *testing.T) {
	r := startTest(t, fake(t), DefaultOptions(), hooks{})
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Wait(c); !errors.Is(err, context.Canceled) || !r.Ready() {
		t.Fatal("Wait changed runtime", err)
	}
	select {
	case <-r.Done():
		t.Fatal("Done closed while running")
	default:
	}
	if _, err := r.Submit(ctx(t), buy("after-wait")); err != nil {
		t.Fatal(err)
	}
	finish(t, r)
	receive(t, r.Done())
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReportsStorageFailureAfterWorkerCleanup(t *testing.T) {
	f := fake(t)
	f.gate = make(chan error)
	r := startTest(t, f, DefaultOptions(), hooks{})
	result := submitAsync(r, ctx(t), buy("failure"))
	receive(t, f.calls)
	f.gate <- injected
	checkFailure(t, receive(t, result).err, "STORAGE_UNAVAILABLE", Unknown)
	if err := r.Wait(ctx(t)); !errors.Is(err, injected) || f.closed.Load() != 1 {
		t.Fatalf("terminal state: %v, closes %d", err, f.closed.Load())
	}
	receive(t, r.Done())
}
