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
	"reflect"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/wal"
)

func TestNativeExpandedTradingRecovery(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "exchange")
	build := exec.Command(filepath.Join(root, "scripts/go"), "build", "-o", binary, "./cmd/exchange")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	for _, scenario := range []string{"all", "snapshot", "crash", "cancel", "output-error"} {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "demo")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stdout bytes.Buffer
			var sink io.Writer = &stdout
			args := []string{"--exchange", binary, "--output", dir}
			success := scenario != "cancel" && scenario != "output-error"
			if scenario == "snapshot" || scenario == "crash" {
				args = append(args, "--scenario", scenario)
			}
			if !success {
				sink = &tailInterruptWriter{cancel: cancel, fail: scenario == "output-error"}
			}
			err := run(ctx, args, sink, io.Discard)
			if success != (err == nil) {
				t.Fatal(stdout.String(), err)
			}
			before, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			var s summary
			if err := json.Unmarshal(before, &s); err != nil {
				t.Fatal(err)
			}
			if (s.Status == "PASSED") != success || s.PowerLossTested || s.RunID == "" {
				t.Fatal(s)
			}
			store, err := wal.Open(filepath.Join(dir, "run"))
			if err != nil {
				t.Fatal("run still locked", err)
			}
			last := store.Recovery().LastSeq
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			want := d.Int(8)
			if !success {
				want = 7
			}
			if last != want {
				t.Fatal("wrong durable seq", last)
			}
			paths, err := filepath.Glob(filepath.Join(dir, "server-*.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				r, err := readReport(path)
				if err != nil {
					t.Fatal(err)
				}
				conn, err := net.DialTimeout("tcp", r.Listen, time.Second)
				if err == nil {
					conn.Close()
					t.Fatal("server still listening", path)
				}
			}
			if !success {
				if s.ProcessKillTested {
					t.Fatal("cancelled demo performed scenario kill")
				}
				return
			}
			expected := []recoveryCheck{{2, 0, 6, true}, {3, 6, 0, true}, {4, 6, 1, true}}
			if scenario == "snapshot" {
				expected = []recoveryCheck{{2, 6, 0, true}, {3, 6, 1, true}}
			}
			if scenario == "crash" {
				expected = []recoveryCheck{{2, 0, 7, true}}
			}
			if !reflect.DeepEqual(s.Recoveries, expected) || s.ProcessKillTested != (scenario != "snapshot") {
				t.Fatal(s)
			}
			assertExtendedReadsBeforePost(t, filepath.Join(dir, "http.jsonl"), expected)
			if err := run(context.Background(), args, io.Discard, io.Discard); !errors.Is(err, os.ErrExist) {
				t.Fatal("output reused", err)
			}
			after, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("old evidence changed", err)
			}
		})
	}
}

type tailInterruptWriter struct {
	cancel context.CancelFunc
	fail   bool
}

func (w *tailInterruptWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("WAL tail:")) {
		if w.fail {
			return 0, errors.New("expected stdout failure")
		}
		w.cancel()
	}
	return len(p), nil
}

func assertExtendedReadsBeforePost(t *testing.T, path string, recoveries []recoveryCheck) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	server := 0
	seen := map[string]bool{}
	checked := map[int]bool{}
	validate := func() {
		if server < 2 {
			return
		}
		n := int(recoveries[server-2].SnapshotSeq + recoveries[server-2].ReplayedCommands)
		for _, cmd := range extendedCommands()[:n] {
			if !seen[cmd.lookup] {
				t.Fatalf("server %d POST before result GET: %s", server, cmd.lookup)
			}
		}
		for _, key := range []string{"alice /v1/balances", "bob /v1/balances", "alice /v1/book", "alice /v1/trades"} {
			if !seen[key] {
				t.Fatal("POST before state GET", server, key)
			}
		}
		checked[server] = true
	}
	for {
		var r exchangeRecord
		if err := decoder.Decode(&r); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if r.Path == "/health/ready" {
			if server >= 2 && !checked[server] {
				validate()
			}
			server++
			seen = map[string]bool{}
		}
		if r.Method == "GET" {
			seen[r.Account+" "+r.Path] = true
			if strings.HasPrefix(r.Path, "/v1/results/") {
				seen[r.Path] = true
			}
		}
		if r.Method == "POST" && server >= 2 && !checked[server] {
			validate()
		}
	}
	if server != len(recoveries)+1 {
		t.Fatal("missing restarts", server)
	}
	validate()
}
