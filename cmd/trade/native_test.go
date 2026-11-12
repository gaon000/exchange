//go:build darwin && arm64 && cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
)

func TestNativeTradingCLIAndRestart(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "exchange")
	build := exec.Command(filepath.Join(root, "scripts/go"), "build", "-o", binary, "./cmd/exchange")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	data := filepath.Join(dir, "run")
	init := exec.Command(binary, "init", "--config", filepath.Join(root, "config/examples/local.json"), "--data-dir", data)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := func(number int, listen string) (string, string, func()) {
		t.Helper()
		report := filepath.Join(dir, fmt.Sprintf("server-%d.json", number))
		out, err := os.Create(report)
		if err != nil {
			t.Fatal(err)
		}
		errout, err := os.Create(filepath.Join(dir, fmt.Sprintf("server-%d.stderr", number)))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "serve", "--data-dir", data, "--listen", listen)
		cmd.Stdout = out
		cmd.Stderr = errout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			t.Helper()
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("server shutdown timeout")
			}
			_ = out.Close()
			_ = errout.Close()
		}
		t.Cleanup(stop)
		for {
			var r struct{ Event, Listen string }
			b, _ := os.ReadFile(report)
			if json.Unmarshal(b, &r) == nil && r.Event == "ready" {
				return report, r.Listen, stop
			}
			select {
			case err := <-done:
				stopped = true
				_ = out.Close()
				_ = errout.Close()
				t.Fatal("server exited before ready", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	report, listen, stop := start(1, "127.0.0.1:0")
	call := func(account, command string, flags ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		args := append([]string{command, "--server-report", report, "--account", account}, flags...)
		if err := run(ctx, args, &out, &stderr); err != nil {
			t.Fatal(args, stderr.String(), err)
		}
		return bytes.TrimSpace(out.Bytes())
	}
	ask := filepath.Join(dir, "buy.json")
	cancelPath := filepath.Join(dir, "cancel.json")
	first := call("alice", "place", "--id", "manual-buy", "--side", "BUY", "--price", "60000", "--quantity", "0.01", "--request", ask)
	call("bob", "place", "--id", "manual-sell", "--side", "SELL", "--price", "59900", "--quantity", "0.004", "--request", filepath.Join(dir, "sell.json"))
	var order api.OrderView
	if err := json.Unmarshal(call("alice", "order", "--id", "manual-buy"), &order); err != nil || order.Order.Status != d.PartiallyFilled || order.Order.OpenQty != 60 {
		t.Fatal(order, err)
	}
	var book api.BookView
	if err := json.Unmarshal(call("alice", "book", "--limit", "1"), &book); err != nil || len(book.Bids) != 1 || book.Bids[0].QuantityLots != 60 {
		t.Fatal(book, err)
	}
	var trades api.TradesView
	if err := json.Unmarshal(call("alice", "trades", "--limit", "1"), &trades); err != nil || len(trades.Trades) != 1 || trades.Trades[0].PriceTicks != 6000000 || trades.NextCursor == nil {
		t.Fatal(trades, err)
	}
	page := call("alice", "trades", "--limit", "1", "--through-seq", trades.ThroughSeq.String(), "--after-seq", trades.NextCursor.AfterSeq.String(), "--after-index", trades.NextCursor.AfterIndex.String())
	var next api.TradesView
	if err := json.Unmarshal(page, &next); err != nil || len(next.Trades) != 0 || next.ThroughSeq != trades.ThroughSeq {
		t.Fatal(next, err)
	}
	var balance api.BalancesView
	if err := json.Unmarshal(call("alice", "balances"), &balance); err != nil || balance.USDT.ReservedAtoms != 360000000 {
		t.Fatal(balance, err)
	}
	firstCancel := call("alice", "cancel", "--id", "manual-cancel", "--order-id", "manual-buy", "--request", cancelPath)
	if err := json.Unmarshal(call("alice", "balances"), &balance); err != nil || balance.USDT.ReservedAtoms != 0 || balance.USDT.AvailableAtoms != 99760000000 {
		t.Fatal(balance, err)
	}
	// Completed business rejections remain HTTP 200 and are shown as results.
	var rejected d.Result
	if err := json.Unmarshal(call("alice", "place", "--id", "no-funds", "--side", "BUY", "--price", "1000000", "--quantity", "1", "--request", filepath.Join(dir, "reject.json")), &rejected); err != nil || rejected.Code != d.InsufficientBalance {
		t.Fatal(rejected, err)
	}
	stop()
	report, _, _ = start(2, listen)
	for path, want := range map[string][]byte{ask: first, cancelPath: firstCancel} {
		for _, command := range []string{"result", "retry"} {
			var out bytes.Buffer
			if err := run(ctx, []string{command, "--request", path}, &out, io.Discard); err != nil || !bytes.Equal(bytes.TrimSpace(out.Bytes()), want) {
				t.Fatal(command, out.String(), err)
			}
		}
	}
	if !bytes.Equal(call("alice", "result", "--kind", "place", "--id", "manual-buy"), first) {
		t.Fatal("initial result changed")
	}
	if err := json.Unmarshal(call("alice", "balances"), &balance); err != nil || balance.AsOfSeq != 4 {
		t.Fatal("retry advanced sequence", balance, err)
	}
	conflictArgs := []string{"place", "--server-report", report, "--account", "alice", "--id", "manual-buy", "--side", "BUY", "--price", "60001", "--quantity", "0.01", "--request", filepath.Join(dir, "conflict.json")}
	if err := run(ctx, conflictArgs, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(err)
	}
	// A completed ORDER_NOT_FOUND result has no order payload. GET alone cannot
	// establish that a cancellation file still names the original target.
	call("alice", "cancel", "--id", "missing-cancel", "--order-id", "missing-one", "--request", filepath.Join(dir, "missing.json"))
	conflictingCancel := filepath.Join(dir, "conflicting-cancel.json")
	args := []string{"cancel", "--server-report", report, "--account", "alice", "--id", "missing-cancel", "--order-id", "missing-two", "--request", conflictingCancel}
	if err := run(ctx, args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"retry", "--request", conflictingCancel}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal("completed GET hid conflicting cancel", err)
	}
}
