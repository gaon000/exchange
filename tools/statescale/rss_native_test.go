//go:build darwin && cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRSSRecoveryAndMemoryPhases(t *testing.T) {
	for _, kind := range []string{"active", "history"} {
		root := t.TempDir()
		dir := filepath.Join(root, "state")
		f, err := prepareTail(dir, kind, 24)
		if err != nil {
			t.Fatal(err)
		}
		// Retain only the final snapshot for this fresh recovery, as in the real collector.
		for _, cut := range []int{12, 16} {
			name := filepath.Join(dir, fmt.Sprintf("snapshot-%020d.bin", cut))
			if err = os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}
		receipt := filepath.Join(root, "fixture.json")
		if err = save(receipt, f); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(root, "result.json")
		args := []string{"rss-measure", "--dir", dir, "--receipt", receipt, "--output", out, "--smoke"}
		if err = command(args); err == nil {
			t.Fatal("unowned RSS run accepted")
		}
		if _, err = os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("unowned RSS result written")
		}
		if err = os.WriteFile(filepath.Join(dir, ".tail-owned"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err = command(args); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var o rssObservation
		if err = json.Unmarshal(data, &o); err != nil {
			t.Fatal(err)
		}
		if !o.Verified || o.Error != "" || !o.Tail.Verified || o.Tail.FirstResponsesVerified != 8 || o.Tail.RetriesVerified != 2 || len(o.Samples) != 13 || len(o.Actions) != 2 {
			t.Fatal("RSS observation/recovery incomplete")
		}
		for _, s := range o.Samples {
			m := s.Memory
			if s.RSSBytes <= 0 || s.RSSBytes > 2<<30 || s.VM.ResidentBytes == 0 || s.VM.PhysicalFootprintBytes == 0 || s.CaptureNanos <= 0 || m.HeapReleased > m.HeapIdle || m.HeapIdle+m.HeapInuse != m.HeapSys || m.StackSys < m.StackInuse {
				t.Fatalf("invalid memory sample: %+v", s)
			}
		}
		if o.Samples[7].Memory.NumGC <= o.Samples[6].Memory.NumGC || o.Samples[10].Memory.NumGC <= o.Samples[9].Memory.NumGC {
			t.Fatal("diagnostic GC controls did not collect")
		}
		if o.Actions[0].Kind != "gc-only" || o.Actions[1].Kind != "force-release" {
			t.Fatal("diagnostic stages reordered")
		}
		if err = command(args); err == nil {
			t.Fatal("existing output overwritten")
		}
	}
}
