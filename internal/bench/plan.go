// Package bench builds reproducible workloads and measures scheduled requests.
// It never calls the matching engine or changes a server's persisted Genesis.
package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

// Version identifies the workload and artifact interpretation, independently of
// the exchange's wire/rules versions. MaxAttempts bounds retained plans/results.
const (
	Version     = 1
	MaxAttempts = 1_000_000
)

// Phase separates preparation, warmup and the measured request cohort.
type Phase string

const (
	Prepare Phase = "PREPARE"
	Warmup  Phase = "WARMUP"
	Measure Phase = "MEASURE"
)

// Intent labels generated input; actual fills/rejections come from responses.
type Intent string

const (
	Rest       Intent = "REST"
	Marketable Intent = "MARKETABLE"
	Cancel     Intent = "CANCEL"
	Duplicate  Intent = "DUPLICATE"
	Reject     Intent = "REJECT_PROBE"
)

// Mix contains integer selection weights summing to 100, not measured outcomes.
type Mix struct {
	Rest       int `json:"rest"`
	Marketable int `json:"marketable"`
	Cancel     int `json:"cancel"`
	Duplicate  int `json:"duplicate"`
	Reject     int `json:"rejectProbe"`
}

// Spec fully determines the planned inputs with a run identity and Version.
// Duration fields are nanoseconds in JSON (bounded to one hour in total).
// HotAccountPercent biases new orders toward Accounts[0]; cancels/duplicates
// inherit their target account. RejectAccount must be a separate zero-funded
// registered account; preparation of its Genesis is outside this package.
type Spec struct {
	Namespace         string        `json:"namespace"`
	Seed              int64         `json:"seed"`
	Accounts          []string      `json:"accounts"`
	RejectAccount     string        `json:"rejectAccount"`
	Rate              int           `json:"requestsPerSecond"`
	Warmup            time.Duration `json:"warmupNanos"`
	Duration          time.Duration `json:"durationNanos"`
	HotAccountPercent int           `json:"hotAccountPercent"`
	MidPriceTicks     d.Int         `json:"midPriceTicks"`
	PriceStepTicks    d.Int         `json:"priceStepTicks"`
	Depth             int           `json:"depth"`
	QuantityLots      d.Int         `json:"quantityLots"`
	SeedQuantityLots  d.Int         `json:"seedQuantityLots"`
	Mix               Mix           `json:"mix"`
}

// Validate rejects ambiguous accounts, invalid prices and unbounded workloads.
func (s Spec) Validate() error {
	if !d.ValidID(s.Namespace) || len(s.Namespace) > 32 || s.Seed < 0 || s.Seed > 1<<31-1 || len(s.Accounts) < 2 || len(s.Accounts) > 10000 {
		return fmt.Errorf("invalid namespace, seed or account count")
	}
	seen := make(map[string]bool, len(s.Accounts))
	for _, account := range s.Accounts {
		if !d.ValidID(account) || seen[account] {
			return fmt.Errorf("invalid or duplicate trading account")
		}
		seen[account] = true
	}
	if s.Mix.Reject > 0 && (!d.ValidID(s.RejectAccount) || seen[s.RejectAccount]) || s.RejectAccount != "" && !d.ValidID(s.RejectAccount) {
		return fmt.Errorf("reject probe requires a separate registered zero-funded account")
	}
	weights := []int{s.Mix.Rest, s.Mix.Marketable, s.Mix.Cancel, s.Mix.Duplicate, s.Mix.Reject}
	total := 0
	for _, weight := range weights {
		if weight < 0 || weight > 100 {
			return fmt.Errorf("mix weights must be between 0 and 100")
		}
		total += weight
	}
	if total != 100 || s.HotAccountPercent < 0 || s.HotAccountPercent > 100 {
		return fmt.Errorf("mix must sum to 100 and account bias must be in range")
	}
	if s.Rate < 1 || s.Rate > 1_000_000 || s.Warmup < 0 || s.Warmup > time.Hour || s.Duration <= 0 || s.Duration > time.Hour || s.Warmup > time.Hour-s.Duration {
		return fmt.Errorf("rate or duration outside workload bounds")
	}
	if count(s.Rate, s.Warmup)+count(s.Rate, s.Duration) > MaxAttempts {
		return fmt.Errorf("workload exceeds %d scheduled attempts", MaxAttempts)
	}
	if s.Depth < 1 || s.Depth > 100 || s.MidPriceTicks < 1 || s.MidPriceTicks > d.MaxPriceTicks || s.PriceStepTicks < 1 || s.PriceStepTicks > d.MaxPriceTicks || s.QuantityLots < 1 || s.QuantityLots > d.MaxQuantityLots || s.SeedQuantityLots < 1 || s.SeedQuantityLots > d.MaxQuantityLots {
		return fmt.Errorf("price, depth or quantity outside domain bounds")
	}
	span := d.Int(s.Depth+1) * s.PriceStepTicks // validated factors cannot overflow
	if s.MidPriceTicks-span < 1 || s.MidPriceTicks+span > d.MaxPriceTicks {
		return fmt.Errorf("price range crosses the domain boundary")
	}
	return nil
}

