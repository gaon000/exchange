package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

func TestDeterministicPlansAndBounds(t *testing.T) {
	a, err := generate(17, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generate(17, 64)
	c, _ := generate(42, 64)
	if !reflect.DeepEqual(a, b) || reflect.DeepEqual(a.Actions, c.Actions) || len(a.Actions) != 64 || a.Actions[63].Kind != "restart" {
		t.Fatal("seed/action schedule is not deterministic")
	}
	for _, n := range []int{31, 257, -1} {
		if _, err := generate(17, n); err == nil {
			t.Fatal("unbounded action count accepted", n)
		}
	}
	for _, seed := range []int64{-1, 0, 17, 42, 701} {
		if _, err := generate(seed, 256); err != nil {
			t.Fatal(seed, err)
		}
	}
	original := a.Actions[0].Command.Place.QuantityLots
	copy := clone(a.Actions[0])
	copy.Command.Place.QuantityLots++
	if a.Actions[0].Command.Place.QuantityLots != original {
		t.Fatal("conflict generation mutated original payload")
	}
}

func TestMandatoryTradingPathsAndFirstResults(t *testing.T) {
	p, _ := generate(17, 32)
	run := "00000000-0000-4000-8000-000000000001"
	m, _ := engine.New(run, config())
	var first d.Result
	for i, a := range p.Actions[:10] {
		c, _ := a.Command.Command(run)
		got, err := m.Apply(m.AppliedSeq()+1, c)
		if i == 3 || i == 6 {
			if !errors.Is(err, engine.ErrConflict) {
				t.Fatal("missing namespace conflict", i, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			first = got
		case 1:
			if got.TradeCount != 1 || got.Order.Status != d.Filled {
				t.Fatal("partial maker fill not exercised", got)
			}
		case 2:
			if !reflect.DeepEqual(got, first) || got.Order.Status != d.Open {
				t.Fatal("initial OPEN result changed after fill")
			}
		case 4:
			if got.Order.ExecutedQty != 2 || got.Order.CancelledQty != 4 || got.Code != d.CancelSucceeded {
				t.Fatal("residual cancellation missing", got)
			}
		case 8:
			if got.Order.Reason != d.SelfTradePrevention || got.Order.CancelledQty != 1 {
				t.Fatal("self-trade prevention missing", got)
			}
		case 9:
			if got.Code != d.InsufficientBalance {
				t.Fatal("persisted rejection missing", got)
			}
		}
	}
	bob, _ := m.Balances("bob")
	alice, _ := m.Balances("alice")
	if bob.Base.Available != 98 || bob.Base.Reserved != 0 || bob.Quote.Available != 100200 ||
		alice.Base.Available != 102 || alice.Quote.Available != 99800 || m.AppliedSeq() != 6 {
		t.Fatal("prefix reservation/refund arithmetic mismatch", bob, alice)
	}
}

func TestRejectMalformedReplayBeforeOutput(t *testing.T) {
	root := t.TempDir()
	for i, data := range []string{
		`{"version":1,"version":1,"seed":17,"actions":[]}`,
		`{"version":1,"seed":17,"actions":[{"kind":"restart","unknown":1}]}`,
		`{"version":1,"seed":17,"actions":[{"kind":"command"}]}`,
		`{"version":1,"seed":17,"actions":[{"kind":"delete"}]}`,
	} {
		input := filepath.Join(root, "input.json")
		if err := os.WriteFile(input, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(root, "out")
		err := command([]string{"--replay", input, "--output", output}, io.Discard, io.Discard)
		if err == nil {
			t.Fatal("invalid replay accepted", i)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("invalid input created output")
		}
	}
	if err := command([]string{"--help"}, io.Discard, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	if err := command([]string{"--replay", "missing", "--seed", "17"}, io.Discard, io.Discard); err == nil {
		t.Fatal("ambiguous replay options accepted")
	}
}

func TestOutputAndReplayFileProtection(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(input, "scenario.json"), plan{}); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(input, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{input, filepath.Join(input, "child"), filepath.Join(alias, "child")} {
		if _, err := fresh(path); err == nil {
			t.Fatal("input/reused/linked output accepted", path)
		}
	}
	if _, err := readRegular(filepath.Join(alias, "missing"), 8); err == nil {
		t.Fatal("missing replay accepted")
	}
	link := filepath.Join(root, "linked-file")
	_ = os.Symlink(filepath.Join(input, "scenario.json"), link)
	if _, err := readRegular(link, 1<<20); err == nil {
		t.Fatal("symlink input accepted")
	}
	if _, err := readRegular(filepath.Join(input, "scenario.json"), 1); err == nil {
		t.Fatal("oversized replay accepted")
	}
	if shellQuote("a'b") != `'a'"'"'b'` {
		t.Fatal("unsafe reproduction quoting")
	}
}

func TestSavedActionListDoesNotDependOnGenerator(t *testing.T) {
	p, _ := generate(17, 32)
	b, _ := json.Marshal(p)
	var recovered plan
	if err := json.Unmarshal(b, &recovered); err != nil || !reflect.DeepEqual(p, recovered) {
		t.Fatal("plan lost payload or boundaries", err)
	}
	// Explicit edited action lists are supported independently of the current
	// generator. The bounded, versioned v1 semantics still apply.
	recovered.Actions = []action{{Kind: "snapshot"}, {Kind: "restart"}}
	if err := validatePlan(recovered); err != nil {
		t.Fatal(err)
	}
	recovered.Actions = append(recovered.Actions, make([]action, 33)...)
	for i := range recovered.Actions {
		recovered.Actions[i] = action{Kind: "restart"}
	}
	if err := validatePlan(recovered); err == nil {
		t.Fatal("unbounded restart count accepted")
	}
}
