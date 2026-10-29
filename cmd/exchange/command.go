package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/wal"
)

const usage = `Usage:
  exchange init --config <json-file> --data-dir <new-run-directory>
  exchange serve --data-dir <existing-run-directory> [options]

init creates a new run under an existing parent; it never reuses a directory.
serve recovers an existing run and listens on numeric loopback only.
Use exchange init -h or exchange serve -h for command options.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return errors.New("a command is required")
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usage)
		return err
	case "init":
		return initialize(ctx, args[1:], stdout, stderr)
	case "serve":
		return serveRun(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; use exchange -h", args[0])
	}
}

func initialize(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("exchange init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "required regular JSON config file")
	dataDir := flags.String("data-dir", "", "required new run directory under an existing parent")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *dataDir == "" {
		return errors.New("init requires --config and --data-dir, with no positional arguments")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	config, err := readConfig(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := wal.Init(dir, config)
	if err != nil {
		return fmt.Errorf("initialize run: %w", err)
	}
	g := store.Genesis()
	if err := store.Close(); err != nil {
		return fmt.Errorf("close initialized run: %w", err)
	}
	report := initializedReport{Event: "initialized", RunID: g.RunID, DataDir: dir, ConfigHash: g.ConfigHash, Accounts: len(g.Config.Accounts), MaxCommands: g.Config.MaxCommands}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return fmt.Errorf("run is initialized and preserved, but writing its report failed: %w", err)
	}
	return nil
}

func readConfig(path string) (d.Config, error) {
	// Reject pipes/devices before open, and recheck the opened descriptor. This
	// loader accepts local regular files (including symlinks to regular files).
	info, err := os.Stat(path)
	if err != nil {
		return d.Config{}, err
	}
	if !info.Mode().IsRegular() {
		return d.Config{}, errors.New("config must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return d.Config{}, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return d.Config{}, errors.Join(errors.New("config is not a readable regular file"), err, f.Close())
	}
	data, err := io.ReadAll(io.LimitReader(f, wal.MaxGenesisPayload+1))
	if err := errors.Join(err, f.Close()); err != nil {
		return d.Config{}, err
	}
	return wal.DecodeConfig(data)
}

type serveOptions struct {
	dir                 string
	address             string
	runtime             rt.Options
	http                api.Options
	httpShutdownTimeout time.Duration
	metricsFile         string
	metricsInterval     time.Duration
	snapshotInterval    time.Duration
	snapshotLog         string
	profileDir          string
}

func parseServe(args []string, stderr io.Writer) (serveOptions, error) {
	o := serveOptions{address: "127.0.0.1:8080", runtime: rt.DefaultOptions(), http: api.DefaultOptions(), httpShutdownTimeout: 15 * time.Second, metricsInterval: time.Second}
	flags := flag.NewFlagSet("exchange serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&o.dir, "data-dir", "", "required existing run directory; never initialized by serve")
	flags.StringVar(&o.address, "listen", o.address, "numeric loopback IP:port (port 0 chooses an available port)")
	flags.IntVar(&o.runtime.CommandQueue, "command-queue", o.runtime.CommandQueue, "maximum queued commands")
	flags.IntVar(&o.runtime.QueryQueue, "query-queue", o.runtime.QueryQueue, "maximum queued queries")
	flags.IntVar(&o.runtime.RequestLimit, "request-limit", o.runtime.RequestLimit, "shared configured limit for HTTP and runtime requests")
	flags.IntVar(&o.runtime.PendingWaiters, "pending-waiters", o.runtime.PendingWaiters, "maximum waiters per pending command key")
	flags.IntVar(&o.runtime.BatchCommands, "batch-commands", o.runtime.BatchCommands, "maximum unique commands per durable group")
	flags.IntVar(&o.runtime.BatchBytes, "batch-bytes", o.runtime.BatchBytes, "maximum durable group payload bytes")
	flags.DurationVar(&o.runtime.BatchDelay, "batch-delay", o.runtime.BatchDelay, "maximum wait to close a nonempty group")
	flags.DurationVar(&o.http.ReadHeaderTimeout, "read-header-timeout", o.http.ReadHeaderTimeout, "request header read deadline")
	flags.DurationVar(&o.http.ReadBodyTimeout, "read-body-timeout", o.http.ReadBodyTimeout, "request body read deadline")
	flags.DurationVar(&o.http.ResultTimeout, "result-timeout", o.http.ResultTimeout, "runtime result wait deadline; accepted work continues after timeout")
	flags.DurationVar(&o.http.WriteTimeout, "write-timeout", o.http.WriteTimeout, "response transmission deadline")
	flags.DurationVar(&o.httpShutdownTimeout, "http-shutdown-timeout", o.httpShutdownTimeout, "HTTP drain deadline before disconnecting clients; storage drain still waits")
	flags.StringVar(&o.metricsFile, "metrics-file", "", "optional new JSONL metrics file under an existing parent")
	flags.DurationVar(&o.metricsInterval, "metrics-interval", o.metricsInterval, "metrics sampling interval (100ms to 1m)")
	flags.DurationVar(&o.snapshotInterval, "snapshot-interval", 0, "optional periodic snapshot interval (100ms to 1h); zero disables")
	flags.StringVar(&o.snapshotLog, "snapshot-log", "", "new JSONL file required with snapshot-interval")
	flags.StringVar(&o.profileDir, "profile-dir", "", "optional new directory for CPU, final heap and allocation profiles")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 || o.dir == "" {
		return o, errors.New("serve requires --data-dir, with no positional arguments")
	}
	o.http.RequestLimit = o.runtime.RequestLimit
	if err := o.runtime.Validate(); err != nil {
		return o, fmt.Errorf("runtime options: %w", err)
	}
	if err := o.http.Validate(); err != nil {
		return o, fmt.Errorf("HTTP options: %w", err)
	}
	if o.httpShutdownTimeout <= 0 || o.httpShutdownTimeout > time.Hour {
		return o, errors.New("http-shutdown-timeout must be positive and at most 1h")
	}
	if o.metricsInterval < 100*time.Millisecond || o.metricsInterval > time.Minute {
		return o, errors.New("metrics-interval must be between 100ms and 1m")
	}
	if o.snapshotInterval != 0 && (o.snapshotInterval < 100*time.Millisecond || o.snapshotInterval > time.Hour) {
		return o, errors.New("snapshot-interval must be zero or between 100ms and 1h")
	}
	if (o.snapshotInterval != 0) != (o.snapshotLog != "") {
		return o, errors.New("snapshot-interval and snapshot-log must be specified together")
	}
	return o, nil
}

func serveRun(ctx context.Context, args []string, stdout, stderr io.Writer) (retErr error) {
	o, err := parseServe(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	// Reserve the socket before recovery: an invalid/occupied address must not
	// truncate a permissible WAL tail or acquire the run lock. No HTTP handler
	// is served until Open completes validation and strict synchronization.
	listener, err := api.ListenLoopback(o.address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	measured, cancel := context.WithCancel(ctx)
	var metricsFile, snapshotFile *os.File
	var runtime *rt.Runtime
	var sampler *metricsSampler
	var snapshots *snapshotSchedule
	var profile *profiles
	// Also covers failures before supervise takes ownership. Keep the final
	// resource sample after storage drain and profile finalization.
	defer func() {
		cancel()
		if runtime != nil {
			retErr = errors.Join(retErr, runtime.Shutdown(context.Background()))
		}
		if snapshots != nil {
			retErr = errors.Join(retErr, snapshots.Wait())
		}
		if profile != nil {
			retErr = errors.Join(retErr, profile.Finish())
		}
		// The final heap profile must retain the exchange state even when file
		// metrics are disabled and no other future closure references runtime.
		goruntime.KeepAlive(runtime)
		if sampler != nil {
			retErr = errors.Join(retErr, sampler.Finish())
		}
		for _, file := range []*os.File{metricsFile, snapshotFile} {
			if file != nil {
				retErr = errors.Join(retErr, file.Close())
			}
		}
	}()
	for _, output := range []struct {
		path *string
		file **os.File
	}{{&o.metricsFile, &metricsFile}, {&o.snapshotLog, &snapshotFile}} {
		if *output.path == "" {
			continue
		}
		*output.path, err = filepath.Abs(*output.path)
		if err != nil {
			return err
		}
		*output.file, err = os.OpenFile(*output.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create new experiment log: %w", err)
		}
	}
	if o.profileDir != "" {
		o.profileDir, err = filepath.Abs(o.profileDir)
		if err != nil {
			return err
		}
		profile, err = openProfiles(o.profileDir, cancel)
		if err != nil {
			return fmt.Errorf("create profiles: %w", err)
		}
	}
	runtime, err = rt.Open(dir, o.runtime)
	if err != nil {
		return fmt.Errorf("recover run: %w", err)
	}
	server, err := api.New(runtime, o.http)
	if err != nil {
		return err
	}
	report := readyReportFor(runtime, dir, listener.Addr().String(), o)
	if metricsFile != nil {
		sampler, err = startMetrics(metricsFile, o.metricsInterval, func(stage string) (metricsRecord, error) { return collectMetrics(stage, runtime, server, dir) }, cancel)
		metricsFile = nil // ownership includes startMetrics' error path
		if err != nil {
			return fmt.Errorf("initialize metrics: %w", err)
		}
	}
	if profile != nil {
		if err := profile.Start(); err != nil {
			return err
		}
	}
	if snapshotFile != nil {
		snapshots = startSnapshots(measured, runtime, o.snapshotInterval, snapshotFile, cancel)
		snapshotFile = nil
	}
	return supervise(measured, runtime, server, listener, report, stdout, o.httpShutdownTimeout)
}
