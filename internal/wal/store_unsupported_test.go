//go:build !darwin || !cgo

package wal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedStorageDoesNotCreateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if s, err := Init(dir, testConfig()); s != nil || err == nil {
		t.Fatal("unsupported Init succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unsupported Init mutated directory: %v", err)
	}
	if s, err := Open(dir); s != nil || err == nil {
		t.Fatal("unsupported Open succeeded")
	}
}
