package bench

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

const testRun = "11111111-1111-4111-8111-111111111111"

func testSpec() Spec {
	return Spec{Namespace: "case", Seed: 73, Accounts: []string{"alice", "bob", "carol", "dave"}, RejectAccount: "empty",
		Rate: 1000, Warmup: 10 * time.Millisecond, Duration: 100 * time.Millisecond,
		MidPriceTicks: 1000, PriceStepTicks: 5, Depth: 3, QuantityLots: 2, SeedQuantityLots: 100,
		Mix: Mix{Rest: 35, Marketable: 30, Cancel: 15, Duplicate: 15, Reject: 5}}
}

func buildPlan(t *testing.T, s Spec) *Plan {
	t.Helper()
	p, err := Build(testRun, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanDeterminismInputsAndDependencies(t *testing.T) {
	s := testSpec()
	p, again := buildPlan(t, s), buildPlan(t, s)
	if p.Digest() != again.Digest() || len(p.Digest()) != 64 || !reflect.DeepEqual(p, again) {
		t.Fatal("same specification did not create identical inputs")
	}
	if p.Count() != 110 || len(p.Preparation()) != 6 {
		t.Fatal("wrong workload size")
	}
	seen := map[d.Key]Request{}
	places := map[d.OrderRef]bool{}
	intents := map[Intent]int{}
	all := append(p.Preparation(), p.requests...)
	for _, r := range all {
		if err := r.Command.Validate(); err != nil {
			t.Fatalf("invalid generated command: %+v: %v", r, err)
		}
		if len(r.Body) > 4096 || r.OriginScheduled > r.Scheduled {
			t.Fatal("invalid payload/origin", r)
		}
		intents[r.Intent]++
		previous, exists := seen[r.Command.Key()]
		if r.Intent == Duplicate {
			if !exists || previous.Command != r.Command || previous.Body != r.Body || previous.Path != r.Path || previous.OriginPhase != r.OriginPhase || previous.OriginScheduled != r.OriginScheduled {
				t.Fatal("duplicate changed its earlier logical request", r)
			}
			continue
		}
		if exists {
			t.Fatal("new request reused a key")
		}
		seen[r.Command.Key()] = r
		if r.Command.Kind == d.PlaceOrder {
			var payload d.Place
			if err := json.Unmarshal([]byte(r.Body), &payload); err != nil || payload != r.Command.Place || r.Path != "/v1/orders" {
				t.Fatal("body and command differ", err)
			}
			places[d.OrderRef{AccountID: r.Command.AccountID, ClientOrderID: payload.ClientOrderID}] = true
			if r.Intent == Reject && r.Command.AccountID != "empty" {
				t.Fatal("reject probe borrowed a funded account")
			}
		} else {
			var payload d.Cancel
			if err := json.Unmarshal([]byte(r.Body), &payload); err != nil || payload != r.Command.Cancel || r.Path != "/v1/cancellations" {
				t.Fatal("cancel body and command differ", err)
			}
			if !places[d.OrderRef{AccountID: r.Command.AccountID, ClientOrderID: payload.ClientOrderID}] {
				t.Fatal("cancel target was not planned earlier in the same account")
			}
		}
	}
	for _, intent := range []Intent{Rest, Marketable, Cancel, Duplicate, Reject} {
		if intents[intent] == 0 {
			t.Fatal("fixture did not exercise intent", intent)
		}
	}
	if p.UniqueCommands() != len(seen) {
		t.Fatal("command budget does not include preparation or excludes rejections")
	}
	for _, mutate := range []func(*Spec){func(s *Spec) { s.Seed++ }, func(s *Spec) { s.Namespace = "other" }, func(s *Spec) { s.Rate++ }, func(s *Spec) { s.QuantityLots++ }} {
		changed := testSpec()
		mutate(&changed)
		if buildPlan(t, changed).Digest() == p.Digest() {
			t.Fatal("changed input did not change digest")
		}
	}
	otherRun, err := Build("22222222-2222-4222-8222-222222222222", s)
	if err != nil || otherRun.Digest() == p.Digest() {
		t.Fatal("digest failed to bind run identity", err)
	}
}

func TestPlanRationalScheduleAndDetachedAccessors(t *testing.T) {
	s := testSpec()
	s.Rate, s.Warmup, s.Duration, s.Mix = 3, 500*time.Millisecond, time.Second, Mix{Rest: 100}
	p := buildPlan(t, s)
	want := []time.Duration{0, 333333333, 500000000, 833333333, 1166666666}
	if p.Count() != len(want) {
		t.Fatal(p.Count())
	}
	for i, planned := range want {
		r := p.Request(i)
		phase := Measure
		if i < 2 {
			phase = Warmup
		}
		if r.Scheduled != planned || r.Phase != phase || r.Index != i || r.OriginPhase != phase || r.OriginScheduled != planned {
			t.Fatalf("schedule %d: %+v", i, r)
		}
	}
	if offset(3, 3) != time.Second || offset(1_000_000, 3) != 333333333333333*time.Nanosecond {
		t.Fatal("schedule accumulated rounded interval error")
	}
	s.Accounts[0] = "mutated"
	copy := p.Spec()
	copy.Accounts[0] = "mutated-again"
	preparation := p.Preparation()
	preparation[0].Command.AccountID = "wrong"
	r := p.Request(0)
	r.Command.AccountID = "wrong"
	if p.Spec().Accounts[0] != "alice" || p.Preparation()[0].Command.AccountID != "alice" || p.Request(0).Command.AccountID == "wrong" {
		t.Fatal("accessors leaked mutable plan storage")
	}
}

func TestPlanSingleIntentAndInitialDepth(t *testing.T) {
	for _, mix := range []Mix{{Rest: 100}, {Marketable: 100}, {Cancel: 100}, {Duplicate: 100}, {Reject: 100}} {
		s := testSpec()
		s.Warmup, s.HotAccountPercent, s.Mix = 0, 100, mix
		p := buildPlan(t, s)
		for i, r := range p.Preparation() {
			level, side := i/2+1, d.Buy
			price := d.Int(1000 - level*5)
			if i%2 == 1 {
				side, price = d.Sell, d.Int(1000+level*5)
			}
			if r.Command.Place.Side != side || r.Command.Place.PriceTicks != price || r.Command.Place.QuantityLots != 100 || r.Phase != Prepare {
				t.Fatal("initial depth differs from requested levels")
			}
		}
		for _, r := range p.requests {
			if r.Phase != Measure || r.Scheduled >= s.Duration {
				t.Fatal("warmup leaked into no-warmup run")
			}
			if r.Intent == Rest || r.Intent == Marketable {
				if r.Command.AccountID != "alice" {
					t.Fatal("100% new-order concentration did not use first account")
				}
			}
			if r.Intent == Marketable {
				price := d.Int(1020)
				if r.Command.Place.Side == d.Sell {
					price = 980
				}
				if r.Command.Place.PriceTicks != price {
					t.Fatal("marketable intent did not cross initial depth")
				}
			}
			if r.Intent == Duplicate && r.OriginPhase != Prepare {
				t.Fatal("duplicate-only plan invented new logical commands")
			}
		}
	}
}

func TestPlanRejectsInvalidAndOversizedSpecifications(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"namespace": func(s *Spec) { s.Namespace = strings.Repeat("x", 33) }, "seed": func(s *Spec) { s.Seed = -1 },
		"one account": func(s *Spec) { s.Accounts = s.Accounts[:1] }, "duplicate account": func(s *Spec) { s.Accounts[1] = s.Accounts[0] },
		"reject account": func(s *Spec) { s.RejectAccount = "alice" }, "missing reject account": func(s *Spec) { s.RejectAccount = "" },
		"rate": func(s *Spec) { s.Rate = 0 }, "duration": func(s *Spec) { s.Duration = 0 }, "negative warmup": func(s *Spec) { s.Warmup = -1 },
		"huge": func(s *Spec) { s.Rate, s.Duration = 1_000_000, time.Hour }, "sum duration": func(s *Spec) { s.Warmup, s.Duration = time.Hour, time.Hour },
		"mix": func(s *Spec) { s.Mix.Rest++ }, "negative weight": func(s *Spec) { s.Mix.Rest = -1 }, "hot": func(s *Spec) { s.HotAccountPercent = 101 },
		"depth": func(s *Spec) { s.Depth = 101 }, "price low": func(s *Spec) { s.MidPriceTicks = 1 }, "price high": func(s *Spec) { s.MidPriceTicks = d.MaxPriceTicks },
		"step": func(s *Spec) { s.PriceStepTicks = 0 }, "quantity": func(s *Spec) { s.QuantityLots = d.MaxQuantityLots + 1 }, "seed quantity": func(s *Spec) { s.SeedQuantityLots = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			s := testSpec()
			mutate(&s)
			if _, err := Build(testRun, s); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	if _, err := Build("not-run", testSpec()); err == nil {
		t.Fatal("invalid run accepted")
	}
	s := testSpec()
	s.Warmup, s.Duration, s.Rate = 0, time.Second, MaxAttempts
	if err := s.Validate(); err != nil {
		t.Fatal("exact attempt limit rejected", err)
	}
	s.Warmup = time.Nanosecond
	if err := s.Validate(); err == nil {
		t.Fatal("attempt limit ignored partial warmup interval")
	}
}
