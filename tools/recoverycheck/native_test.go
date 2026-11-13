//go:build darwin && cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

func scenarioParent(t *testing.T) string {
	t.Helper()
	if parent := os.Getenv("EXCHANGE_SCENARIO_EVIDENCE_PARENT"); parent != "" {
		path, err := os.MkdirTemp(parent, "random-recovery-"+t.Name()+"-")
		if err != nil {
			t.Fatal(err)
		}
		// CI owns this parent and retains these small successful/failed fixtures.
		return path
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func runScenario(t *testing.T, p plan, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := run(ctx, p, output, "", io.Discard); err != nil {
		t.Fatalf("%v\nReproduce: sh scripts/random-recovery.sh --replay %s", err, shellQuote(filepath.Join(output, "scenario.json")))
	}
	if _, err := os.Stat(filepath.Join(output, "completed.json")); err != nil {
		t.Fatal("missing completion", err)
	}
}

func TestNativeSeededRecoveryScenarios(t *testing.T) {
	parent := scenarioParent(t)
	for _, seed := range []int64{17, 42, 701} {
		p, _ := generate(seed, 64)
		output := filepath.Join(parent, "seed-"+stringInt(seed))
		runScenario(t, p, output)
		var report struct{ SnapshotSeq, ReplayedCommands d.Int }
		decodeFile(t, filepath.Join(output, "restart-012.json"), &report)
		if report.SnapshotSeq != 6 || report.ReplayedCommands != 1 {
			t.Fatal("mandatory snapshot+tail recovery missing", report)
		}
		var summary struct{ Retries, Conflicts, Restarts, GenesisReplayChecks int }
		decodeFile(t, filepath.Join(output, "summary.json"), &summary)
		if summary.Retries < 2 || summary.Conflicts < 2 || summary.Restarts < 2 || summary.GenesisReplayChecks != summary.Restarts {
			t.Fatal("missing seeded coverage", summary)
		}
	}
}

func stringInt(n int64) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func decodeFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func normalized(t *testing.T, path string) d.Checkpoint {
	t.Helper()
	var cp d.Checkpoint
	decodeFile(t, path, &cp)
	for i := range cp.Results {
		cp.Results[i].Response.RunID = ""
	}
	for i := range cp.Trades {
		_, suffix, _ := strings.Cut(cp.Trades[i].TradeID, ":")
		cp.Trades[i].TradeID = suffix
	}
	return cp
}

func TestNativeSavedPlanReplayAndFailurePreservation(t *testing.T) {
	parent := scenarioParent(t)
	first := filepath.Join(parent, "first")
	p, _ := generate(17, 32)
	runScenario(t, p, first)
	input := filepath.Join(first, "scenario.json")
	before, _ := hashFile(input)
	replay := filepath.Join(parent, "replay")
	if err := command([]string{"--replay", input, "--output", replay}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized(t, filepath.Join(first, "final-actual.json")), normalized(t, filepath.Join(replay, "final-actual.json"))) {
		t.Fatal("replay logical state differs (run identity excluded)")
	}
	after, _ := hashFile(input)
	if before != after {
		t.Fatal("replay modified original evidence")
	}
	if err := command([]string{"--replay", input, "--output", replay}, io.Discard, io.Discard); err == nil {
		t.Fatal("existing output was reused")
	}
	failed := filepath.Join(parent, "expected-cancelled-failure")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	if err := run(ctx, p, failed, "", &stdout); err == nil {
		t.Fatal("cancelled run reported success")
	}
	var failure struct {
		Step      int
		Reproduce string
	}
	decodeFile(t, filepath.Join(failed, "failed.json"), &failure)
	if failure.Step != 0 || !strings.Contains(failure.Reproduce, "--replay") || !strings.Contains(stdout.String(), "Reproduce:") {
		t.Fatal("missing reproducible failure evidence", failure, stdout.String())
	}
	if _, err := os.Stat(filepath.Join(failed, "completed.json")); !os.IsNotExist(err) {
		t.Fatal("failed run emitted completion")
	}
	// The interruption is external, while the retained action list can be
	// rerun unchanged once the cancellation is gone.
	if err := command([]string{"--replay", filepath.Join(failed, "scenario.json"), "--output", filepath.Join(parent, "after-cancellation")}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestNativeZeroSnapshotAndStateMismatchDetection(t *testing.T) {
	parent := scenarioParent(t)
	runScenario(t, plan{Version: 1, Seed: 0, Actions: []action{{Kind: "snapshot"}, {Kind: "restart"}}}, filepath.Join(parent, "zero"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"runId":"wrong","asOfSeq":"0","BTC":{"availableLots":"999","reservedLots":"0"},"USDT":{"availableAtoms":"0","reservedAtoms":"0"}}`)
	}))
	defer server.Close()
	model, _ := engine.New("00000000-0000-4000-8000-000000000001", config())
	step := 0
	s := &session{client: server.Client(), base: server.URL, trace: json.NewEncoder(io.Discard), step: &step}
	if err := s.checkViews(context.Background(), "00000000-0000-4000-8000-000000000001", model, nil); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatal("contradictory HTTP state passed", err)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("expected output error") }

func TestNativeOutputFailureCannotPublishCompletion(t *testing.T) {
	parent := scenarioParent(t)
	out := filepath.Join(parent, "expected-output-failure")
	p := plan{Version: 1, Seed: 0, Actions: []action{{Kind: "snapshot"}}}
	if err := run(context.Background(), p, out, "", failedWriter{}); err == nil {
		t.Fatal("output failure reported success")
	}
	if _, err := os.Stat(filepath.Join(out, "completed.json")); !os.IsNotExist(err) {
		t.Fatal("completion published before output failure")
	}
	if _, err := os.Stat(filepath.Join(out, "failed.json")); err != nil {
		t.Fatal("failure evidence lost", err)
	}
}
