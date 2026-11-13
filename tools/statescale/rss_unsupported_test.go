//go:build !darwin || !cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedRSSLeavesNoOutput(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "result.json")
	if err := command([]string{"rss-measure", "--dir", root, "--receipt", filepath.Join(root, "fixture.json"), "--output", out, "--smoke"}); err == nil {
		t.Fatal("unsupported RSS accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("unsupported RSS wrote output")
	}
}
