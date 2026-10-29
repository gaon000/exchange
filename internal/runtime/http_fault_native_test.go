//go:build darwin && cgo

package runtime_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

func TestNativeHTTPRuntimeQueueAndRequestBounds(t *testing.T) {
	for _, kind := range []string{"command-queue", "query-queue", "runtime-limit"} {
		t.Run(kind, func(t *testing.T) {
			f := newHTTPFixture(t)
			entered, release := newHTTPGate(), newHTTPGate()
			defer release.open()
			o := httpOptions()
			o.CommandQueue, o.QueryQueue = 1, 1
			h := rt.HTTPTestHooks{}
			if kind == "command-queue" {
				h.Append = func(s *wal.Store, records []wal.Record) error {
					entered.open()
					release.wait()
					return s.Append(records)
				}
			} else {
				h.BeforeApply = func(wal.Record) { entered.open(); release.wait() }
			}
			if kind == "runtime-limit" {
				o.RequestLimit, o.PendingWaiters = 2, 2
			}
			r := httpOpen(t, f.dir, o, h)
			e := httpServe(t, r, nil)
			body := httpOrder("target", "BUY", 105, 2)
			first := httpAsync(e.client, e.base, f.run, "alice", "POST", "/v1/orders", body)
			httpAwait(t, entered.ch)
			method, path, queuedBody := "GET", "/v1/balances", ""
			if kind == "command-queue" {
				method, path, queuedBody = "POST", "/v1/orders", body
			}
			queued := httpAsync(e.client, e.base, f.run, "alice", method, path, queuedBody)
			httpEventually(t, func() bool {
				commands, queries := rt.HTTPTestQueues(r)
				return kind == "command-queue" && commands == 1 || kind != "command-queue" && queries == 1
			})
			start := time.Now()
			overflow := e.call(f.run, "alice", method, path, queuedBody)
			code := "QUEUE_FULL"
			if kind == "runtime-limit" {
				code = "TOO_MANY_REQUESTS"
			}
			httpFailure(t, overflow, 429, code, rt.NotAdmitted)
			if time.Since(start) > 2*time.Second {
				t.Fatal("overflow waited instead of rejecting while the gate was held")
			}
			select {
			case got := <-first:
				t.Fatalf("success escaped blocked storage/apply: %+v", got)
			default:
			}
			if kind == "command-queue" {
				httpStatus(t, e.call(f.run, "alice", "GET", "/v1/results/place/target", ""), 202)
				balance := httpDecode[api.BalancesView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 200))
				if balance.AsOfSeq != 1 || balance.USDT.AvailableAtoms != 100000 || balance.USDT.ReservedAtoms != 0 {
					t.Fatal("storage wait exposed uncommitted reservation", balance)
				}
			}
			release.open()
			if got := httpStatus(t, httpAwait(t, first), 200); !bytes.Equal(got, f.target) {
				t.Fatal("first response differs from normal execution")
			}
			got := httpStatus(t, httpAwait(t, queued), 200)
			if kind == "command-queue" && !bytes.Equal(got, f.target) {
				t.Fatal("queued duplicate consumed another sequence")
			}
			if kind != "command-queue" && httpDecode[api.BalancesView](t, got).AsOfSeq != 2 {
				t.Fatal("queued query exposed a partial apply")
			}
			e.close(t)
			httpStop(t, r)
			assertHTTPRecovered(t, f, 2)
		})
	}
}

