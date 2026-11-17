//go:build !darwin || !cgo

package runtime_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func crashChild(t *testing.T) { t.Fatal("concurrent crash child requires macOS/APFS/cgo") }
func executeCrashSuite(context.Context, crashPlan, string) error {
	return errors.New("concurrent crash requires macOS/APFS/cgo")
}
func TestUnsupportedConcurrentCrashLeavesNoOutput(t *testing.T) {
	p, _ := makeCrashPlan(17)
	out := filepath.Join(t.TempDir(), "new")
	if err := runCrashSuite(p, out, io.Discard); err == nil {
		t.Fatal("unsupported storage accepted")
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("unsupported storage wrote output")
	}
}
