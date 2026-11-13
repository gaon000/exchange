//go:build darwin && cgo

package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gaon000/exchange/internal/snapshot"
)

func TestNativeProfileBoundariesAndState(t *testing.T) {
	previous := runtime.MemProfileRate
	defer func() { runtime.MemProfileRate = previous }()
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	f, err := prepare(dir, "history", 12)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := snapshot.Filename(12)
	for _, phase := range []string{"wal-open", "snapshot-open", "create"} {
		for _, kind := range []string{"cpu", "alloc"} {
			copy := filepath.Join(root, phase+kind)
			if err = os.Mkdir(copy, 0700); err != nil {
				t.Fatal(err)
			}
			for filename := range f.Files {
				if filename == name && phase != "snapshot-open" {
					continue
				}
				b, e := os.ReadFile(filepath.Join(dir, filename))
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(copy, filename), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			out := copy + "-profile"
			args := []string{"profile", "--dir", copy, "--output", out, "--count", "12", "--phase", phase, "--profiler", kind, "--snapshot-sha", f.Files[name], "--wal-sha", f.Files["commands.wal"]}
			if err = command(args); err == nil {
				t.Fatal("unowned profile accepted")
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("unowned profile wrote output")
			}
			if err = os.WriteFile(filepath.Join(copy, ".statescale-owned"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err = command(args); err != nil {
				t.Fatal(phase, kind, err)
			}
			if err = command(args); err == nil {
				t.Fatal("existing profile accepted")
			}
			profiles := []string{"cpu.pprof"}
			if kind == "alloc" {
				profiles = []string{"alloc-before.pprof", "alloc-after.pprof"}
			}
			for _, filename := range profiles {
				file, e := os.Open(filepath.Join(out, filename))
				if e != nil {
					t.Fatal(e)
				}
				z, e := gzip.NewReader(file)
				if e != nil {
					t.Fatal(e)
				}
				b := make([]byte, 1)
				_, e = z.Read(b)
				if e != nil {
					t.Fatal("empty profile", e)
				}
				_ = z.Close()
				_ = file.Close()
			}
			for filename, sha := range f.Files {
				got, e := digest(filepath.Join(copy, filename))
				if e != nil || got != sha {
					t.Fatal("state changed", filename, e)
				}
			}
		}
	}
}