// writeHTTPFaultOutcome substitutes only the failing Append call. The normal
// prefix, frame bytes, runtime, HTTP transport and subsequent recovery are real.
// Exact Store write/sync-call injection is covered by wal/store_test.go; here we
// model its possible on-disk outcomes without adding production injection APIs.
func writeHTTPFaultOutcome(f httpFixture, phase string) error {
	if phase == "before-write" {
		return nil
	}
	file, err := os.OpenFile(filepath.Join(f.dir, "commands.wal"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	data := f.group
	if phase == "partial-write" {
		data = data[:len(data)/2]
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = fmt.Errorf("fault fixture short write: %d/%d", n, len(data))
	}
	if err == nil && phase == "after-sync" {
		err = durable.Sync(file)
	}
	return errors.Join(err, file.Close())
}

func TestNativeHTTPWALFailureOutcomesAndRecovery(t *testing.T) {
	for _, phase := range []string{"before-write", "partial-write", "before-sync", "after-sync"} {
		t.Run(phase, func(t *testing.T) {
			f := newHTTPFixture(t)
			entered, release := newHTTPGate(), newHTTPGate()
			defer release.open()
			r := httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{Append: func(_ *wal.Store, records []wal.Record) error {
				if len(records) != 1 || records[0].Seq != 2 || records[0].Command.Place.ClientOrderID != "target" {
					return errors.New("unexpected fault batch")
				}
				if err := writeHTTPFaultOutcome(f, phase); err != nil {
					return fmt.Errorf("fault setup: %w", err)
				}
				entered.open()
				release.wait()
				return fmt.Errorf("injected %s: %w", phase, syscall.EIO)
			}})
			e := httpServe(t, r, nil)
			out := httpAsync(e.client, e.base, f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2))
			httpAwait(t, entered.ch)
			balance := httpDecode[api.BalancesView](t, httpStatus(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 200))
			if balance.AsOfSeq != 1 || balance.USDT.ReservedAtoms != 0 {
				t.Fatal("failed/unacknowledged storage reached engine")
			}
			httpStatus(t, e.call(f.run, "alice", "GET", "/v1/results/place/target", ""), 202)
			queued := httpAsync(e.client, e.base, f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2))
			httpEventually(t, func() bool { n, _ := rt.HTTPTestQueues(r); return n == 1 })
			release.open()
			httpFailure(t, httpAwait(t, out), 503, "STORAGE_UNAVAILABLE", rt.Unknown)
			httpFailure(t, httpAwait(t, queued), 503, "STORAGE_UNAVAILABLE", rt.Unknown)
			c, cancel := httpContext()
			err := r.Wait(c)
			cancel()
			if !errors.Is(err, syscall.EIO) || r.Ready() {
				t.Fatalf("storage failure was not terminal: %v", err)
			}
			httpStatus(t, e.call(f.run, "alice", "GET", "/health/ready", ""), 503)
			httpFailure(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("after-failure", "BUY", 50, 1)), 503, "NOT_READY", rt.NotAdmitted)
			e.close(t)
			seq := d.Int(1)
			if phase == "before-sync" || phase == "after-sync" {
				seq = 2 // Valid bytes remain in this running OS; this is not power loss.
			}
			assertHTTPRecovered(t, f, seq)
		})
	}
}

func TestNativeHTTPSnapshotFailureClosesReadinessAndRecovers(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprintf("published=%t", published), func(t *testing.T) {
			f := newHTTPFixture(t)
			entered, release := newHTTPGate(), newHTTPGate()
			defer release.open()
			r := httpOpen(t, f.dir, httpOptions(), rt.HTTPTestHooks{Publish: func(dir string, doc snapshot.Document) error {
				if published {
					if err := snapshot.Publish(dir, doc); err != nil {
						return err
					}
				}
				entered.open()
				release.wait()
				return fmt.Errorf("injected snapshot completion failure: %w", syscall.EIO)
			}})
			e := httpServe(t, r, nil)
			httpStatus(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2)), 200)
			out := make(chan error, 1)
			go func() { c, cancel := httpContext(); defer cancel(); _, err := r.Snapshot(c); out <- err }()
			httpAwait(t, entered.ch)
			if r.SnapshotSeq() != 1 {
				t.Fatal("snapshot completion was acknowledged before the publisher returned")
			}
			// Snapshot I/O does not block ordinary HTTP queries or duplicate results.
			got := httpStatus(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2)), 200)
			if !bytes.Equal(got, f.target) {
				t.Fatal("snapshot wait changed duplicate result")
			}
			httpStatus(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 200)
			release.open()
			if err := httpAwait(t, out); !errors.Is(err, syscall.EIO) {
				t.Fatalf("snapshot failure lost: %v", err)
			}
			httpAwait(t, r.Done())
			httpStatus(t, e.call(f.run, "alice", "GET", "/health/ready", ""), 503)
			httpFailure(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("new", "BUY", 50, 1)), 503, "NOT_READY", rt.NotAdmitted)
			e.close(t)
			snapshotSeq := d.Int(1)
			if published {
				snapshotSeq = 2
			}
			assertHTTPRecoveredFrom(t, f, 2, snapshotSeq)
		})
	}
}

func TestNativeHTTPApplyFailureStopsAndReplaysWholeCommand(t *testing.T) {
	for _, phase := range []string{"before-apply", "after-apply"} {
		t.Run(phase, func(t *testing.T) {
			f := newHTTPFixture(t)
			fail := func(wal.Record) { panic("injected apply boundary") }
			h := rt.HTTPTestHooks{BeforeApply: fail}
			if phase == "after-apply" {
				h = rt.HTTPTestHooks{AfterApply: fail}
			}
			r := httpOpen(t, f.dir, httpOptions(), h)
			e := httpServe(t, r, nil)
			httpFailure(t, e.call(f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2)), 503, "INTERNAL_FAILURE", rt.Unknown)
			httpAwait(t, r.Done())
			httpFailure(t, e.call(f.run, "alice", "GET", "/v1/balances", ""), 503, "NOT_READY", rt.NotAdmitted)
			e.close(t)
			assertHTTPRecovered(t, f, 2)
		})
	}
}
