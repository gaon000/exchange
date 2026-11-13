//go:build darwin && cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gaon000/exchange/internal/snapshot"
)

func TestNativeStateScaleRoundTrip(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"active", "history"} {
		dir := filepath.Join(root, kind)
		f, err := prepare(dir, kind, 12)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := snapshot.Filename(12)
		for _, mode := range []string{"wal", "snapshot"} {
			copy := filepath.Join(root, kind+"-"+mode)
			if err := os.Mkdir(copy, 0700); err != nil {
				t.Fatal(err)
			}
			for filename := range f.Files {
				if mode == "wal" && filename == name {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, filename))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(copy, filename), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := command([]string{"measure", "--count", "12", "--dir", copy, "--mode", mode, "--snapshot-sha", f.Files[name], "--wal-sha", f.Files["commands.wal"], "--output", filepath.Join(root, kind+mode+"-refused.json")}); err == nil {
				t.Fatal("unowned run measured")
			}
			if err := os.WriteFile(filepath.Join(copy, ".statescale-owned"), []byte("owned"), 0600); err != nil {
				t.Fatal(err)
			}
			o, err := measure(copy, mode, 12, f.Files[name], f.Files["commands.wal"])
			if err != nil || !o.Verified {
				t.Fatal(mode, err)
			}
			if o.Open.AfterGC.NumGC <= o.Open.After.NumGC || o.Open.PeakRSSBytes < 1<<20 {
				t.Fatal("GC/RSS measurements missing")
			}
			if _, err := measure(copy, mode, 12, "wrong", f.Files["commands.wal"]); err == nil {
				t.Fatal("mismatched state accepted")
			}
		}
		for filename, sha := range f.Files {
			got, err := digest(filepath.Join(dir, filename))
			if err != nil || got != sha {
				t.Fatal("original fixture changed", filename, err)
			}
		}
	}
}
