package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/strictjson"
	"github.com/gaon000/exchange/internal/wal"
)

type settings struct {
	Workload  bench.Spec       `json:"workload"`
	Execution bench.RunOptions `json:"execution"`
}
type extraArgs []string

func (a *extraArgs) String() string { return strings.Join(*a, " ") }
func (a *extraArgs) Set(value string) error {
	key, v, ok := strings.Cut(value, "=")
	allowed := map[string]bool{"--command-queue": true, "--query-queue": true, "--request-limit": true, "--pending-waiters": true, "--batch-commands": true, "--batch-bytes": true, "--batch-delay": true, "--read-header-timeout": true, "--read-body-timeout": true, "--result-timeout": true, "--write-timeout": true, "--http-shutdown-timeout": true}
	if !ok || v == "" || !allowed[key] {
		return fmt.Errorf("server-arg must be an allowed runtime/HTTP --option=value")
	}
	for _, previous := range *a {
		if strings.HasPrefix(previous, key+"=") {
			return fmt.Errorf("duplicate server option %s", key)
		}
	}
	*a = append(*a, value)
	return nil
}

type options struct {
	Exchange, Loadgen, Config, Workload, Output, Scenario string
	MetricsInterval, SnapshotInterval                     time.Duration
	ServerArgs                                            extraArgs
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("experiment", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.Exchange, "exchange", "bin/exchange", "server binary to copy and execute")
	f.StringVar(&o.Loadgen, "loadgen", "bin/loadgen", "load generator binary to copy and execute")
	f.StringVar(&o.Config, "config", "", "initial config JSON")
	f.StringVar(&o.Workload, "workload", "", "workload/execution JSON")
	f.StringVar(&o.Output, "output", "", "new bundle directory under existing parent")
	f.StringVar(&o.Scenario, "scenario", "normal", "normal, snapshot, profile or snapshot-profile")
	f.DurationVar(&o.MetricsInterval, "metrics-interval", time.Second, "server metrics interval (100ms to 1m)")
	f.DurationVar(&o.SnapshotInterval, "snapshot-interval", 5*time.Second, "snapshot interval in snapshot scenarios (100ms to 1h)")
	f.Var(&o.ServerArgs, "server-arg", "repeatable runtime/HTTP --option=value; cannot override output/lifecycle controls")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.Config == "" || o.Workload == "" || o.Output == "" {
		return o, fmt.Errorf("config, workload and output required; no positional arguments")
	}
	switch o.Scenario {
	case "normal", "snapshot", "profile", "snapshot-profile":
	default:
		return o, fmt.Errorf("unknown scenario")
	}
	if o.MetricsInterval < 100*time.Millisecond || o.MetricsInterval > time.Minute || o.SnapshotInterval < 100*time.Millisecond || o.SnapshotInterval > time.Hour {
		return o, fmt.Errorf("invalid sampling interval")
	}
	return o, nil
}
func (o options) snapshots() bool {
	return o.Scenario == "snapshot" || o.Scenario == "snapshot-profile"
}
func (o options) profiles() bool { return o.Scenario == "profile" || o.Scenario == "snapshot-profile" }

