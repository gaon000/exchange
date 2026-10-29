//go:build darwin && cgo

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNativeCLIExperimentControlsAndSnapshotRecovery(t *testing.T) {
	dir, _ := initProcess(t)
	outputs := t.TempDir()
	logPath, profileDir := filepath.Join(outputs, "snapshots.jsonl"), filepath.Join(outputs, "profiles")
	p := startProcess(t, "serve", "--data-dir", dir, "--listen", "127.0.0.1:0", "--snapshot-interval", "100ms", "--snapshot-log", logPath, "--profile-dir", profileDir)
	report := p.ready(t)
	if report.Experiments.SnapshotInterval != "100ms" || report.Experiments.SnapshotLog != logPath || report.Experiments.ProfileDir != profileDir || report.Experiments.MemProfileRate <= 0 {
		t.Fatal(report.Experiments)
	}
	client := clientFor(t)
	first := send(client, report, "alice", "POST", "/v1/orders", orderJSON)
	if first.err != nil || first.status != 200 {
		t.Fatal(first)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			var event snapshotEvent
			if json.Unmarshal(line, &event) == nil && event.Outcome == "COMPLETED" && event.SnapshotSeq == 1 && event.RunID == report.RunID {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot was not published", string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(t); err != nil {
		t.Fatal(err, p.stderr.String())
	}
	for _, name := range []string{"cpu.pprof", "heap.pprof", "allocs.pprof"} {
		data, err := os.ReadFile(filepath.Join(profileDir, name))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(name, err)
		}
		payload, err := io.ReadAll(reader)
		if err != nil || len(payload) == 0 {
			t.Fatal(name, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	metadata, err := os.ReadFile(filepath.Join(profileDir, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		StartedUTC, CPUStoppedUTC, FinishedUTC time.Time
		MemProfileRate                         int
		PostDrainGC                            bool
	}
	if err := json.Unmarshal(metadata, &info); err != nil || info.StartedUTC.IsZero() || info.CPUStoppedUTC.Before(info.StartedUTC) || info.FinishedUTC.Before(info.CPUStoppedUTC) || !info.PostDrainGC {
		t.Fatal(string(metadata), err)
	}
	// Both output collisions fail before recovery can trim an unfinished tail.
	walPath := filepath.Join(dir, "commands.wal")
	f, err := os.OpenFile(walPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--snapshot-interval", "1s", "--snapshot-log", logPath}, {"--profile-dir", profileDir}} {
		args := append([]string{"serve", "--data-dir", dir, "--listen", "127.0.0.1:0"}, extra...)
		if output, err := childCommand(args...).CombinedOutput(); err == nil {
			t.Fatal("collision accepted", string(output))
		}
		after, err := os.ReadFile(walPath)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("collision changed WAL", err)
		}
	}
	restarted := startProcess(t, "serve", "--data-dir", dir, "--listen", "127.0.0.1:0")
	r := restarted.ready(t)
	if r.Recovery.SnapshotSeq != 1 || r.Recovery.ReplayedCommands != 0 || r.Experiments.SnapshotInterval != "0s" || r.Experiments.ProfileDir != "" {
		t.Fatal(r)
	}
	retry := send(client, r, "alice", "POST", "/v1/orders", orderJSON)
	if retry.err != nil || retry.status != 200 || !bytes.Equal(retry.body, first.body) {
		t.Fatal(retry)
	}
	if err := restarted.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := restarted.wait(t); err != nil {
		t.Fatal(err)
	}
}
