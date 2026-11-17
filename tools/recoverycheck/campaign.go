package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gaon000/exchange/internal/durable"
)

const campaignRunReservation int64 = 8 << 20
const campaignMetadataReservation int64 = 1 << 20

type campaignOptions struct {
	Seed       int64         `json:"seed"`
	SeedCount  int           `json:"seedCount"`
	Repeats    int           `json:"repeats"`
	Profile    string        `json:"profile"`
	Duration   time.Duration `json:"durationNanos"`
	DiskBudget int64         `json:"diskBudgetBytes"`
}
type campaignCase struct {
	Index     int    `json:"index"`
	Seed      int64  `json:"seed"`
	Repeat    int    `json:"repeat"`
	Profile   string `json:"profile"`
	Directory string `json:"directory"`
}
type campaignResult struct {
	campaignCase
	Status         string `json:"status"`
	AppliedSeq     string `json:"appliedSeq,omitempty"`
	Conflicts      int    `json:"conflicts"`
	Unknown        int    `json:"originalUnknown"`
	Retries        int    `json:"sameIDRetries"`
	ManifestSHA256 string `json:"manifestSHA256,omitempty"`
	Reproduce      string `json:"reproduce"`
	Error          string `json:"error,omitempty"`
}

func campaignCases(o campaignOptions) ([]campaignCase, error) {
	profiles := concurrentProfiles
	if o.Profile != "" && o.Profile != "all" {
		if !validProfile(o.Profile) {
			return nil, errors.New("invalid campaign profile")
		}
		profiles = []string{o.Profile}
	}
	if o.Seed < 0 || o.SeedCount < 1 || o.SeedCount > 4 || o.Seed > 1<<31-int64(o.SeedCount) || o.Repeats < 1 || o.Repeats > 3 || o.SeedCount*o.Repeats*len(profiles) > 24 || o.Duration < 10*time.Second || o.Duration > 5*time.Minute || o.DiskBudget < 16<<20 || o.DiskBudget > 256<<20 {
		return nil, errors.New("campaign requires 1..4 seeds, 1..3 repeats, <=24 runs, 10s..5m and 16..256MiB")
	}
	var cases []campaignCase
	for s := 0; s < o.SeedCount; s++ {
		for repeat := 1; repeat <= o.Repeats; repeat++ {
			for _, profile := range profiles {
				i := len(cases)
				cases = append(cases, campaignCase{Index: i, Seed: o.Seed + int64(s), Repeat: repeat, Profile: profile, Directory: fmt.Sprintf("case-%03d-%s", i, profile)})
			}
		}
	}
	return cases, nil
}
func campaignBytes(root string) (total int64, err error) {
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || e.IsDir() {
			return err
		}
		info, err := e.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular campaign evidence")
		}
		if info.Size() > 1<<40 || total > 1<<40-info.Size() {
			return errors.New("campaign evidence exceeds accounting range")
		}
		total += info.Size()
		return nil
	})
	return
}
func runCampaign(ctx context.Context, o campaignOptions, output string, stdout io.Writer) error {
	if _, err := campaignCases(o); err != nil {
		return err
	}
	abs, err := fresh(output)
	if err != nil {
		return err
	}
	if err := durable.CheckFilesystem(filepath.Dir(abs)); err != nil {
		return err
	}
	return executeCampaign(ctx, o, abs, stdout, run)
}

