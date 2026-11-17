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
	parallel, _ := concurrentPlan(17)
	for _, input := range []plan{p, parallel} {
		out := filepath.Join(t.TempDir(), "new")
		if err := run(context.Background(), input, out, "", io.Discard); err == nil {
			t.Fatal("unsupported storage succeeded")
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("unsupported storage created output")
		}
	}
}

func TestUnsupportedCampaignLeavesNoOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "new")
	if err := runCampaign(context.Background(), testCampaignOptions(), out, io.Discard); err == nil {
		t.Fatal("unsupported campaign succeeded")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("unsupported campaign created output")
	}
}
