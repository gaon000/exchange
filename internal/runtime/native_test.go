//go:build darwin && cgo

package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	"github.com/gaon000/exchange/internal/wal"
)

func nativeCommand(run, id string) d.Command { c := buy(id); c.RunID = run; return c }

// This is a process-kill test with the OS still running. It does not simulate
// loss of OS/drive caches or claim the separate power-loss acceptance criterion.
func TestNativeDurableReplayAfterProcessKill(t *testing.T) {
	if dir := os.Getenv("EXCHANGE_RUNTIME_CHILD_DIR"); dir != "" {
		s, err := wal.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		o := DefaultOptions()
		o.BatchCommands = 1
		r, err := start(s, o, hooks{beforeApply: func(record wal.Record) {
			if record.Seq == 2 {
				fmt.Fprintln(os.Stdout, "DURABLE_UNPUBLISHED")
				select {}
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		first, err := r.Submit(context.Background(), nativeCommand(r.RunID(), "acknowledged"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		_, _ = r.Submit(context.Background(), nativeCommand(r.RunID(), "unpublished"))
		t.Fatal("child was not killed at the apply boundary")
	}
	dir := filepath.Join(t.TempDir(), "run")
	s, err := wal.Init(dir, d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 10, QuoteAtoms: 10000}, {AccountID: "bob", BaseLots: 10, QuoteAtoms: 10000}}))
	if err != nil {
		t.Fatal(err)
	}
	g := s.Genesis()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeDurableReplayAfterProcessKill$")
	cmd.Env = append(os.Environ(), "EXCHANGE_RUNTIME_CHILD_DIR="+dir)
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
	if first.CommandSeq != 1 || first.Order.Status != d.Open {
		t.Fatalf("invalid pre-kill response: %+v", first)
	}
	if line := receive(t, lines); line != "DURABLE_UNPUBLISHED" {
		t.Fatalf("unexpected child boundary %q", line)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child exited without the forced kill")
	}
	reaped = true
	r, err := Open(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, stderr.String())
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(c)
	})
	if another, err := Open(dir, DefaultOptions()); err == nil || !durable.Contended(err) {
		if another != nil {
			_ = another.Shutdown(ctx(t))
		}
		t.Fatalf("second owner: %v", err)
	}
	v := read(t, r, Query{Kind: BalancesQuery})
	if v.AsOfSeq != 2 || v.Balances.Quote.Reserved != 400 || v.Balances.Quote.Available != 9600 {
		t.Fatalf("lost or reseeded state: %+v", v)
	}
	got, err := r.Submit(ctx(t), nativeCommand(g.RunID, "acknowledged"))
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("acknowledged result changed: %+v %v", got, err)
	}
	got, err = r.Submit(ctx(t), nativeCommand(g.RunID, "unpublished"))
	if err != nil || got.CommandSeq != 2 || got.Order.Status != d.Open {
		t.Fatalf("unpublished command not replayed: %+v %v", got, err)
	}
	c := cancel("cancel", "acknowledged")
	c.RunID = g.RunID
	if result, err := r.Submit(ctx(t), c); err != nil || result.CommandSeq != 3 || result.Code != d.CancelSucceeded {
		t.Fatalf("post-recovery cancel: %+v %v", result, err)
	}
	finish(t, r)
	r, err = Open(dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	v = read(t, r, Query{Kind: BalancesQuery})
	if v.AsOfSeq != 3 || v.Balances.Quote.Reserved != 200 || v.Balances.Quote.Available != 9800 {
		t.Fatal(v)
	}
	got, err = r.Submit(ctx(t), nativeCommand(g.RunID, "acknowledged"))
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("cancel replaced initial result")
	}
	finish(t, r)
}
