//go:build !darwin || !cgo

package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gaon000/exchange/internal/durable"
)

func TestUnsupportedPublicationDoesNotCreateFiles(t *testing.T) {
	doc, _, _ := fixture(t)
	dir := filepath.Join(t.TempDir(), "not-created")
	if err := Publish(dir, doc); err == nil {
		t.Fatal("unsupported publish succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unsupported publish changed filesystem: %v", err)
	}
	if err := durable.RenameExclusive("missing", "missing2"); err == nil {
		t.Fatal("unsupported rename succeeded")
	}
}
