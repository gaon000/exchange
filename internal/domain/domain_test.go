package domain_test

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

func TestIntegerWireAndBoundaries(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{`"000100"`, `"100"`}, {`"0"`, `"0"`}, {`"9223372036854775807"`, `"9223372036854775807"`}, {`"\u0031"`, `"1"`}} {
		var n d.Int
		if err := json.Unmarshal([]byte(tc.input), &n); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(n)
		if err != nil || string(b) != tc.want {
			t.Fatalf("%s: %s %v", tc.input, b, err)
		}
	}
	for _, input := range []string{`null`, `1`, `1.0`, `true`, `""`, `"-1"`, `"+1"`, `"1e2"`, `"1.0"`, `" 1"`, `"１"`, `"9223372036854775808"`, `"00000000000000000000"`} {
		n := d.Int(7)
		if err := json.Unmarshal([]byte(input), &n); err == nil || n != 7 {
			t.Fatalf("invalid input mutated or accepted: %s, %d", input, n)
		}
	}
	if _, err := json.Marshal(d.Int(-1)); err == nil {
		t.Fatal("negative output")
	}
	for _, tc := range []struct {
		f          func(d.Int, d.Int) (d.Int, error)
		a, b, want d.Int
		bad        bool
	}{
		{d.Add, math.MaxInt64, 0, math.MaxInt64, false}, {d.Add, math.MaxInt64, 1, 0, true}, {d.Add, -1, 1, 0, true},
		{d.Mul, math.MaxInt64, 1, math.MaxInt64, false}, {d.Mul, math.MaxInt64, 2, 0, true}, {d.Mul, 0, math.MaxInt64, 0, false}, {d.Mul, -1, 0, 0, true},
		{d.Sub, 1, 1, 0, false}, {d.Sub, 0, 1, 0, true}, {d.Sub, 1, -1, 0, true},
	} {
		n, err := tc.f(tc.a, tc.b)
		if (err != nil) != tc.bad || n != tc.want {
			t.Fatalf("arithmetic %+v: %d %v", tc, n, err)
		}
	}
}

func TestConfigFixtureAndInvalidInputs(t *testing.T) {
	data, err := os.ReadFile("../../config/examples/local.json")
	if err != nil {
		t.Fatal(err)
	}
	var c d.Config
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 10000, QuoteAtoms: 100000000000}, {AccountID: "bob", BaseLots: 10000, QuoteAtoms: 100000000000}, {AccountID: "carol", BaseLots: 10000, QuoteAtoms: 100000000000}})) {
		t.Fatal("fixture drift")
	}
	for name, change := range map[string]func(*d.Config){
		"version": func(c *d.Config) { c.RulesVersion++ }, "unit": func(c *d.Config) { c.BaseLotDecimals++ }, "limit": func(c *d.Config) { c.MaxPriceTicks-- }, "no commands": func(c *d.Config) { c.MaxCommands = 0 },
		"empty": func(c *d.Config) { c.Accounts = nil }, "too many": func(c *d.Config) { c.Accounts = make([]d.InitialAccount, 10001) }, "duplicate": func(c *d.Config) { c.Accounts[1] = c.Accounts[0] }, "id": func(c *d.Config) { c.Accounts[0].AccountID = "한글" }, "negative": func(c *d.Config) { c.Accounts[0].BaseLots = -1 },
		"base total": func(c *d.Config) { c.Accounts[0].BaseLots = d.MaxBaseSupplyLots }, "quote total": func(c *d.Config) { c.Accounts[0].QuoteAtoms = d.MaxQuoteSupplyAtoms }, "overflow": func(c *d.Config) { c.Accounts[1].QuoteAtoms = math.MaxInt64 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := c
			copy.Accounts = append([]d.InitialAccount(nil), c.Accounts...)
			change(&copy)
			if copy.Validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	max := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: d.MaxBaseSupplyLots, QuoteAtoms: d.MaxQuoteSupplyAtoms}})
	if err := max.Validate(); err != nil {
		t.Fatal(err)
	}
	zero := d.DefaultConfig([]d.InitialAccount{{AccountID: "a"}})
	if err := zero.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentifierAndCommandValidation(t *testing.T) {
	for _, id := range []string{"a", "Ab-01_", strings.Repeat("a", 64)} {
		if !d.ValidID(id) {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 65), " a", "a/b", "😀"} {
		if d.ValidID(id) {
			t.Fatal(id)
		}
	}
	run := "11111111-1111-4111-8111-111111111111"
	if !d.ValidRunID(run) {
		t.Fatal("valid run")
	}
	for _, id := range []string{"", strings.Replace(run, "4111", "5111", 1), strings.Replace(run, "8111", "7111", 1), strings.Replace(run, "11111111", "AAAAAAAA", 1)} {
		if d.ValidRunID(id) {
			t.Fatal(id)
		}
	}
	c := d.Command{RunID: run, AccountID: "a", Kind: d.PlaceOrder, Place: d.Place{ClientOrderID: "o", Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: d.MaxPriceTicks, QuantityLots: d.MaxQuantityLots}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*d.Command){func(c *d.Command) { c.Place.PriceTicks++ }, func(c *d.Command) { c.Place.QuantityLots = 0 }, func(c *d.Command) { c.Place.Side = "buy" }, func(c *d.Command) { c.Place.Type = "MARKET" }, func(c *d.Command) { c.Place.TimeInForce = "IOC" }, func(c *d.Command) { c.Place.Symbol = "ETH-USDT" }, func(c *d.Command) { c.Cancel.CancelRequestID = "extra" }, func(c *d.Command) { c.Kind = "UNKNOWN" }} {
		copy := c
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid command")
		}
	}
	c = d.Command{RunID: run, AccountID: "a", Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: "c", ClientOrderID: "o"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Place.ClientOrderID = "ignored"
	if c.Validate() == nil {
		t.Fatal("inactive payload accepted")
	}
}
