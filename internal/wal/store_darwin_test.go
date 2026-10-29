//go:build darwin && cgo

package wal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gaon000/exchange/internal/durable"
)

func TestNativeStore(t *testing.T) {
	if dir := os.Getenv("EXCHANGE_WAL_CHILD_DIR"); dir != "" {
		s, err := Open(dir)
		if os.Getenv("EXCHANGE_WAL_CHILD_MODE") == "contended" {
			if s != nil {
				_ = s.Close()
			}
			if !durable.Contended(err) {
				t.Fatalf("second process took owned directory: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Replay(nil); err != nil {
			t.Fatal(err)
		}
		if err := s.ConfirmRecovery(); err != nil || s.DurableSeq() != 2 {
			t.Fatalf("recovery: %v seq %d", err, s.DurableSeq())
		}
		return
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "run")
	s, err := Init(dir, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := s.Genesis()
	a := bind(place(1, "alice", "a"), g)
	b := bind(place(2, "bob", "b"), g)
	lockBefore, err := os.Stat(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	child := func(mode string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeStore$")
		cmd.Env = append(os.Environ(), "EXCHANGE_WAL_CHILD_DIR="+dir, "EXCHANGE_WAL_CHILD_MODE="+mode)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
	}
	child("contended")
	if err := s.Append([]Record{a, b}); err != nil || s.DurableSeq() != 2 {
		t.Fatalf("append %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Actual truncated file; reopening must leave the bytes alone until the
	// retained contents and metadata are strictly synced by ConfirmRecovery.
	appendFile(t, filepath.Join(dir, "commands.wal"), []byte("EXGRP00"))
	child("recover")
	lockAfter, err := os.Stat(filepath.Join(dir, ".lock"))
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("lock inode replaced: %v", err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Recovery().TornTail || s.Recovery().LastSeq != 2 || s.DurableSeq() != 0 {
		t.Fatalf("%+v", s.Recovery())
	}
	if err := s.ConfirmRecovery(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append([]Record{bind(place(3, "alice", "next"), g)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir, testConfig()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing run init: %v", err)
	}
}

func TestNativeInitializationFailureReleasesLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	ops := nativeOps()
	realSync := ops.sync
	ops.sync = func(f *os.File) error {
		if filepath.Base(f.Name()) == "commands.wal" {
			return injected
		}
		return realSync(f)
	}
	if s, err := initStore(dir, testConfig(), ops); s != nil || !errors.Is(err, injected) {
		t.Fatalf("%v", err)
	}
	// Full initial bytes survived, but initialization did not succeed. A new
	// process owner can validate and strictly reconfirm them, without reseeding.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ConfirmRecovery(); err != nil {
		t.Fatal(err)
	}
	if s.DurableSeq() != 0 {
		t.Fatal("empty WAL has commands")
	}
}