type invocation struct {
	Stage string   `json:"stage"`
	Argv  []string `json:"argv"`
}
type experimentManifest struct {
	Version    int          `json:"version"`
	CreatedUTC time.Time    `json:"createdUTC"`
	Scenario   string       `json:"scenario"`
	Commands   []invocation `json:"commands"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (retErr error) {
	o, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	configBytes, err := readSmall(o.Config)
	if err != nil {
		return err
	}
	if _, err := wal.DecodeConfig(configBytes); err != nil {
		return err
	}
	workBytes, err := readSmall(o.Workload)
	if err != nil {
		return err
	}
	var workload settings
	if err := strictjson.Decode(workBytes, &workload); err != nil {
		return err
	}
	if err := errors.Join(workload.Workload.Validate(), workload.Execution.Validate()); err != nil {
		return err
	}
	dir, err := filepath.Abs(o.Output)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	path := func(name string) string { return filepath.Join(dir, name) }
	completed := false
	defer func() {
		if retErr != nil && !completed {
			retErr = errors.Join(retErr, writeJSON(path("failed.json"), struct {
				Error       string    `json:"error"`
				FinishedUTC time.Time `json:"finishedUTC"`
			}{retErr.Error(), time.Now().UTC()}))
		}
	}()
	if err := os.Mkdir(path("bin"), 0700); err != nil {
		return err
	}
	for _, item := range []struct {
		source, target string
		executable     bool
	}{{o.Exchange, "bin/exchange", true}, {o.Loadgen, "bin/loadgen", true}, {o.Config, "config.json", false}, {o.Workload, "workload.json", false}} {
		if err := copyFile(item.source, path(item.target), item.executable); err != nil {
			return err
		}
	}
	// Re-read the copied inputs, binding validation to the exact bytes used by
	// both children even if the original files changed during setup.
	if b, err := readSmall(path("config.json")); err != nil {
		return err
	} else if _, err := wal.DecodeConfig(b); err != nil {
		return err
	}
	if b, err := readSmall(path("workload.json")); err != nil {
		return err
	} else if err := strictjson.Decode(b, &workload); err != nil {
		return err
	}
	if err := errors.Join(workload.Workload.Validate(), workload.Execution.Validate()); err != nil {
		return err
	}
	env, err := captureEnvironment(ctx, dir)
	if err != nil {
		return err
	}
	if err := writeJSON(path("environment.json"), env); err != nil {
		return err
	}
	initialize := []string{path("bin/exchange"), "init", "--config", path("config.json"), "--data-dir", path("run")}
	serve := []string{path("bin/exchange"), "serve", "--data-dir", path("run"), "--listen", "127.0.0.1:0", "--metrics-file", path("server-metrics.jsonl"), "--metrics-interval", o.MetricsInterval.String()}
	serve = append(serve, o.ServerArgs...)
	if o.snapshots() {
		serve = append(serve, "--snapshot-interval", o.SnapshotInterval.String(), "--snapshot-log", path("snapshot-events.jsonl"))
	}
	if o.profiles() {
		serve = append(serve, "--profile-dir", path("profiles"))
	}
	load := []string{path("bin/loadgen"), "--config", path("config.json"), "--workload", path("workload.json"), "--server-report", path("server-ready.json"), "--output", path("loadgen")}
	manifest := experimentManifest{1, time.Now().UTC(), o.Scenario, []invocation{{"init", initialize}, {"serve", serve}, {"loadgen", load}}}
	if err := writeJSON(path("manifest.json"), manifest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	initCtx, cancelInit := context.WithTimeout(ctx, 30*time.Second)
	defer cancelInit()
	p, err := startChild(initialize, path("initialized.json"), path("init.stderr"))
	if err != nil {
		return err
	}
	if err := p.wait(initCtx); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	server, err := startChild(serve, path("server-ready.json"), path("server.stderr"))
	if err != nil {
		return err
	}
	serverStopped := false
	defer func() {
		if !serverStopped {
			retErr = errors.Join(retErr, server.stop(30*time.Second))
		}
	}()
	r, err := awaitReady(ctx, server, path("server-ready.json"))
	if err != nil {
		return err
	}
	if r.DataDir != path("run") {
		return fmt.Errorf("unexpected server data directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	gen, err := startChild(load, path("loadgen.stdout"), path("loadgen.stderr"))
	if err != nil {
		return err
	}
	// The deadline includes preparation, the fixed window and artifact writing;
	// an arbitrary stalled child must not leave an experiment running forever.
	limit := workload.Workload.Warmup + workload.Workload.Duration + time.Duration(2*workload.Workload.Depth)*workload.Execution.RequestTimeout + 2*time.Minute
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), gen.stop(30*time.Second))
	case <-timer.C:
		return errors.Join(fmt.Errorf("loadgen exceeded experiment deadline"), gen.stop(30*time.Second))
	case <-server.done:
		return errors.Join(fmt.Errorf("server exited during load generation"), server.err, gen.stop(30*time.Second))
	case <-gen.done:
		if gen.err != nil {
			return fmt.Errorf("loadgen: %w", gen.err)
		}
	}
	select {
	case <-server.done:
		return errors.Join(fmt.Errorf("server exited before requested shutdown"), server.err)
	default:
	}
	stopErr := server.stop(30 * time.Second)
	serverStopped = true
	if stopErr != nil {
		return stopErr
	}
	verified, err := verifyEvidence(dir, r, workload, o)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeJSON(path("verification.json"), verified); err != nil {
		return err
	}
	hashes, err := bundleHashes(dir)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	complete := struct {
		Version     int               `json:"version"`
		RunID       string            `json:"runId"`
		FinishedUTC time.Time         `json:"finishedUTC"`
		Files       map[string]string `json:"sha256"`
	}{1, r.RunID, time.Now().UTC(), hashes}
	if err := writeJSON(path(".completed.pending"), complete); err != nil {
		return err
	}
	if err := os.Rename(path(".completed.pending"), path("completed.json")); err != nil {
		return err
	}
	completed = true
	_, err = fmt.Fprintf(stdout, "experiment recorded: %s\n", dir)
	return err
}
