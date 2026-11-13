package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

func TestFixtureCompositionsAndIndependentBalances(t *testing.T) {
	const run = "00000000-0000-4000-8000-000000000001"
	for _, kind := range []string{"active", "history"} {
		c := configuration(12)
		e, err := engine.New(run, c)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 12; i++ {
			cmd := input(run, kind, i)
			if err := cmd.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Apply(d.Int(i+1), cmd); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		got := composition(e.Capture())
		want := counts{Orders: 12, Active: 12, Results: 12}
		alice, _ := e.Balances("alice")
		bob, _ := e.Balances("bob")
		if kind == "history" {
			want = counts{Orders: 6, Filled: 3, Cancelled: 3, Trades: 3, Results: 12}
			if alice.Base.Available != 115 || bob.Base.Available != 109 || alice.Quote.Available != 310000 || bob.Quote.Available != 370000 || bob.Base.Reserved != 0 {
				t.Fatal("trade/refund arithmetic", alice, bob)
			}
		} else if alice.Quote.Reserved != 59400 || bob.Base.Reserved != 6 {
			t.Fatal("resting reservations", alice, bob)
		}
		if got != want {
			t.Fatal(kind, got, want)
		}
	}
}

func TestProbeInputBoundsAndOutputExclusion(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{}, {"prepare", "--count", "10001", "--dir", filepath.Join(root, "bad"), "--output", filepath.Join(root, "result.json")},
		{"prepare", "--count", "3", "--dir", filepath.Join(root, "bad"), "--output", filepath.Join(root, "result.json")},
	} {
		if err := command(args); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "bad")); !os.IsNotExist(err) {
		t.Fatal("invalid input wrote files")
	}
	p := filepath.Join(root, "once.json")
	if err := save(p, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := save(p, map[string]int{"a": 2}); err == nil {
		t.Fatal("output reused")
	}
	f, _ := os.Open(p)
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "{\n  \"a\": 1\n}\n" {
		t.Fatal(string(b))
	}
}
