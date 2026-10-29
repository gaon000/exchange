//go:build darwin && cgo

package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type wrappedDiskSnapshots struct {
	diskCheckpoints
	publish func(snapshot.Document) error
}

func (f wrappedDiskSnapshots) Publish(doc snapshot.Document) error { return f.publish(doc) }

func cleanupRuntime(t *testing.T, r *Runtime) {
	t.Helper()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(c)
	})
}

func TestNativeSnapshotPublicationRetainsRunLockThroughShutdown(t *testing.T) {
	f := nativeRecovery(t)
	s, err := wal.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	files := wrappedDiskSnapshots{diskCheckpoints{f.dir}, func(doc snapshot.Document) error {
		close(started)
		<-release
		return snapshot.Publish(f.dir, doc)
	}}
	r, err := startWithSnapshots(s, files, DefaultOptions(), hooks{})
	if err != nil {
		t.Fatal(err)
	}
	cleanupRuntime(t, r)
	if r.SnapshotSeq() != 11 {
		t.Fatal("recovered snapshot sequence not initialized")
	}
	request := snapshotAsync(r, ctx(t))
	receive(t, started)
	c := cancel("during-publication", "future")
	c.RunID = f.genesis.RunID
	want, err := f.want.Apply(12, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Submit(ctx(t), c)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("WAL and snapshot workers did not progress independently: %v", err)
	}
	if view := read(t, r, Query{Kind: BalancesQuery}); view.AsOfSeq != 12 {
		t.Fatal("query stalled on snapshot")
	}
	stop, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	if err := r.Shutdown(stop); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if another, err := Open(f.dir, DefaultOptions()); err == nil || !durable.Contended(err) {
		if another != nil {
			finish(t, another)
		}
		t.Fatalf("publication lost lifetime lock: %v", err)
	}
	once.Do(func() { close(release) })
	if result := receive(t, request); result.err != nil || result.result.SnapshotSeq != 11 {
		t.Fatal(result)
	}
	finish(t, r)
	r, err = Open(f.dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	cleanupRuntime(t, r)
	if r.Recovery().SnapshotSeq != 11 || r.Recovery().ReplayedCommands != 1 {
		t.Fatal("wrong prefix/suffix recovered")
	}
	if result, err := r.Snapshot(ctx(t)); err != nil || result.SnapshotSeq != 12 || r.SnapshotSeq() != 12 {
		t.Fatalf("real public API publication: %+v %v", result, err)
	}
	finish(t, r)
	if !reflect.DeepEqual(r.engine.Capture(), f.want.Capture()) {
		t.Fatal("native snapshot changed state or first responses")
	}
	r, err = Open(f.dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	cleanupRuntime(t, r)
	if r.Recovery().SnapshotSeq != 12 || r.Recovery().ReplayedCommands != 0 {
		t.Fatal("new publication was not selected after restart")
	}
	finish(t, r)
	if !reflect.DeepEqual(r.engine.Capture(), f.want.Capture()) {
		t.Fatal("restart lost captured state")
	}
}

// These are process exits at the capture boundaries and after actual publication,
// not power-loss tests or claims about an observed instruction inside Capture.
func TestNativeSnapshotCaptureAndPublicationProcessKill(t *testing.T) {
	if dir := os.Getenv("EXCHANGE_SNAPSHOT_RUNTIME_CHILD_DIR"); dir != "" {
		s, err := wal.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		phase := os.Getenv("EXCHANGE_SNAPSHOT_RUNTIME_CHILD_PHASE")
		pause := func() { fmt.Fprintln(os.Stdout, "SNAPSHOT_BOUNDARY"); select {} }
		h := hooks{}
		if phase == "before-copy" {
			h.beforeCapture = pause
		} else if phase == "after-copy" {
			h.afterCapture = func(snapshot.Document) { pause() }
		}
		files := wrappedDiskSnapshots{diskCheckpoints{dir}, func(doc snapshot.Document) error {
			if err := snapshot.Publish(dir, doc); err != nil {
				return err
			}
			if phase == "published-before-owner-ack" {
				pause()
			}
			return nil
		}}
		r, err := startWithSnapshots(s, files, DefaultOptions(), h)
		if err != nil {
			t.Fatal(err)
		}
		c := cancel("before-kill", "future")
		c.RunID = r.RunID()
		first, err := r.Submit(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, string(b))
		_, err = r.Snapshot(context.Background())
		t.Fatalf("child passed kill boundary: %v", err)
	}
	for _, phase := range []string{"before-copy", "after-copy", "published-before-owner-ack"} {
		t.Run(phase, func(t *testing.T) {
			f := nativeRecovery(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeSnapshotCaptureAndPublicationProcessKill$")
			cmd.Env = append(os.Environ(), "EXCHANGE_SNAPSHOT_RUNTIME_CHILD_DIR="+f.dir, "EXCHANGE_SNAPSHOT_RUNTIME_CHILD_PHASE="+phase)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			reaped := false
			t.Cleanup(func() {
				if !reaped {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			lines := make(chan string, 8)
			go func() {
				defer close(lines)
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
			}()
			var first d.Result
			if err := json.Unmarshal([]byte(receive(t, lines)), &first); err != nil {
				t.Fatal(err)
			}
			if first.CommandSeq != 12 || receive(t, lines) != "SNAPSHOT_BOUNDARY" {
				t.Fatal("child did not reach expected snapshot boundary")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("child exited without forced kill")
			}
			reaped = true
			c := cancel("before-kill", "future")
			c.RunID = f.genesis.RunID
			want, err := f.want.Apply(12, c)
			if err != nil || !reflect.DeepEqual(want, first) {
				t.Fatalf("child result differs from expected command: %v", err)
			}
			selected := d.Int(11)
			if phase == "published-before-owner-ack" {
				selected = 12
			}
			r, err := Open(f.dir, DefaultOptions())
			if err != nil {
				t.Fatalf("restart: %v\n%s", err, stderr.String())
			}
			cleanupRuntime(t, r)
			if r.SnapshotSeq() != selected || r.Recovery().ReplayedCommands != 12-selected {
				t.Fatalf("wrong recovered boundary: %+v", r.Recovery())
			}
			commands := append(recoveryCommands(f.genesis.RunID), c)
			for _, command := range commands {
				want, _, _ := f.want.Lookup(command)
				got, err := r.Submit(ctx(t), command)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("first response changed after kill: %v", err)
				}
			}
			finish(t, r)
			if !reflect.DeepEqual(r.engine.Capture(), f.want.Capture()) {
				t.Fatal("snapshot + WAL state differs after process kill")
			}
		})
	}
}
