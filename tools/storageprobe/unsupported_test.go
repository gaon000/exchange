//go:build !darwin || !cgo

package main

import (
	"os"
	"testing"
)

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := probe(dir); err == nil {
		t.Fatal("unsupported platform reported storage success")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left files: %v, %v", entries, err)
	}
}
