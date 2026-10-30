//go:build !darwin || !arm64 || !cgo

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedDemoLeavesNoOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if err := run(context.Background(), []string{"--output", dir}, io.Discard, io.Discard); err == nil {
		t.Fatal("unsupported platform reported success")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsupported demo created output", err)
	}
}
