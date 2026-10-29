package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gaon000/exchange/internal/buildinfo"
)

func TestCLIRejectsInvalidInputsBeforeCreatingOutput(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{nil, {"--output", filepath.Join(dir, "out")}, {"--bad"}, {"--config", "missing", "--workload", "missing", "--server-report", "missing", "--output", filepath.Join(dir, "out")}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created output before validation")
	}
	if err := run(context.Background(), []string{"-h"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"--config", "x", "--workload", "y", "--server-report", "z", "--output", "out"}, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestArtifactFailurePreservesPartialFileAndHashInventory(t *testing.T) {
	a := artifacts{dir: t.TempDir(), hashes: map[string]string{}}
	want := errors.New("output interrupted")
	err := a.write("samples.jsonl", func(w io.Writer) error { _, _ = io.WriteString(w, "partial"); return want })
	if !errors.Is(err, want) || len(a.hashes) != 0 {
		t.Fatalf("failure lost: %v %+v", err, a.hashes)
	}
	b, _ := os.ReadFile(filepath.Join(a.dir, "samples.jsonl"))
	if string(b) != "partial" {
		t.Fatal("partial evidence lost")
	}
	if err := a.json("samples.jsonl", map[string]int{"x": 1}); err == nil {
		t.Fatal("overwrote partial file")
	}
	if err := a.json("manifest.json", buildinfo.Current()); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(a.dir, "manifest.json"))
	sum := sha256.Sum256(b)
	if a.hashes["manifest.json"] != hex.EncodeToString(sum[:]) {
		t.Fatal("hash mismatch")
	}
}

func TestJSONFileRejectsAmbiguityAndNonregularInputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	var v struct {
		Number int `json:"number"`
	}
	for _, body := range []string{`{"number":1,"number":2}`, `{"number":1,"other":2}`, `null`, `{}`, `{"number":1} {}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := readJSON(path, &v); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := readJSON(dir, &v); err == nil {
		t.Fatal("accepted directory")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), 4<<20+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(path, &v); err == nil {
		t.Fatal("accepted oversized input")
	}
	b, _ := json.Marshal(struct {
		Number int `json:"number"`
	}{7})
	_ = os.WriteFile(path, b, 0600)
	if err := readJSON(path, &v); err != nil || v.Number != 7 {
		t.Fatalf("valid decode: %v %+v", err, v)
	}
}
