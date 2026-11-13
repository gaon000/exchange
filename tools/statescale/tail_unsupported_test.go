//go:build !darwin || !cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedTailLeavesNoOutput(t *testing.T) {
	root := t.TempDir()
	dir, output := filepath.Join(root, "tail"), filepath.Join(root, "result.json")
	if err := command([]string{"tail-prepare", "--kind", "active", "--count", "24", "--dir", dir, "--output", output}); err == nil {
		t.Fatal("unsupported tail preparation accepted")
	}
	for _, p := range []string{dir, output} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("unsupported tail wrote files")
		}
	}
}
