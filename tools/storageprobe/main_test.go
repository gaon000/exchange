//go:build darwin && cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProbePreservesParent(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep")
	if err := os.WriteFile(keep, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := probe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.PowerLoss != "not tested" || len(result.Checks) != 5 {
		t.Fatalf("unexpected evidence: %+v", result)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("parent changed: %v, %v", entries, err)
	}
	data, err := os.ReadFile(keep)
	if err != nil || string(data) != "user file" {
		t.Fatalf("user file changed: %q, %v", data, err)
	}
}

func TestFullSyncClosedFileFails(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fullSync(file); err == nil {
		t.Fatal("closed descriptor reported sync success")
	}
}

func TestLockAcrossProcesses(t *testing.T) {
	if path := os.Getenv("EXCHANGE_PROBE_CHILD_LOCK"); path != "" {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := tryLock(file); !lockContended(err) {
			t.Fatalf("child lock not rejected: %v", err)
		}
		return
	}
	file, err := os.CreateTemp(t.TempDir(), "lock-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := tryLock(file); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockAcrossProcesses$")
	cmd.Env = append(os.Environ(), "EXCHANGE_PROBE_CHILD_LOCK="+file.Name())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child process: %v\n%s", err, output)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := os.OpenFile(file.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := tryLock(second); err != nil {
		t.Fatalf("lock did not release: %v", err)
	}
}
