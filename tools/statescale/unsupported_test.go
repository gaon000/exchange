//go:build !darwin || !cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedScaleLeavesNoOutput(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	out := filepath.Join(root, "result.json")
	if err := command([]string{"prepare", "--kind", "active", "--count", "12", "--dir", dir, "--output", out}); err == nil {
		t.Fatal("unsupported storage accepted")
	}
	for _, p := range []string{dir, out} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("unsupported probe created output")
		}
	}
}
