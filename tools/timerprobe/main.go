package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"sync"
	"syscall"
	"time"
)

// Row records a monotonic scheduling boundary, never a trading result.
type Row struct {
	Index     int    `json:"index"`
	Scheduled int64  `json:"scheduledNanos"`
	WaitStart int64  `json:"waitStartNanos"`
	WaitEnd   int64  `json:"waitEndNanos"`
	Worker    int64  `json:"workerNanos"`
	Failure   string `json:"failure,omitempty"`
}

func schedule(ctx context.Context, n, workers int, observe bool) ([]Row, time.Time, error) {
	rows := make([]Row, n)
	jobs := make(chan int)
	ready := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var epoch time.Time
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			for i := range jobs {
				now := time.Since(epoch)
				rows[i].Worker = now.Nanoseconds()
				if now-time.Duration(rows[i].Scheduled) > 50*time.Millisecond {
					rows[i].Failure = "worker"
				}
				if observe && now-time.Duration(rows[i].Scheduled) > 10*time.Millisecond {
					trace.Log(ctx, "worker-delay", fmt.Sprintf("index=%d offset=%d delay=%d", i, now, now-time.Duration(rows[i].Scheduled)))
				}
			}
		}()
	}
	for w := 0; w < workers; w++ {
		<-ready
	}
	epoch = time.Now().Add(20 * time.Millisecond)
	defer func() { close(jobs); wg.Wait() }()
	for i := range rows {
		rows[i] = Row{Index: i, Scheduled: int64(i) * int64(10*time.Millisecond), Worker: -1}
		rows[i].WaitStart = time.Since(epoch).Nanoseconds()
		delay := time.Until(epoch.Add(time.Duration(rows[i].Scheduled)))
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return rows[:i], epoch, ctx.Err()
			case <-t.C:
			}
		} else if ctx.Err() != nil {
			return rows[:i], epoch, ctx.Err()
		}
		end := time.Since(epoch)
		rows[i].WaitEnd = end.Nanoseconds()
		late := end - time.Duration(rows[i].Scheduled)
		if observe && late > 10*time.Millisecond {
			trace.Log(ctx, "scheduler-delay", fmt.Sprintf("index=%d offset=%d delay=%d", i, end, late))
		}
		if late > 50*time.Millisecond {
			rows[i].Failure = "scheduler"
			continue
		}
		if workers > 0 {
			select {
			case jobs <- i:
			default:
				rows[i].Failure = "capacity"
			}
		}
	}
	return rows, epoch, nil
}

type limited struct {
	f   *os.File
	n   int64
	err error
}

func (w *limited) Write(b []byte) (int, error) {
	if w.n+int64(len(b)) > 128<<20 {
		w.err = fmt.Errorf("trace limit")
		return 0, w.err
	}
	n, e := w.f.Write(b)
	w.n += int64(n)
	if e != nil {
		w.err = e
	}
	return n, e
}
func run(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("timerprobe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	observe := flags.Bool("trace", false, "enable runtime trace and delay logs")
	duration := flags.Duration("duration", 600*time.Second, "up to 600s")
	workers := flags.Int("workers", 0, "0 or 2048")
	out := flags.String("output", "", "new output directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *duration%(10*time.Millisecond) != 0 || *duration <= 0 || *duration > 600*time.Second || (*workers != 0 && *workers != 2048) || *out == "" {
		return fmt.Errorf("invalid options")
	}
	if e := os.Mkdir(*out, 0700); e != nil {
		return e
	}
	var w *limited
	if *observe {
		f, e := os.OpenFile(filepath.Join(*out, "timer.trace"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		w = &limited{f: f}
		if e = trace.Start(w); e != nil {
			f.Close()
			return e
		}
		trace.Log(ctx, "clock", time.Now().UTC().Format(time.RFC3339Nano))
	}
	rows, epoch, err := schedule(ctx, int(*duration/(10*time.Millisecond)), *workers, *observe)
	var traceBytes int64
	if *observe {
		trace.Log(ctx, "clock", time.Now().UTC().Format(time.RFC3339Nano))
		trace.Stop()
		err = errors.Join(err, w.err, w.f.Sync(), w.f.Close())
		traceBytes = w.n
	}
	data, e := os.OpenFile(filepath.Join(*out, "samples.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	enc := json.NewEncoder(data)
	for _, r := range rows {
		if e = enc.Encode(r); e != nil {
			data.Close()
			return e
		}
	}
	if e = errors.Join(data.Sync(), data.Close()); e != nil {
		return e
	}
	meta := map[string]any{"epochUTC": epoch.UTC(), "finishedUTC": time.Now().UTC(), "durationNanos": *duration, "workers": *workers, "requestsPerSecond": 100, "count": len(rows), "traceBytes": traceBytes, "traceEnabled": *observe, "requestedCount": int(*duration / (10 * time.Millisecond)), "aborted": err != nil}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if e := writeNew(filepath.Join(*out, "run.json"), append(b, '\n')); e != nil {
		return errors.Join(err, e)
	}
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return errors.Join(err, writeNew(filepath.Join(*out, "failed.json"), append(b, '\n')))
	}
	return nil
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeNew(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	return errors.Join(e, f.Sync(), f.Close())
}
