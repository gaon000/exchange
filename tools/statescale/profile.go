package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
)

const allocationSampleRate = 64 * 1024

type profileResult struct {
	Version                   int
	Phase, Profiler           string
	Commands                  int
	Before, After             memory
	Recovery                  rt.RecoveryReport
	SnapshotSHA256, WALSHA256 string
	Verified                  bool
	GOMAXPROCS                int
	GoVersion                 string
	MemProfileRate            int
}

func profileCommand(args []string) error {
	f := flag.NewFlagSet("profile", flag.ContinueOnError)
	dir := f.String("dir", "", "coordinator-owned copied run")
	out := f.String("output", "", "new profile directory")
	phase := f.String("phase", "", "wal-open, snapshot-open or create")
	kind := f.String("profiler", "", "cpu or alloc")
	n := f.Int("count", 0, "multiple of four, 4..10000")
	want := f.String("snapshot-sha", "", "canonical state SHA256")
	walSHA := f.String("wal-sha", "", "immutable WAL SHA256")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *dir == "" || *out == "" || *n < 4 || *n > 10000 || *n%4 != 0 || (*phase != "wal-open" && *phase != "snapshot-open" && *phase != "create") || (*kind != "cpu" && *kind != "alloc") {
		return errors.New("invalid profile arguments")
	}
	for _, sha := range []string{*want, *walSHA} {
		b, e := hex.DecodeString(sha)
		if e != nil || len(b) != 32 || hex.EncodeToString(b) != sha {
			return errors.New("invalid expected SHA256")
		}
	}
	if err := durable.CheckFilesystem(filepath.Dir(*dir)); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(*dir, ".statescale-owned")); err != nil || !info.Mode().IsRegular() {
		return errors.New("profile requires coordinator-owned run")
	}
	files, err := snapshot.List(*dir)
	if err != nil {
		return err
	}
	if (*phase == "snapshot-open" && len(files) != 1) || (*phase != "snapshot-open" && len(files) != 0) {
		return errors.New("wrong profile snapshot presence")
	}
	if err = os.Mkdir(*out, 0700); err != nil {
		return err
	}
	return profilePhase(*dir, *out, *phase, *kind, *n, *want, *walSHA)
}

func shutdownProfile(r *rt.Runtime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.Shutdown(ctx)
}

func allocationProfile(path string) error {
	// Two collections flush delayed runtime memory-profile accounting.
	runtime.GC()
	runtime.GC()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	return errors.Join(pprof.Lookup("allocs").WriteTo(f, 0), f.Close())
}

func profilePhase(dir, out, phase, kind string, n int, want, walSHA string) (err error) {
	// Set once, before the target Open or Snapshot, in this fresh worker.
	// Baseline subtraction and pprof focus exclude setup/profile serialization.
	if kind == "alloc" {
		runtime.MemProfileRate = allocationSampleRate
	}
	var r *rt.Runtime
	defer func() {
		if r != nil {
			err = errors.Join(err, shutdownProfile(r))
		}
	}()
	if phase == "create" {
		r, err = rt.Open(dir, rt.DefaultOptions())
		if err != nil {
			return err
		}
	}
	runtime.GC()
	var cpu *os.File
	if kind == "cpu" {
		cpu, err = os.OpenFile(filepath.Join(out, "cpu.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err = pprof.StartCPUProfile(cpu); err != nil {
			_ = cpu.Close()
			return err
		}
	} else if err = allocationProfile(filepath.Join(out, "alloc-before.pprof")); err != nil {
		return err
	}
	o := profileResult{Version: 1, Phase: phase, Profiler: kind, Commands: n, GOMAXPROCS: runtime.GOMAXPROCS(0), GoVersion: runtime.Version(), MemProfileRate: runtime.MemProfileRate}
	o.Before = mem()
	if phase == "create" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		var saved rt.SnapshotResult
		saved, err = r.Snapshot(ctx)
		cancel()
		if err == nil && saved.SnapshotSeq != d.Int(n) {
			err = errors.New("profile snapshot sequence mismatch")
		}
	} else {
		r, err = rt.Open(dir, rt.DefaultOptions())
	}
	o.After = mem()
	if kind == "cpu" {
		pprof.StopCPUProfile()
		err = errors.Join(err, cpu.Close())
	}
	if err != nil {
		return err
	}
	if kind == "alloc" {
		if err = allocationProfile(filepath.Join(out, "alloc-after.pprof")); err != nil {
			return err
		}
	}
	runtime.KeepAlive(r)
	// Complete recovery checks and validation publication are outside profiles.
	o.Recovery = r.Recovery()
	seq := d.Int(0)
	if phase == "snapshot-open" {
		seq = d.Int(n)
	}
	if o.Recovery.SnapshotSeq != seq || o.Recovery.ReplayedCommands != d.Int(n)-seq || len(o.Recovery.Skipped) != 0 || (o.Recovery.SnapshotFile != "") != (phase == "snapshot-open") {
		return fmt.Errorf("profile recovery mismatch: %+v", o.Recovery)
	}
	if phase != "create" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err = r.Snapshot(ctx)
		cancel()
		if err != nil {
			return err
		}
	}
	name, _ := snapshot.Filename(d.Int(n))
	o.SnapshotSHA256, err = digest(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	o.WALSHA256, err = digest(filepath.Join(dir, "commands.wal"))
	if err != nil {
		return err
	}
	if o.SnapshotSHA256 != want || o.WALSHA256 != walSHA {
		return errors.New("profile changed canonical state or WAL")
	}
	if err = shutdownProfile(r); err != nil {
		return err
	}
	r = nil
	o.Verified = true
	return save(filepath.Join(out, "result.json"), o)
}
