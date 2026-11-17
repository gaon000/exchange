package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type campaignFailedWriter struct{}

func (campaignFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func testCampaignOptions() campaignOptions {
	return campaignOptions{Seed: 17, SeedCount: 1, Repeats: 1, Duration: time.Minute, DiskBudget: 16 << 20}
}
func TestConcurrentProfilesAndReplayValidation(t *testing.T) {
	for _, profile := range concurrentProfiles {
		p, err := profilePlan(17, profile)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := profilePlan(17, profile)
		other, _ := profilePlan(18, profile)
		if !reflect.DeepEqual(p, again) || reflect.DeepEqual(p, other) {
			t.Fatal("unseeded profile", profile)
		}
		clients := map[string]int{"cancel-race": 4, "duplicate-storm": 12, "id-conflict": 8}[profile]
		groups := 0
		for _, a := range p.Actions {
			if a.Concurrent != nil {
				groups++
				if len(a.Concurrent.Initial) != clients || len(a.Concurrent.Retries) != clients {
					t.Fatal("wrong client coverage")
				}
			}
		}
		if groups != 2 {
			t.Fatal("missing wave")
		}
	}
	p, _ := profilePlan(17, "id-conflict")
	for name, edit := range map[string]func(*plan){
		"v2":                func(p *plan) { p.Version = 2; p.Profile = "" },
		"fresh-conflict":    func(p *plan) { p.Actions[3].Concurrent.Initial[0].ExpectConflict = true },
		"unmarked-conflict": func(p *plan) { p.Actions[3].Concurrent.Initial[4].ExpectConflict = false },
		"unchanged-payload": func(p *plan) { p.Actions[3].Concurrent.Initial[4].Command = *p.Actions[0].Command },
		"fault-on-conflict": func(p *plan) { p.Actions[3].Concurrent.Initial[4].Fault = "drop-headers" },
		"wrong-profile":     func(p *plan) { p.Profile = "unknown" },
		"client-bound":      func(p *plan) { p.Actions[3].Concurrent.Initial[0].Client = 12 },
	} {
		t.Run(name, func(t *testing.T) {
			q := copyPlan(t, p)
			edit(&q)
			if validatePlan(q) == nil {
				t.Fatal("invalid v3 accepted")
			}
		})
	}
	for _, args := range [][]string{{"--campaign", "--concurrent"}, {"--campaign", "--replay", "missing"}, {"--campaign", "--actions", "64"}, {"--repeats", "2"}, {"--profile", "id-conflict"}, {"--concurrent", "--profile", "unknown"}} {
		if command(args, io.Discard, io.Discard) == nil {
			t.Fatal("mixed flags accepted", args)
		}
	}
}
func TestCampaignBoundsAndDeterministicSelection(t *testing.T) {
	o := testCampaignOptions()
	cases, err := campaignCases(o)
	if err != nil || len(cases) != 3 {
		t.Fatal(cases, err)
	}
	o.SeedCount, o.Repeats = 2, 2
	cases, err = campaignCases(o)
	if err != nil || len(cases) != 12 {
		t.Fatal(err)
	}
	again, _ := campaignCases(o)
	if !reflect.DeepEqual(cases, again) {
		t.Fatal("selection changed")
	}
	for _, edit := range []func(*campaignOptions){func(o *campaignOptions) { o.Seed = -1 }, func(o *campaignOptions) { o.Seed = 1<<31 - 1; o.SeedCount = 2 }, func(o *campaignOptions) { o.SeedCount = 0 }, func(o *campaignOptions) { o.SeedCount = 4; o.Repeats = 3 }, func(o *campaignOptions) { o.Duration = time.Second }, func(o *campaignOptions) { o.Duration = 6 * time.Minute }, func(o *campaignOptions) { o.DiskBudget = 15 << 20 }, func(o *campaignOptions) { o.Profile = "bad" }} {
		bad := o
		edit(&bad)
		if _, err := campaignCases(bad); err == nil {
			t.Fatal("invalid budget accepted", bad)
		}
	}
}

// Only budget/error plumbing is fake. Real trading and recorded replay are
// exercised on supported storage by the native regression below.
func fakeCampaignRun(ctx context.Context, p plan, dir, replay string, out io.Writer) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "scenario.json"), p); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "summary.json"), map[string]any{"status": "PASSED", "appliedSeq": "1"}); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "manifest.json"), map[string]any{"files": map[string]any{}})
}
func campaignTestDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestCampaignBudgetsCancellationAndFailurePreservation(t *testing.T) {
	for _, mode := range []string{"disk-admission", "disk-observer", "cancel", "output", "before-input"} {
		t.Run(mode, func(t *testing.T) {
			parent := campaignTestDir(t)
			output := filepath.Join(parent, "campaign")
			o := testCampaignOptions()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			executor := func(ctx context.Context, p plan, dir, replay string, out io.Writer) error {
				calls++
				if mode == "before-input" {
					return errors.New("expected preflight failure")
				}
				if err := fakeCampaignRun(ctx, p, dir, replay, out); err != nil {
					return err
				}
				switch mode {
				case "disk-admission":
					return os.WriteFile(filepath.Join(dir, "padding"), make([]byte, 9<<20), 0600)
				case "disk-observer":
					f, err := os.Create(filepath.Join(dir, "oversize"))
					if err != nil {
						return err
					}
					err = errors.Join(f.Truncate(17<<20), f.Close())
					if err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				case "cancel":
					cancel()
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			var out io.Writer = io.Discard
			if mode == "output" {
				out = campaignFailedWriter{}
			}
			err := executeCampaign(ctx, o, output, out, executor)
			if err == nil {
				t.Fatal("budget/failure accepted")
			}
			if calls != 1 {
				t.Fatal("started more work after failure", calls)
			}
			if mode == "disk-observer" && !strings.Contains(err.Error(), "disk budget") {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(output, "failed.json"))
			if err != nil {
				t.Fatal(err)
			}
			var fail struct{ Results []campaignResult }
			if err := json.Unmarshal(data, &fail); err != nil || len(fail.Results) != 1 || !strings.Contains(fail.Results[0].Reproduce, "--replay") {
				t.Fatal("missing retained input/reproduction", err, string(data))
			}
			if _, err := os.Stat(filepath.Join(output, "completed.json")); !os.IsNotExist(err) {
				t.Fatal("failure marked complete")
			}
			input := filepath.Join(output, fail.Results[0].Directory, "scenario.json")
			if mode == "before-input" {
				input = filepath.Join(output, "case-000-input.json")
			}
			if _, err := os.Stat(input); err != nil {
				t.Fatal("lost original input", err)
			}
		})
	}
}
