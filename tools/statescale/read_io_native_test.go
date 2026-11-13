//go:build darwin && cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gaon000/exchange/internal/snapshot"
)

func TestNativeBufferReadDiagnostic(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	f, err := prepareTail(dir, "history", 24)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "copy")
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	name, _ := snapshot.Filename(12)
	for _, n := range []string{".lock", "genesis.bin", "commands.wal", name} {
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(target, n), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = measureTail(target, f, 12); err != nil {
		t.Fatal(err)
	}
	io, err := inspectReadIO(target, f, 12)
	if err != nil || !io.Verified || io.SnapshotSeq != 12 || io.PreadCalls.Calls < 4 {
		t.Fatal(io, err)
	}
	for n, want := range f.Files {
		got, e := digest(filepath.Join(dir, n))
		if e != nil || got != want {
			t.Fatal("original changed", n, e)
		}
	}
}
