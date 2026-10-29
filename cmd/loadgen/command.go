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
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/buildinfo"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
	"github.com/gaon000/exchange/internal/wal"
)

type settings struct {
	Workload  bench.Spec       `json:"workload"`
	Execution bench.RunOptions `json:"execution"`
}

// These are the server CLI's recorded fields. Preserve the full nested settings
// as raw JSON instead of inventing server defaults in the experiment manifest.
type serverReport struct {
	Event      string          `json:"event"`
	RunID      string          `json:"runId"`
	ConfigHash string          `json:"configHash"`
	DataDir    string          `json:"dataDir"`
	Listen     string          `json:"listen"`
	Build      buildinfo.Info  `json:"build"`
	Runtime    json.RawMessage `json:"runtime"`
	HTTP       json.RawMessage `json:"http"`
	Recovery   json.RawMessage `json:"recovery"`
}
type manifest struct {
	Version       int            `json:"version"`
	CreatedUTC    time.Time      `json:"createdUTC"`
	PlanDigest    string         `json:"planDigest"`
	Settings      settings       `json:"settings"`
	InitialConfig d.Config       `json:"initialConfig"`
	Server        serverReport   `json:"server"`
	Generator     buildinfo.Info `json:"generator"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "", "same initial JSON config used by exchange init")
	workloadFile := flags.String("workload", "", "required workload/execution JSON settings")
	serverFile := flags.String("server-report", "", "required exchange serve ready JSON report")
	output := flags.String("output", "", "new artifact directory under an existing parent")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *configFile == "" || *workloadFile == "" || *serverFile == "" || *output == "" {
		return fmt.Errorf("--config, --workload, --server-report and --output are required; no positional arguments")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var options settings
	if err := readJSON(*workloadFile, &options); err != nil {
		return fmt.Errorf("workload: %w", err)
	}
	if err := options.Execution.Validate(); err != nil {
		return err
	}
	var server serverReport
	if err := readJSON(*serverFile, &server); err != nil {
		return fmt.Errorf("server report: %w", err)
	}
	if server.Event != "ready" || server.DataDir == "" || server.Build.GoVersion == "" || string(server.Runtime) == "null" || string(server.HTTP) == "null" || string(server.Recovery) == "null" {
		return fmt.Errorf("incomplete server ready report")
	}
	var config d.Config
	if err := readJSON(*configFile, &config); err != nil {
		return fmt.Errorf("initial config: %w", err)
	}
	g, err := wal.NewGenesis(server.RunID, config)
	if err != nil {
		return err
	}
	if g.ConfigHash != server.ConfigHash {
		return fmt.Errorf("initial config does not match server config hash")
	}
	p, err := bench.Build(server.RunID, options.Workload)
	if err != nil {
		return err
	}
	if err := bench.ValidatePreparation(p, config); err != nil {
		return err
	}
	e, err := bench.NewHTTPExecutor("http://"+server.Listen, options.Execution.MaxInFlight)
	if err != nil {
		return err
	}
	defer e.Close()
	dir, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create new output directory: %w", err)
	}
	a := artifacts{dir: dir, hashes: map[string]string{}}
	completed := false
	defer func() {
		if err != nil && !completed {
			err = errors.Join(err, a.json("failed.json", struct {
				Error string    `json:"error"`
				At    time.Time `json:"atUTC"`
			}{err.Error(), time.Now().UTC()}))
		}
	}()
	m := manifest{bench.Version, time.Now().UTC(), p.Digest(), options, g.Config, server, buildinfo.Current()}
	if err := a.json("manifest.json", m); err != nil {
		return err
	}
	if err := a.write("plan.jsonl", func(w io.Writer) error {
		encoder := json.NewEncoder(w)
		for _, r := range p.Preparation() {
			if err := encoder.Encode(r); err != nil {
				return err
			}
		}
		for i := 0; i < p.Count(); i++ {
			if err := encoder.Encode(p.Request(i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	prep, prepareErr := e.Prepare(ctx, p, config, options.Execution.RequestTimeout)
	if err := errors.Join(prepareErr, a.json("preparation.json", prep)); err != nil {
		return fmt.Errorf("preparation failed; partial artifacts and server state preserved: %w", err)
	}
	result, runErr := bench.Run(ctx, p, options.Execution, e)
	// Persist all samples before summarizing, including aborted/invalid runs.
	if err := a.write("samples.jsonl", func(w io.Writer) error {
		encoder := json.NewEncoder(w)
		for _, s := range result.Samples {
			if err := encoder.Encode(s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return errors.Join(runErr, err)
	}
	if err := a.json("run.json", result); err != nil {
		return errors.Join(runErr, err)
	}
	summary, summaryErr := bench.Summarize(p, result)
	if summaryErr != nil {
		return errors.Join(runErr, summaryErr)
	}
	if err := a.json("summary.json", summary); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr != nil {
		return runErr
	}
	if !summary.MeasurementValid {
		return fmt.Errorf("invalid measurement: %v", summary.InvalidReasons)
	}
	// This marker means the artifact set is complete and internally valid, not
	// that a latency/failure-rate performance target has passed.
	complete := struct {
		Version    int               `json:"version"`
		PlanDigest string            `json:"planDigest"`
		Files      map[string]string `json:"sha256"`
	}{bench.Version, p.Digest(), a.hashes}
	if err := a.json(".completed.pending", complete); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, ".completed.pending"), filepath.Join(dir, "completed.json")); err != nil {
		return err
	}
	completed = true
	return json.NewEncoder(stdout).Encode(struct {
		Event   string        `json:"event"`
		Output  string        `json:"output"`
		Summary bench.Summary `json:"summary"`
	}{"measurement_recorded", dir, summary})
}

func readJSON(path string, target any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("input must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(fmt.Errorf("input is not a regular file"), err, f.Close())
	}
	b, err := io.ReadAll(io.LimitReader(f, wal.MaxGenesisPayload+1))
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if len(b) > wal.MaxGenesisPayload {
		return fmt.Errorf("input exceeds 4 MiB")
	}
	return strictjson.Decode(b, target)
}
