//go:build darwin && cgo

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNativeCLIMetricsFinalIncludesDurableWork(t *testing.T) {
	dir, _ := initProcess(t)
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	p := startProcess(t, "serve", "--data-dir", dir, "--listen", "127.0.0.1:0", "--metrics-file", path, "--metrics-interval", "100ms")
	report := p.ready(t)
	if !report.Metrics.Enabled || !report.Metrics.Instrumentation || report.Metrics.File != path || report.Metrics.Interval != "100ms" {
		t.Fatal(report.Metrics)
	}
	client := clientFor(t)
	for i := 0; i < 2; i++ {
		v := send(client, report, "alice", "POST", "/v1/orders", orderJSON)
		if v.err != nil || v.status != 200 {
			t.Fatal(v)
		}
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(t); err != nil {
		t.Fatal(err, p.stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var samples []metricsRecord
	for scanner.Scan() {
		var sample metricsRecord
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(samples) < 2 || samples[0].Stage != "initial" || samples[len(samples)-1].Stage != "final" {
		t.Fatal("missing initial/final")
	}
	last := samples[len(samples)-1]
	if last.Runtime.Applied != 1 || last.Runtime.CachedRetries != 1 || last.Runtime.Enqueued != 2 || last.Runtime.BatchCommands != 1 || last.Runtime.Apply.Count != 1 || last.Runtime.CommandQueueWait.Count != 2 || last.Runtime.BatchWait.Count != 1 || last.Runtime.Append.Count != 1 || last.Runtime.AwaitingCalls != 0 || last.Runtime.WAL == nil {
		t.Fatalf("runtime: %+v", last.Runtime)
	}
	w := last.Runtime.WAL
	if w.Groups != 1 || w.Commands != 1 || w.Write.Count != 1 || w.Sync.Count != 1 || w.IOErrors != 0 || last.WALBytes-samples[0].WALBytes != int64(w.EncodedBytes) {
		t.Fatalf("WAL: %+v", w)
	}
	if last.HTTP.Handler.Count != 2 || last.HTTP.Status2xx != 2 || last.HTTP.Encode.Count != 2 || last.HTTP.Write.Count != 2 || last.HTTP.WriteErrors != 0 || last.HTTP.Active != 0 {
		t.Fatalf("HTTP: %+v", last.HTTP)
	}
	if !last.Process.OSAvailable || len(last.Process.RuntimeUnavailable) != 0 || last.Process.PeakRSSBytes == 0 || last.FinishedUTC.Before(last.StartedUTC) {
		t.Fatalf("resources: %+v", last.Process)
	}
	// Opening the same metrics path must fail before recovering or changing WAL.
	before, err := os.ReadFile(filepath.Join(dir, "commands.wal"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := childCommand("serve", "--data-dir", dir, "--listen", "127.0.0.1:0", "--metrics-file", path).CombinedOutput()
	if err == nil {
		t.Fatal("existing metrics file reused", string(output))
	}
	after, _ := os.ReadFile(filepath.Join(dir, "commands.wal"))
	metrics, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || !bytes.Equal(data, metrics) {
		t.Fatal("existing data changed on output collision")
	}
}