// executeCampaign takes an executor only to test budgets and failure preservation
// on portable hosts. The public CLI always uses real HTTP/APFS run above.
func executeCampaign(parent context.Context, o campaignOptions, output string, stdout io.Writer, executor func(context.Context, plan, string, string, io.Writer) error) (err error) {
	cases, err := campaignCases(o)
	if err != nil {
		return err
	}
	output, err = fresh(output)
	if err != nil {
		return err
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	ctx, timeout := context.WithTimeout(parent, o.Duration)
	defer timeout()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	started := time.Now()
	rows := []campaignResult{}
	next := 0
	budgetFailure := make(chan error, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				used, e := campaignBytes(output)
				if e == nil && used > o.DiskBudget-campaignMetadataReservation {
					e = errors.New("campaign disk budget exceeded during execution")
				}
				if e != nil {
					budgetFailure <- e
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-watchDone
		select {
		case e := <-budgetFailure:
			err = errors.Join(err, e)
		default:
		}
		if problem := recover(); problem != nil {
			err = errors.Join(err, fmt.Errorf("campaign panic: %v", problem))
		}
		if err != nil {
			used, accountErr := campaignBytes(output)
			err = errors.Join(err, accountErr, writeJSON(filepath.Join(output, "failed.json"), map[string]any{"status": "FAILED", "error": err.Error(), "nextCase": next, "results": rows, "observedBytesBeforeFailureRecord": used, "elapsedNanos": time.Since(started).Nanoseconds(), "powerLossTested": false}))
			fmt.Fprintf(stdout, "FAILED campaign: %s\nCompleted: %d/%d; inspect failed.json and each case reproduction\n", output, len(rows), len(cases))
		}
	}()
	if err := writeJSON(filepath.Join(output, "campaign.json"), map[string]any{"version": 1, "options": o, "cases": cases, "perRunReservationBytes": campaignRunReservation, "metadataReservationBytes": campaignMetadataReservation, "powerLossTested": false}); err != nil {
		return err
	}
	for i, item := range cases {
		next = i
		if err := ctx.Err(); err != nil {
			return err
		}
		used, err := campaignBytes(output)
		if err != nil {
			return err
		}
		if used > o.DiskBudget-campaignRunReservation-campaignMetadataReservation {
			return fmt.Errorf("disk budget cannot reserve %d bytes for case %d; used %d", campaignRunReservation, i, used)
		}
		p, err := profilePlan(item.Seed, item.Profile)
		if err != nil {
			return err
		}
		inputPath := filepath.Join(output, fmt.Sprintf("case-%03d-input.json", i))
		if err := writeJSON(inputPath, p); err != nil {
			return err
		}
		dir := filepath.Join(output, item.Directory)
		row := campaignResult{campaignCase: item, Status: "FAILED"}
		runErr := executor(ctx, p, dir, "", io.Discard)
		row.Reproduce = reproduction(dir)
		if _, err := readRegular(filepath.Join(dir, "scenario.json"), 1<<20); err != nil {
			row.Reproduce = "sh scripts/random-recovery.sh --replay " + shellQuote(inputPath)
		}
		if runErr == nil {
			data, readErr := readRegular(filepath.Join(dir, "summary.json"), 1<<20)
			var summary struct {
				Status                             string
				AppliedSeq                         string
				Conflicts, ResponseLosses, Retries int
			}
			if readErr == nil {
				readErr = json.Unmarshal(data, &summary)
			}
			runErr = readErr
			if runErr == nil && summary.Status != "PASSED" {
				runErr = errors.New("case missing PASSED summary")
			}
			if runErr == nil {
				row.Status = "PASSED"
				row.AppliedSeq = summary.AppliedSeq
				row.Conflicts = summary.Conflicts
				row.Unknown = summary.ResponseLosses
				row.Retries = summary.Retries
				row.ManifestSHA256, runErr = hashFile(filepath.Join(dir, "manifest.json"))
			}
		}
		if runErr != nil {
			row.Status = "FAILED"
			row.Error = runErr.Error()
		}
		rows = append(rows, row)
		if err := writeJSON(filepath.Join(output, fmt.Sprintf("case-%03d-result.json", i)), row); err != nil {
			return errors.Join(runErr, err)
		}
		if runErr != nil {
			return fmt.Errorf("case %d seed=%d repeat=%d profile=%s: %w; %s", i, item.Seed, item.Repeat, item.Profile, runErr, row.Reproduce)
		}
		next = i + 1
		if _, err := fmt.Fprintf(stdout, "case %d/%d PASSED seed=%d repeat=%d profile=%s\n", next, len(cases), item.Seed, item.Repeat, item.Profile); err != nil {
			return err
		}
	}
	// Stop the budget observer before publishing metadata. Actual executor cleanup
	// and each child manifest are already complete; no process is left running.
	if err := ctx.Err(); err != nil {
		return err
	}
	cancel()
	<-watchDone
	select {
	case e := <-budgetFailure:
		return e
	default:
	}
	used, err := campaignBytes(output)
	if err != nil {
		return err
	}
	if used > o.DiskBudget-campaignMetadataReservation {
		return errors.New("campaign disk budget exceeded after cleanup")
	}
	if err := writeJSON(filepath.Join(output, "summary.json"), map[string]any{"version": 1, "status": "PASSED", "plannedRuns": len(cases), "completedRuns": len(rows), "options": o, "results": rows, "observedBytesBeforeMetadata": used, "elapsedNanos": time.Since(started).Nanoseconds(), "powerLossTested": false, "performanceMeasurement": false}); err != nil {
		return err
	}
	text := []string{"# 동시 시나리오 반복 결과", "", "성능 측정이 아니다. 원래 UNKNOWN·실패와 독립 run을 구분한다. 전원 장애는 미완료·보류.", "", "| case | profile | seed | repeat | 결과 | WAL 명령 | 충돌 | 원래 UNKNOWN | 같은 ID 중복 |", "| ---: | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |"}
	for _, row := range rows {
		text = append(text, fmt.Sprintf("| %d | %s | %d | %d | %s | %s | %d | %d | %d |", row.Index, row.Profile, row.Seed, row.Repeat, row.Status, row.AppliedSeq, row.Conflicts, row.Unknown, row.Retries))
	}
	text = append(text, "", "## 각 실행 재현", "")
	for _, row := range rows {
		text = append(text, "- "+row.Reproduce)
	}
	reportFile, err := os.OpenFile(filepath.Join(output, "report.md"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(reportFile, strings.Join(text, "\n")+"\n")
	if err := errors.Join(writeErr, reportFile.Sync(), reportFile.Close()); err != nil {
		return err
	}
	// Each run is independently analyzable by the existing read-only report.
	entries := []map[string]any{}
	for _, item := range cases {
		entries = append(entries, map[string]any{"name": fmt.Sprintf("%s seed%d repeat%d", item.Profile, item.Seed, item.Repeat), "kind": "concurrent", "bundle": item.Directory})
	}
	if err := writeJSON(filepath.Join(output, "transport-spec.json"), map[string]any{"version": 1, "inputs": entries}); err != nil {
		return err
	}
	files := map[string]any{}
	if err := filepath.WalkDir(output, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular evidence")
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(output, path)
		files[filepath.ToSlash(rel)] = map[string]any{"bytes": info.Size(), "sha256": hash}
		return err
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(output, "manifest.json"), map[string]any{"version": 1, "files": files}); err != nil {
		return err
	}
	hash, err := hashFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		return err
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if time.Since(started) > o.Duration {
		return errors.New("campaign deadline exceeded while sealing")
	}
	finalBytes, err := campaignBytes(output)
	if err != nil {
		return err
	}
	if finalBytes > o.DiskBudget-4096 {
		return errors.New("campaign disk budget exceeded while sealing")
	}
	if _, err := fmt.Fprintf(stdout, "PASSED campaign runs=%d\nEvidence: %s\nReport: %s\n", len(rows), output, filepath.Join(output, "report.md")); err != nil {
		return err
	}
	return writeJSON(filepath.Join(output, "completed.json"), map[string]any{"version": 1, "status": "PASSED", "manifestSHA256": hash, "powerLossTested": false})
}
