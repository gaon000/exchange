package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/gaon000/exchange/internal/durable"
)

type rssMemory struct {
	HeapAlloc, HeapInuse, HeapSys, HeapIdle, HeapReleased, HeapObjects uint64
	StackInuse, StackSys, MSpanInuse, MSpanSys, MCacheInuse, MCacheSys uint64
	GCSys, OtherSys, Sys, TotalAlloc, Mallocs, Frees, NumGC            uint64
}
type rssVM struct {
	ResidentBytes, PhysicalFootprintBytes, ReusableBytes, CompressedBytes uint64
	InternalBytes, ExternalBytes, VirtualBytes                            uint64
}
type rssSample struct {
	Stage                                    string
	TargetMillis, ElapsedNanos, CaptureNanos int64
	Memory                                   rssMemory
	VM                                       rssVM
	RSSBytes, PeakRSSBytes                   int64
	Goroutines                               int
}
type rssAction struct {
	Kind          string
	DurationNanos int64
}
type rssObservation struct {
	Version         int
	Smoke, Verified bool
	Error           string `json:",omitempty"`
	Tail            tailObservation
	Samples         []rssSample
	Actions         []rssAction
}

func rssTimes(smoke bool) (idle, control []time.Duration) {
	if smoke {
		return []time.Duration{0, time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}, []time.Duration{0, time.Millisecond, 5 * time.Millisecond}
	}
	return []time.Duration{0, 100 * time.Millisecond, time.Second, 5 * time.Second, 15 * time.Second}, []time.Duration{0, 100 * time.Millisecond, time.Second}
}

func captureRSS(stage string, target time.Duration, start time.Time) (rssSample, error) {
	begin := time.Now()
	s := rssSample{Stage: stage, TargetMillis: target.Milliseconds(), ElapsedNanos: begin.Sub(start).Nanoseconds()}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s.Memory = rssMemory{HeapAlloc: m.HeapAlloc, HeapInuse: m.HeapInuse, HeapSys: m.HeapSys, HeapIdle: m.HeapIdle, HeapReleased: m.HeapReleased, HeapObjects: m.HeapObjects,
		StackInuse: m.StackInuse, StackSys: m.StackSys, MSpanInuse: m.MSpanInuse, MSpanSys: m.MSpanSys, MCacheInuse: m.MCacheInuse, MCacheSys: m.MCacheSys,
		GCSys: m.GCSys, OtherSys: m.OtherSys, Sys: m.Sys, TotalAlloc: m.TotalAlloc, Mallocs: m.Mallocs, Frees: m.Frees, NumGC: uint64(m.NumGC)}
	var err error
	if s.RSSBytes, err = residentRSS(); err != nil {
		return s, err
	}
	if s.PeakRSSBytes, err = peakRSS(); err != nil {
		return s, err
	}
	if s.VM, err = vmMemory(); err != nil {
		return s, err
	}
	s.Goroutines = runtime.NumGoroutine()
	s.CaptureNanos = time.Since(begin).Nanoseconds()
	if s.RSSBytes <= 0 || s.RSSBytes > 2<<30 || s.PeakRSSBytes <= 0 || s.PeakRSSBytes > 2<<30 || s.VM.PhysicalFootprintBytes > 2<<30 || s.Memory.HeapAlloc > 2<<30 {
		return s, errors.New("RSS diagnostic memory budget exceeded/unavailable")
	}
	return s, nil
}

func observeRSS(o *rssObservation, stage string, smoke bool) error {
	sample := func(name string, target time.Duration, start time.Time) error {
		s, err := captureRSS(name, target, start)
		o.Samples = append(o.Samples, s)
		return err
	}
	if stage != "after-gc" {
		return sample(stage, 0, time.Now())
	}
	idle, control := rssTimes(smoke)
	series := func(name string, times []time.Duration) error {
		start := time.Now()
		for _, target := range times {
			if remaining := time.Until(start.Add(target)); remaining > 0 {
				time.Sleep(remaining)
			}
			if err := sample(name, target, start); err != nil {
				return err
			}
		}
		return nil
	}
	if err := series("idle", idle); err != nil {
		return err
	}
	start := time.Now()
	runtime.GC()
	o.Actions = append(o.Actions, rssAction{Kind: "gc-only", DurationNanos: time.Since(start).Nanoseconds()})
	if err := series("gc-only", control); err != nil {
		return err
	}
	start = time.Now()
	debug.FreeOSMemory()
	o.Actions = append(o.Actions, rssAction{Kind: "force-release", DurationNanos: time.Since(start).Nanoseconds()})
	return series("force-release", control)
}

func rssCommand(args []string) (err error) {
	fs := flag.NewFlagSet("rss-measure", flag.ContinueOnError)
	dir := fs.String("dir", "", "coordinator-owned copied run")
	receipt := fs.String("receipt", "", "tail fixture receipt")
	out := fs.String("output", "", "new JSON file")
	smoke := fs.Bool("smoke", false, "short diagnostic observation intervals")
	if err = fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *receipt == "" || *out == "" {
		return errors.New("invalid RSS arguments")
	}
	if err = durable.CheckFilesystem(filepath.Dir(*dir)); err != nil {
		return err
	}
	if _, e := os.Lstat(*out); !os.IsNotExist(e) {
		return errors.New("RSS output must be new")
	}
	info, e := os.Lstat(filepath.Join(*dir, ".tail-owned"))
	if e != nil || !info.Mode().IsRegular() {
		return errors.New("RSS requires coordinator-owned copy")
	}
	data, e := os.ReadFile(*receipt)
	if e != nil {
		return e
	}
	var f tailFixture
	if err = json.Unmarshal(data, &f); err != nil {
		return err
	}
	n := 10000
	if *smoke {
		n = 24
	}
	if f.Version != 1 || f.Commands != n || (f.Kind != "active" && f.Kind != "history") || len(f.Witnesses) != 8 || f.Counts != expectedTailCounts(f.Kind, n) {
		return errors.New("invalid RSS fixture")
	}
	o := rssObservation{Version: 1, Smoke: *smoke, Samples: make([]rssSample, 0, 13), Actions: make([]rssAction, 0, 2)}
	defer func() {
		if err != nil {
			o.Error = fmt.Sprint(err)
		}
		err = errors.Join(err, save(*out, o))
	}()
	o.Tail, err = measureTailObserved(*dir, f, 0, true, func(stage string) error { return observeRSS(&o, stage, *smoke) })
	o.Verified = err == nil && o.Tail.Verified
	return err
}
