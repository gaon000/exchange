package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/wal"
)

// Re-exec the actual CLI main without running the test runner in child mode.
// This environment switch exists only in the test binary, never in exchange.
func TestMain(m *testing.M) {
	if os.Getenv("EXCHANGE_TEST_CLI_CHILD") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func configFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	config := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 100, QuoteAtoms: 100000}, {AccountID: "bob", BaseLots: 100, QuoteAtoms: 100000}})
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommandUsageAndOptionValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"init"}, {"init", "--config", "missing"}, {"init", "--config", "missing", "--data-dir", "unused", "extra"}, {"serve"}, {"serve", "--data-dir", "unused", "extra"}, {"serve", "--data-dir", "unused", "--config", "ignored"}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"init", "-h"}, {"serve", "--help"}} {
		var output bytes.Buffer
		if err := run(context.Background(), args, &output, &output); err != nil || output.Len() == 0 {
			t.Fatalf("help %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"--command-queue", "0"}, {"--query-queue", "0"}, {"--request-limit", "0"}, {"--request-limit", "1"}, {"--pending-waiters", "0"},
		{"--batch-commands", "257"}, {"--batch-bytes", "4096"}, {"--batch-delay", "0"}, {"--batch-delay", "not-time"},
		{"--read-header-timeout", "0"}, {"--read-body-timeout", "-1s"}, {"--result-timeout", "2h"}, {"--write-timeout", "0"}, {"--http-shutdown-timeout", "0"}, {"--http-shutdown-timeout", "2h"},
		{"--metrics-interval", "0"}, {"--metrics-interval", "99ms"}, {"--metrics-interval", "61s"},
		{"--snapshot-interval", "99ms", "--snapshot-log", "unused"}, {"--snapshot-interval", "2h", "--snapshot-log", "unused"},
		{"--snapshot-interval", "1s"}, {"--snapshot-log", "unused"}, {"--snapshot-interval", "-1s", "--snapshot-log", "unused"},
	} {
		args = append([]string{"--data-dir", "unused"}, args...)
		if _, err := parseServe(args, io.Discard); err == nil {
			t.Fatal("bad limits accepted", args)
		}
	}
	o, err := parseServe([]string{"--data-dir", "unused"}, io.Discard)
	if err != nil || o.runtime != rt.DefaultOptions() || o.http.RequestLimit != o.runtime.RequestLimit || o.address != "127.0.0.1:8080" || o.httpShutdownTimeout != 15*time.Second {
		t.Fatal(o, err)
	}
	o, err = parseServe([]string{"--data-dir", "unused", "--request-limit", "3", "--pending-waiters", "2", "--batch-delay", "7ms"}, io.Discard)
	if err != nil || o.http.RequestLimit != 3 || o.runtime.RequestLimit != 3 || o.runtime.BatchDelay != 7*time.Millisecond {
		t.Fatal(o, err)
	}
	if _, err := parseServe([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
}

func TestInvalidConfigDoesNotCreateRun(t *testing.T) {
	validPath := configFixture(t)
	valid, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"syntax": []byte("{"), "unknown": bytes.Replace(valid, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":1,"extra":true`), 1),
		"duplicate":   bytes.Replace(valid, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":1,"schemaVersion":1`), 1),
		"bad version": bytes.Replace(valid, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":2`), 1),
		"too large":   append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), int(wal.MaxGenesisPayload)+1-len(valid))...),
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, "config.json")
			dir := filepath.Join(parent, "run")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background(), []string{"init", "--config", path, "--data-dir", dir}, io.Discard, io.Discard); err == nil {
				t.Fatal("invalid config initialized")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid config changed data directory", err)
			}
		})
	}
	if _, err := readConfig(t.TempDir()); err == nil {
		t.Fatal("config directory accepted")
	}
	if _, err := readConfig(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing config accepted")
	}
	// The byte cap is inclusive and does not depend on file metadata alone.
	padded := append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), int(wal.MaxGenesisPayload)-len(valid))...)
	if err := os.WriteFile(validPath, padded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(validPath); err != nil {
		t.Fatal("exact config limit", err)
	}
	link := filepath.Join(t.TempDir(), "linked.json")
	if err := os.Symlink(validPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(link); err != nil {
		t.Fatal("regular config symlink", err)
	}
}

func TestCancelledCommandsDoNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "run")
	for _, args := range [][]string{{"init", "--config", configFixture(t), "--data-dir", dir}, {"serve", "--data-dir", dir}} {
		if err := run(ctx, args, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled command created run")
	}
}

func TestServeRejectsInvalidOptionsBeforeDataOrSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	err := run(context.Background(), []string{"serve", "--data-dir", dir, "--listen", "0.0.0.0:1", "--result-timeout", "0"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP options") {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("option validation changed data")
	}
}
