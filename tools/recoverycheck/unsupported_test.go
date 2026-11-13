//go:build !darwin || !cgo

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedScenarioLeavesNoOutput(t *testing.T) {
	p, _ := generate(17, 32)
	out := filepath.Join(t.TempDir(), "new")
	if err := run(context.Background(), p, out, "", io.Discard); err == nil {
		t.Fatal("unsupported storage succeeded")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("unsupported storage created output")
	}
}
