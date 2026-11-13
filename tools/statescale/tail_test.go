package main

import (
	"os"
	"path/filepath"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

func TestTailStreamAndIndependentExpectations(t *testing.T) {
	const run = "00000000-0000-4000-8000-000000000001"
	for _, kind := range []string{"active", "history"} {
		e, err := engine.New(run, configuration(24))
		if err != nil {
			t.Fatal(err)
		}
		var first []d.Result
		for i := 0; i < 24; i++ {
			c := tailInput(run, kind, i, 24)
			r, err := e.Apply(d.Int(i+1), c)
			if err != nil {
				t.Fatal(err)
			}
			if i < 2 {
				first = append(first, r.Clone())
			}
			if i == 11 || i == 15 {
				for _, id := range []string{"carry-fill", "carry-cancel"} {
					o, ok := e.Order("alice", id)
					if !ok || o.Status != d.Open {
						t.Fatal("cut does not carry open order")
					}
				}
			}
		}
		if err = e.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		a, _ := e.Balances("alice")
		b, _ := e.Balances("bob")
		want := counts{Orders: 19, Active: 16, Filled: 1, Cancelled: 2, Trades: 1, Results: 24}
		if kind == "active" {
			if a.Base.Available != 125 || a.Quote.Available != 490810 || a.Quote.Reserved != 79200 || b.Base.Available != 115 || b.Base.Reserved != 8 || b.Quote.Available != 589990 {
				t.Fatal("active independent settlement", a, b)
			}
		} else {
			want = counts{Orders: 11, Filled: 5, Cancelled: 6, Trades: 5, Results: 24}
			if a.Base.Available != 129 || a.Quote.Available != 530010 || a.Quote.Reserved != 0 || b.Base.Available != 119 || b.Base.Reserved != 0 || b.Quote.Available != 629990 {
				t.Fatal("history independent settlement", a, b)
			}
		}
		if composition(e.Capture()) != want || expectedTailCounts(kind, 24) != want || expectedTailBalance(kind, "alice", 24) != a || expectedTailBalance(kind, "bob", 24) != b {
			t.Fatal("tail composition/formula differs")
		}
		for i, id := range []string{"carry-fill", "carry-cancel"} {
			o, _ := e.Order("alice", id)
			if first[i].Order.Status != d.Open || o.Status != d.Cancelled || !checkTailOrder(id, o) {
				t.Fatal("initial response/current order mixup")
			}
		}
	}
}

func TestTailInputBoundsBeforeFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad")
	out := filepath.Join(root, "result.json")
	for _, n := range []string{"0", "12", "9999", "10001"} {
		if err := command([]string{"tail-prepare", "--kind", "active", "--count", n, "--dir", dir, "--output", out}); err == nil {
			t.Fatal("invalid tail count accepted")
		}
	}
	for _, p := range []string{dir, out} {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatal("invalid input wrote files")
		}
	}
}
