//go:build !darwin || !cgo

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitUnsupportedDoesNotCreateRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	err := run(context.Background(), []string{"init", "--config", configFixture(t), "--data-dir", dir}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "requires macOS APFS and CGO_ENABLED=1") {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsupported init created directory", err)
	}
}
