package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/bench"
)

func TestVerifyPlanFileRejectsMisattributionAndIncompleteRows(t *testing.T) {
	plan, err := bench.Build("11111111-1111-4111-8111-111111111111", bench.Spec{
		Namespace: "plan", Seed: 17, Accounts: []string{"alice", "bob"}, Rate: 10,
		Warmup: 100 * time.Millisecond, Duration: 100 * time.Millisecond,
		MidPriceTicks: 100, PriceStepTicks: 1, Depth: 1, QuantityLots: 1, SeedQuantityLots: 100,
		Mix: bench.Mix{Rest: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := plan.Preparation()
	for i := 0; i < plan.Count(); i++ {
		requests = append(requests, plan.Request(i))
	}
	var encoded bytes.Buffer
	for _, r := range requests {
		if err := json.NewEncoder(&encoded).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	good := strings.Split(strings.TrimSpace(encoded.String()), "\n")
	path := filepath.Join(t.TempDir(), "plan.jsonl")
	write := func(rows []string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(good)
	if err := verifyPlanFile(path, plan); err != nil {
		t.Fatal("valid preparation/warmup/measurement plan rejected", err)
	}
	for _, field := range []string{"account", "id", "phase", "scheduled", "body"} {
		t.Run(field, func(t *testing.T) {
			rows := append([]string(nil), good...)
			r := requests[len(requests)-1]
			switch field {
			case "account":
				r.Command.AccountID = "carol"
			case "id":
				r.Command.Place.ClientOrderID = "different"
			case "phase":
				r.Phase = bench.Warmup
			case "scheduled":
				r.Scheduled++
			case "body":
				r.Body = "{}"
			}
			b, _ := json.Marshal(r)
			rows[len(rows)-1] = string(b)
			write(rows)
			if err := verifyPlanFile(path, plan); err == nil {
				t.Fatal("resealed but misattributed plan accepted", field)
			}
		})
	}
	for name, rows := range map[string][]string{
		"missing": good[:len(good)-1], "extra": append(append([]string(nil), good...), good[0]),
		"reordered":     append([]string{good[1], good[0]}, good[2:]...),
		"duplicate-key": append([]string{strings.Replace(good[0], `"index":0`, `"index":0,"index":0`, 1)}, good[1:]...),
	} {
		t.Run(name, func(t *testing.T) {
			write(rows)
			if err := verifyPlanFile(path, plan); err == nil {
				t.Fatal("invalid plan accepted", name)
			}
		})
	}
}

func TestVerifyPlanOptionRejectsMissingArguments(t *testing.T) {
	for _, args := range [][]string{{"--verify-plan"}, {"--verify-plan", "input"}, {"--unknown", "input", "output"}} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
}