func count(rate int, duration time.Duration) int {
	whole, remainder := duration/time.Second, duration%time.Second
	n := int64(whole)*int64(rate) + int64((remainder*time.Duration(rate)+time.Second-1)/time.Second)
	if n > MaxAttempts {
		return MaxAttempts + 1 // reject before converting a potentially huge count
	}
	return int(n)
}

// offset uses rational positions, avoiding accumulated interval truncation.
func offset(index, rate int) time.Duration {
	return time.Duration(index/rate)*time.Second + time.Duration(index%rate)*time.Second/time.Duration(rate)
}

// Request contains an immutable command and its already encoded HTTP body.
// Origin identifies the original logical command even if this is a duplicate
// scheduled in a different phase. Preparation has Scheduled = -1.
type Request struct {
	Index           int           `json:"index"`
	Phase           Phase         `json:"phase"`
	Intent          Intent        `json:"intent"`
	Scheduled       time.Duration `json:"scheduledNanos"`
	OriginPhase     Phase         `json:"originPhase"`
	OriginScheduled time.Duration `json:"originScheduledNanos"`
	Path            string        `json:"path"`
	Body            string        `json:"body"`
	Command         d.Command     `json:"command"`
}

// Plan owns immutable prepared requests. Accessors return copies, including the
// account slice, so callers cannot accidentally change a running experiment.
type Plan struct {
	spec        Spec
	runID       string
	preparation []Request
	requests    []Request
	digest      string
	unique      int
}

// Spec returns a detached workload configuration.
func (p *Plan) Spec() Spec { s := p.spec; s.Accounts = append([]string(nil), s.Accounts...); return s }

// RunID returns the required exchange run identity.
func (p *Plan) RunID() string { return p.runID }

// Count returns the number of scheduled warmup and measured attempts.
func (p *Plan) Count() int { return len(p.requests) }

// UniqueCommands includes preparation and nonduplicate scheduled commands. The
// caller must provision enough Genesis command capacity before running the plan.
func (p *Plan) UniqueCommands() int { return p.unique }

// Request returns one scheduled request by its zero-based index.
func (p *Plan) Request(index int) Request { return p.requests[index] }

// Preparation returns detached requests to execute before starting the clock.
func (p *Plan) Preparation() []Request { return append([]Request(nil), p.preparation...) }

// Digest returns a SHA-256 binding the version, run, specification and inputs.
func (p *Plan) Digest() string { return p.digest }

