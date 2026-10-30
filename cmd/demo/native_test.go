//go:build darwin && arm64 && cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/wal"
)

func TestNativeTradingDemoAndCleanup(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "exchange")
	cmd := exec.Command(filepath.Join(root, "scripts/go"), "build", "-o", binary, "./cmd/exchange")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	for _, mode := range []string{"success", "cancel", "output-error"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "demo")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stdout bytes.Buffer
			var output io.Writer = &stdout
			if mode != "success" {
				output = &interruptWriter{mode: mode, cancel: cancel}
			}
			args := []string{"--exchange", binary, "--output", dir}
			err := run(ctx, args, output, io.Discard)
			if (mode == "success") != (err == nil) {
				t.Fatal(stdout.String(), err)
			}
			data, readErr := os.ReadFile(filepath.Join(dir, "summary.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var s summary
			if err := json.Unmarshal(data, &s); err != nil {
				t.Fatal(err)
			}
			if s.PowerLossTested || s.RunID == "" || (s.Status == "PASSED") != (mode == "success") {
				t.Fatal(s)
			}
			// A second owner can acquire the same run only after all demo children stop.
			store, err := wal.Open(filepath.Join(dir, "run"))
			if err != nil {
				t.Fatal("demo left run locked", err)
			}
			seq := store.Recovery().LastSeq
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if mode == "success" && seq != 3 || mode != "success" && seq != 2 {
				t.Fatal("unexpected durable sequence", seq)
			}
			for _, name := range []string{"server-1.json", "server-2.json"} {
				r, err := readReport(filepath.Join(dir, name))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				conn, err := net.DialTimeout("tcp", r.Listen, time.Second)
				if err == nil {
					conn.Close()
					t.Fatal("demo server still listening", name)
				}
			}
			if mode != "success" {
				return
			}
			if len(s.Checks) != 7 || !strings.Contains(stdout.String(), "PASS:") {
				t.Fatal(s, stdout.String())
			}
			assertRecoveryReadBeforeRetry(t, filepath.Join(dir, "http.jsonl"))
			if err := run(context.Background(), args, io.Discard, io.Discard); !errors.Is(err, os.ErrExist) {
				t.Fatal("existing output was reused", err)
			}
			after, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("existing evidence changed", err)
			}
		})
	}
}

type interruptWriter struct {
	mode   string
	cancel context.CancelFunc
}

func (w *interruptWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("부분 체결:")) {
		if w.mode == "cancel" {
			w.cancel()
		} else {
			return 0, errors.New("expected stdout failure")
		}
	}
	return len(p), nil
}

func assertRecoveryReadBeforeRetry(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	starts, restoredResults, retries := 0, 0, 0
	for {
		var record exchangeRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if record.Path == "/health/ready" {
			starts++
		}
		if starts == 2 && strings.HasPrefix(record.Path, "/v1/results/") {
			restoredResults++
		}
		if starts == 2 && record.Method == "POST" {
			if restoredResults != 3 {
				t.Fatal("retry could hide missing recovery results")
			}
			retries++
		}
	}
	if starts != 2 || restoredResults != 3 || retries != 3 {
		t.Fatalf("starts=%d restored=%d retries=%d", starts, restoredResults, retries)
	}
}
