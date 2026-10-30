package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "demo-test-child" {
		switch os.Args[2] {
		case "fail":
			fmt.Fprintln(os.Stderr, "expected startup failure")
			os.Exit(3)
		case "wait":
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGTERM)
			fmt.Println(`{"event":"ready","runId":"11111111-1111-4111-8111-111111111111","listen":"127.0.0.1:12345"}`)
			<-ch
			os.Exit(0)
		case "ignore":
			signal.Ignore(syscall.SIGTERM)
			fmt.Println(`{"event":"ready","runId":"11111111-1111-4111-8111-111111111111","listen":"127.0.0.1:12345"}`)
			for {
				time.Sleep(time.Second)
			}
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestOptionsAndPrecancelledRun(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--timeout", "0"}, {"--timeout", "11m"}, {"--exchange", ""}, {"--bad"}} {
		if _, err := parse(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	if err := run(context.Background(), []string{"-h"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(t.TempDir(), "output")
	if err := run(ctx, []string{"--output", output}, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled run created output", err)
	}
}

func TestHTTPExpectRejectsWrongValuesAndTransportFailures(t *testing.T) {
	want := balances("test", 3, "alice")
	for _, scenario := range []string{"ok", "wrong-balance", "missing-field", "business-rejection", "http-error", "redirect", "too-large", "truncated", "trace-error"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/balances" || r.Header.Get("X-Run-ID") != "test" || r.Header.Get("X-Account-ID") != "alice" {
					t.Error("request context changed")
				}
				value := want
				switch scenario {
				case "wrong-balance":
					value.USDT.AvailableAtoms++
				case "missing-field":
					fmt.Fprint(w, `{"runId":"test"}`)
					return
				case "business-rejection":
					fmt.Fprint(w, `{"code":"INSUFFICIENT_BALANCE"}`)
					return
				case "http-error":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "redirect":
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				case "too-large":
					fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
					return
				case "truncated":
					w.Header().Set("Content-Length", "10000")
					fmt.Fprint(w, "{")
					return
				}
				_ = json.NewEncoder(w).Encode(value)
			}))
			defer server.Close()
			var trace bytes.Buffer
			var sink io.Writer = &trace
			if scenario == "trace-error" {
				sink = badWriter{}
			}
			c := newHTTPClient(server.URL, "test", sink)
			_, err := expect(context.Background(), c, "alice", http.MethodGet, "/v1/balances", nil, want)
			if (scenario == "ok") != (err == nil) {
				t.Fatalf("unexpected result: %v", err)
			}
			if scenario != "trace-error" && trace.Len() == 0 {
				t.Fatal("HTTP evidence was lost")
			}
		})
	}
}

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("output failed") }

func TestFirstResultByteMismatchIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(result("test", 1))
	}))
	defer server.Close()
	c := newHTTPClient(server.URL, "test", io.Discard)
	if err := checkResults(context.Background(), c, [][]byte{[]byte("different first result")}, false); err == nil || !strings.Contains(err.Error(), "bytes changed") {
		t.Fatal(err)
	}
}

func TestChildStartupFailureGracefulStopAndForcedCleanup(t *testing.T) {
	for _, mode := range []string{"fail", "wait", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			readyPath := filepath.Join(dir, "ready.json")
			p, err := startChild([]string{os.Args[0], "demo-test-child", mode}, readyPath, filepath.Join(dir, "stderr"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.stop(100 * time.Millisecond) })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := awaitReady(ctx, p, readyPath)
			if mode == "fail" {
				if err == nil {
					t.Fatal("failed child reported ready")
				}
				return
			}
			if err != nil || !d.ValidRunID(r.RunID) {
				t.Fatal(r, err)
			}
			waitCtx, stopWait := context.WithCancel(context.Background())
			stopWait()
			if err := p.wait(waitCtx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			timeout := 100 * time.Millisecond
			if mode == "wait" {
				timeout = 3 * time.Second // race-instrumented helper sleeps before process exit
			}
			err = p.stop(timeout)
			if (mode == "wait") != (err == nil) {
				t.Fatal("unexpected stop result", err)
			}
			select {
			case <-p.done:
			default:
				t.Fatal("child not joined")
			}
		})
	}
}
