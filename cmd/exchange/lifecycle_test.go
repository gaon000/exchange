package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var injected = errors.New("injected lifecycle failure")

type gate struct {
	once sync.Once
	c    chan struct{}
}

func newGate() *gate  { return &gate{c: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.c) }) }

type testRuntime struct {
	ready                   atomic.Bool
	done, stopping, release *gate
	err                     error
	stopOnce                sync.Once
}

func newRuntime(err error) *testRuntime {
	r := &testRuntime{done: newGate(), stopping: newGate(), release: newGate(), err: err}
	r.ready.Store(true)
	return r
}
func (r *testRuntime) Ready() bool           { return r.ready.Load() }
func (r *testRuntime) Done() <-chan struct{} { return r.done.c }
func (r *testRuntime) Wait(ctx context.Context) error {
	select {
	case <-r.done.c:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *testRuntime) Shutdown(ctx context.Context) error {
	r.stopOnce.Do(func() { r.ready.Store(false); r.stopping.open(); go func() { <-r.release.c; r.done.open() }() })
	return r.Wait(ctx)
}

type testListener struct{ closed *gate }

func (l testListener) Accept() (net.Conn, error) { <-l.closed.c; return nil, net.ErrClosed }
func (l testListener) Close() error              { l.closed.open(); return nil }
func (l testListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

type testHTTP struct {
	l                                       testListener
	stopping, release, closed, shutdownDone *gate
	serveError                              error
}

func (h *testHTTP) Serve(l net.Listener) error {
	if h.serveError != nil {
		return h.serveError
	}
	_, _ = l.Accept()
	return http.ErrServerClosed
}
func (h *testHTTP) Shutdown(ctx context.Context) error {
	h.stopping.open()
	_ = h.l.Close()
	defer h.shutdownDone.open()
	select {
	case <-h.release.c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *testHTTP) Close() error { h.closed.open(); h.release.open(); return h.l.Close() }

type reportWriter struct {
	bytes.Buffer
	ready *gate
}

func (w *reportWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.ready.open()
	return n, err
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("channel deadline")
		var zero T
		return zero
	}
}

func fixture(t *testing.T) (*testRuntime, *testHTTP, *reportWriter) {
	t.Helper()
	r := newRuntime(nil)
	h := &testHTTP{l: testListener{newGate()}, stopping: newGate(), release: newGate(), closed: newGate(), shutdownDone: newGate()}
	w := &reportWriter{ready: newGate()}
	t.Cleanup(func() { r.release.open(); h.release.open(); _ = h.Close() })
	return r, h, w
}

func TestSuperviseDrainsHTTPAndRuntimeInEitherCompletionOrder(t *testing.T) {
	for _, httpFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "runtime first", true: "HTTP first"}[httpFirst], func(t *testing.T) {
			r, h, w := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- supervise(ctx, r, h, h.l, readyReport{Event: "ready"}, w, time.Second) }()
			receive(t, w.ready.c)
			cancel()
			receive(t, r.stopping.c)
			receive(t, h.stopping.c)
			if httpFirst {
				h.release.open()
				receive(t, h.shutdownDone.c)
			} else {
				r.release.open()
				receive(t, r.done.c)
			}
			select {
			case err := <-done:
				t.Fatalf("returned before both drains: %v", err)
			default:
			}
			r.release.open()
			h.release.open()
			if err := receive(t, done); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.closed.c:
				t.Fatal("normal drain forced HTTP close")
			default:
			}
		})
	}
}

func TestHTTPShutdownDeadlineNeverAbandonsStorageDrain(t *testing.T) {
	r, h, w := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervise(ctx, r, h, h.l, readyReport{Event: "ready"}, w, 10*time.Millisecond) }()
	receive(t, w.ready.c)
	cancel()
	receive(t, h.closed.c)
	receive(t, r.stopping.c)
	select {
	case err := <-done:
		t.Fatalf("HTTP deadline abandoned storage: %v", err)
	default:
	}
	r.release.open()
	if err := receive(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestRuntimeFailureStopsHTTPWithoutSignal(t *testing.T) {
	r, h, w := fixture(t)
	r.err = injected
	done := make(chan error, 1)
	go func() {
		done <- supervise(context.Background(), r, h, h.l, readyReport{Event: "ready"}, w, time.Second)
	}()
	receive(t, w.ready.c)
	r.ready.Store(false)
	r.done.open()
	r.release.open()
	receive(t, h.stopping.c)
	h.release.open()
	if err := receive(t, done); !errors.Is(err, injected) {
		t.Fatal(err)
	}
}

func TestStartupAndOutputFailureAlwaysCloseRuntime(t *testing.T) {
	for _, mode := range []string{"serve error", "output error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			r, h, w := fixture(t)
			r.release.open()
			h.release.open()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output io.Writer = w
			switch mode {
			case "serve error":
				h.serveError = injected
			case "output error":
				output = brokenWriter{}
			case "cancelled":
				cancel()
			}
			err := supervise(ctx, r, h, h.l, readyReport{Event: "ready"}, output, time.Second)
			if mode == "serve error" && !errors.Is(err, injected) || mode == "output error" && !errors.Is(err, io.ErrClosedPipe) || mode == "cancelled" && err != nil {
				t.Fatal(err)
			}
			receive(t, r.done.c)
			receive(t, h.shutdownDone.c)
			if mode != "output error" && w.Len() != 0 {
				t.Fatal("ready reported after failed startup")
			}
		})
	}
}