// Build creates deterministic inputs without opening files, sockets or a run.
// Duplicates/cancels refer to earlier planned inputs; no response feedback changes
// the schedule. Consequently a cancel may race its target or find it filled.
func Build(runID string, s Spec) (*Plan, error) {
	if !d.ValidRunID(runID) {
		return nil, fmt.Errorf("invalid run identity")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s.Accounts = append([]string(nil), s.Accounts...)
	p := &Plan{spec: s, runID: runID, preparation: make([]Request, 0, s.Depth*2), requests: make([]Request, 0, count(s.Rate, s.Warmup)+count(s.Rate, s.Duration))}
	// References avoid retaining multiple full command copies while planning.
	var places, originals []int
	lookup := func(index int) Request {
		if index < 0 {
			return p.preparation[-index-1]
		}
		return p.requests[index]
	}
	makePlace := func(id, account string, side d.Side, price, qty d.Int) d.Command {
		return d.Command{RunID: runID, AccountID: account, Kind: d.PlaceOrder, Place: d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}}
	}
	for level := 0; level < s.Depth; level++ {
		for sideIndex, side := range []d.Side{d.Buy, d.Sell} {
			price := s.MidPriceTicks - d.Int(level+1)*s.PriceStepTicks
			if side == d.Sell {
				price = s.MidPriceTicks + d.Int(level+1)*s.PriceStepTicks
			}
			account := s.Accounts[(level+sideIndex*(len(s.Accounts)/2))%len(s.Accounts)]
			c := makePlace(fmt.Sprintf("%s-s-%d", s.Namespace, len(p.preparation)), account, side, price, s.SeedQuantityLots)
			r := request(c, Prepare, Rest, len(p.preparation), -1)
			p.preparation = append(p.preparation, r)
			ref := -len(p.preparation)
			places, originals = append(places, ref), append(originals, ref)
		}
	}
	rng := rand.New(rand.NewSource(s.Seed))
	for _, phase := range []Phase{Warmup, Measure} {
		duration, start := s.Warmup, time.Duration(0)
		if phase == Measure {
			duration, start = s.Duration, s.Warmup
		}
		for i, n := 0, count(s.Rate, duration); i < n; i++ {
			index, planned := len(p.requests), start+offset(i, s.Rate)
			intent := chooseIntent(rng.Intn(100), s.Mix)
			if intent == Duplicate {
				r := lookup(originals[rng.Intn(len(originals))])
				r.Index, r.Phase, r.Intent, r.Scheduled = index, phase, Duplicate, planned
				p.requests = append(p.requests, r)
				continue
			}
			var c d.Command
			if intent == Cancel {
				target := lookup(places[rng.Intn(len(places))]).Command
				c = d.Command{RunID: runID, AccountID: target.AccountID, Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: fmt.Sprintf("%s-c-%d", s.Namespace, index), ClientOrderID: target.Place.ClientOrderID}}
			} else {
				account := s.Accounts[0]
				if rng.Intn(100) >= s.HotAccountPercent {
					account = s.Accounts[rng.Intn(len(s.Accounts))]
				}
				side := []d.Side{d.Buy, d.Sell}[rng.Intn(2)]
				level := 1 + rng.Intn(s.Depth)
				price := s.MidPriceTicks - d.Int(level)*s.PriceStepTicks
				if side == d.Sell {
					price = s.MidPriceTicks + d.Int(level)*s.PriceStepTicks
				}
				if intent == Marketable {
					price = s.MidPriceTicks + d.Int(s.Depth+1)*s.PriceStepTicks
					if side == d.Sell {
						price = s.MidPriceTicks - d.Int(s.Depth+1)*s.PriceStepTicks
					}
				}
				if intent == Reject {
					account, side, price = s.RejectAccount, d.Buy, d.MaxPriceTicks
				}
				c = makePlace(fmt.Sprintf("%s-o-%d", s.Namespace, index), account, side, price, s.QuantityLots)
			}
			r := request(c, phase, intent, index, planned)
			p.requests, originals = append(p.requests, r), append(originals, index)
			if c.Kind == d.PlaceOrder && intent != Reject {
				places = append(places, index)
			}
		}
	}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	_ = encoder.Encode(struct {
		Version int
		RunID   string
		Spec    Spec
	}{Version, runID, s})
	for _, collection := range [][]Request{p.preparation, p.requests} {
		for _, r := range collection {
			_ = encoder.Encode(r) // fixed structs contain no failing JSON values
		}
	}
	p.digest = hex.EncodeToString(hash.Sum(nil))
	p.unique = len(originals)
	return p, nil
}

func request(c d.Command, phase Phase, intent Intent, index int, planned time.Duration) Request {
	path := "/v1/orders"
	var body []byte
	if c.Kind == d.CancelOrder {
		path = "/v1/cancellations"
		body, _ = json.Marshal(c.Cancel)
	} else {
		body, _ = json.Marshal(c.Place)
	}
	return Request{index, phase, intent, planned, phase, planned, path, string(body), c}
}

func chooseIntent(n int, mix Mix) Intent {
	for _, item := range []struct {
		intent Intent
		weight int
	}{{Rest, mix.Rest}, {Marketable, mix.Marketable}, {Cancel, mix.Cancel}, {Duplicate, mix.Duplicate}, {Reject, mix.Reject}} {
		if n < item.weight {
			return item.intent
		}
		n -= item.weight
	}
	panic("validated mix did not select an intent")
}
