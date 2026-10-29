//go:build darwin && cgo

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/api"
	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/buildinfo"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/wal"
)

func liveFixture(t *testing.T) ([]string, *rt.Runtime, settings) {
	t.Helper()
	dir := t.TempDir()
	config := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 100000, QuoteAtoms: 1000000000}, {AccountID: "bob", BaseLots: 100000, QuoteAtoms: 1000000000}, {AccountID: "reject"}})
	s, err := wal.Init(filepath.Join(dir, "run"), config)
	if err != nil {
		t.Fatal(err)
	}
	g := s.Genesis()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := rt.Open(filepath.Join(dir, "run"), rt.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	server, err := api.New(r, api.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	options := settings{Workload: bench.Spec{Namespace: "http-test", Seed: 17, Accounts: []string{"alice", "bob"}, RejectAccount: "reject", Rate: 50, Warmup: 100 * time.Millisecond, Duration: time.Second, MidPriceTicks: 100, PriceStepTicks: 1, Depth: 2, QuantityLots: 1, SeedQuantityLots: 20, Mix: bench.Mix{Rest: 35, Marketable: 30, Cancel: 10, Duplicate: 15, Reject: 10}}, Execution: bench.RunOptions{MaxInFlight: 16, RequestTimeout: time.Second, MaxLateness: 100 * time.Millisecond}}
	serverInfo := serverReport{Event: "ready", RunID: g.RunID, ConfigHash: g.ConfigHash, DataDir: filepath.Join(dir, "run"), Listen: listener.Addr().String(), Build: buildinfo.Current(), Runtime: json.RawMessage(`{"fixture":"default runtime"}`), HTTP: json.RawMessage(`{"fixture":"default HTTP"}`), Recovery: json.RawMessage(`{"fixture":"genesis"}`)}
	writeFixture(t, filepath.Join(dir, "config.json"), config)
	writeFixture(t, filepath.Join(dir, "workload.json"), options)
	writeFixture(t, filepath.Join(dir, "server.json"), serverInfo)
	return []string{"--config", filepath.Join(dir, "config.json"), "--workload", filepath.Join(dir, "workload.json"), "--server-report", filepath.Join(dir, "server.json"), "--output", filepath.Join(dir, "artifacts")}, r, options
}
func writeFixture(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCLIProducesReplayableHTTPArtifacts(t *testing.T) {
	args, r, options := liveFixture(t)
	var stdout bytes.Buffer
	if err := run(context.Background(), args, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	dir := args[7]
	var m manifest
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		t.Fatal(err)
	}
	p, err := bench.Build(r.RunID(), m.Settings.Workload)
	if err != nil || p.Digest() != m.PlanDigest {
		t.Fatal("input replay differs", err)
	}
	var result bench.RunResult
	// Samples deliberately live in their own file; json.Unmarshal honors json:"-".
	b, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	var resource runReport
	if err := json.Unmarshal(b, &resource); err != nil {
		t.Fatal(err)
	}
	if resource.GeneratorBeforeUTC.IsZero() || resource.GeneratorAfterUTC.Before(resource.GeneratorBeforeUTC) || !resource.GeneratorBefore.OSAvailable || !resource.GeneratorAfter.OSAvailable || resource.GeneratorAfter.UserCPUNanos < resource.GeneratorBefore.UserCPUNanos || len(resource.GeneratorAfter.RuntimeUnavailable) != 0 {
		t.Fatal("invalid generator resource evidence", resource)
	}
	f, err := os.Open(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var s bench.Sample
		if err := json.Unmarshal(scanner.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		result.Samples = append(result.Samples, s)
	}
	if err := errors.Join(scanner.Err(), f.Close()); err != nil {
		t.Fatal(err)
	}
	summary, err := bench.Summarize(p, result)
	if err != nil || !summary.MeasurementValid {
		t.Fatalf("summary: %+v %v", summary, err)
	}
	if summary.Measurement.Scheduled != int(options.Workload.Duration.Seconds()*float64(options.Workload.Rate)) || summary.Measurement.UniqueAcceptedOrders == 0 || summary.Measurement.UniqueTrades == 0 || summary.Measurement.UniqueBusinessRejections == 0 {
		t.Fatalf("unexercised workload: %+v", summary.Measurement)
	}
	var saved bench.Summary
	b, _ = os.ReadFile(filepath.Join(dir, "summary.json"))
	_ = json.Unmarshal(b, &saved)
	want, _ := json.Marshal(summary)
	got, _ := json.Marshal(saved)
	if !bytes.Equal(want, got) {
		t.Fatal("saved summary differs from raw samples")
	}
	var complete struct {
		Version    int               `json:"version"`
		PlanDigest string            `json:"planDigest"`
		Files      map[string]string `json:"sha256"`
	}
	if err := readJSON(filepath.Join(dir, "completed.json"), &complete); err != nil {
		t.Fatal(err)
	}
	if len(complete.Files) != 6 {
		t.Fatalf("incomplete inventory: %+v", complete.Files)
	}
	for name, hash := range complete.Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if hash != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "failed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("success contains failed marker")
	}
	if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("existing output reused")
	}
	// A different output directory still cannot reuse a previously traded run.
	args[7] = filepath.Join(filepath.Dir(dir), "again")
	if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("used run accepted")
	}
	if _, err := os.Stat(filepath.Join(args[7], "failed.json")); err != nil {
		t.Fatal("missing failed preparation", err)
	}
	if _, err := os.Stat(filepath.Join(args[7], "completed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed run marked complete")
	}
}

func TestNativeCLIInterruptedMeasurementRetainsEverySample(t *testing.T) {
	args, r, options := liveFixture(t)
	options.Workload.Duration = 5 * time.Second
	writeFixture(t, args[3], options)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, io.Discard, io.Discard) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, err := r.Read(context.Background(), rt.Query{RunID: r.RunID(), AccountID: "alice", Kind: rt.BalancesQuery})
		if err != nil {
			t.Fatal(err)
		}
		if v.AsOfSeq > d.Int(options.Workload.Depth*2) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("measurement never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption lost: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(args[7], "samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := bench.Build(r.RunID(), options.Workload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(b, []byte("\n")) != p.Count() {
		t.Fatal("canceled future samples lost")
	}
	var summary bench.Summary
	b, err = os.ReadFile(filepath.Join(args[7], "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.MeasurementValid || summary.Measurement.LocalFailures[bench.LocalCanceled] == 0 {
		t.Fatalf("canceled run reported valid: %+v", summary)
	}
	if _, err := os.Stat(filepath.Join(args[7], "completed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aborted run marked complete")
	}
	if _, err := os.Stat(filepath.Join(args[7], "failed.json")); err != nil {
		t.Fatal("missing failure", err)
	}
}

func TestNativeCLIConfigMismatchDoesNotTrade(t *testing.T) {
	args, r, _ := liveFixture(t)
	var config d.Config
	if err := readJSON(args[1], &config); err != nil {
		t.Fatal(err)
	}
	config.MaxCommands++
	writeFixture(t, args[1], config)
	if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("config mismatch accepted")
	}
	v, err := r.Read(context.Background(), rt.Query{RunID: r.RunID(), AccountID: "alice", Kind: rt.BalancesQuery})
	if err != nil || v.AsOfSeq != 0 {
		t.Fatalf("changed server: %+v %v", v, err)
	}
	if _, err := os.Stat(args[7]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created output for invalid setup")
	}
}
