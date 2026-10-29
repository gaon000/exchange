package bench

import (
	"fmt"
	"sort"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

// Distribution reports exact nearest-rank quantiles over retained samples.
// Count=0 means no observations; its zero durations are not measured latency.
type Distribution struct {
	Count int           `json:"count"`
	Min   time.Duration `json:"minNanos"`
	P50   time.Duration `json:"p50Nanos"`
	P95   time.Duration `json:"p95Nanos"`
	P99   time.Duration `json:"p99Nanos"`
	Max   time.Duration `json:"maxNanos"`
}

func distribution(values []time.Duration) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(percent int) time.Duration { return values[(len(values)*percent+99)/100-1] }
	return Distribution{len(values), values[0], percentile(50), percentile(95), percentile(99), values[len(values)-1]}
}

// PhaseSummary distinguishes attempt cohorts from unique logical commands.
// Unique* and trades belong to the original command's phase, so retrying warmup
// traffic during measurement cannot inflate measured new-order throughput.
type PhaseSummary struct {
	Phase                          Phase                          `json:"phase"`
	Duration                       time.Duration                  `json:"durationNanos"`
	Scheduled                      int                            `json:"scheduled"`
	Started                        int                            `json:"started"`
	LocalFailures                  map[LocalFailure]int           `json:"localFailures"`
	Intents                        map[Intent]int                 `json:"intents"`
	Responses                      map[ResponseClass]int          `json:"responses"`
	Codes                          map[string]int                 `json:"codes"`
	RepeatedResults                int                            `json:"repeatedResults"`
	UniqueBusinessResults          int                            `json:"uniqueBusinessResults"`
	UniqueAcceptedOrders           int                            `json:"uniqueAcceptedOrders"`
	UniqueMatchedOrders            int                            `json:"uniqueMatchedOrders"`
	UniqueSuccessfulCancels        int                            `json:"uniqueSuccessfulCancels"`
	UniqueBusinessRejections       int                            `json:"uniqueBusinessRejections"`
	UniqueTrades                   d.Int                          `json:"uniqueTrades"`
	UnknownLogicalCommands         int                            `json:"unknownLogicalCommands"`
	NewOrdersFinishedWithinWindow  int                            `json:"newOrdersFinishedWithinWindow"`
	CancelsFinishedWithinWindow    int                            `json:"cancelsFinishedWithinWindow"`
	TradesFinishedWithinWindow     d.Int                          `json:"tradesFinishedWithinWindow"`
	NewOrdersPerSecond             float64                        `json:"newOrdersPerSecond"`
	CancelsPerSecond               float64                        `json:"cancelsPerSecond"`
	TradesPerSecond                float64                        `json:"tradesPerSecond"`
	GeneratorDropRate              float64                        `json:"generatorDropRateOfScheduled"`
	TimeoutRate                    float64                        `json:"timeoutRateOfStarted"`
	OverloadRate                   float64                        `json:"overloadRateOfStarted"`
	SchedulerDelay                 Distribution                   `json:"schedulerDelay"`
	RequestLatency                 Distribution                   `json:"requestLatency"`
	ScheduledAttemptLatency        Distribution                   `json:"scheduledAttemptLatency"`
	TransportWriteDelay            Distribution                   `json:"transportWriteDelay"`
	UniqueNewOrderScheduledLatency Distribution                   `json:"uniqueNewOrderScheduledLatency"`
	RequestLatencyByIntent         map[Intent]Distribution        `json:"requestLatencyByIntent"`
	RequestLatencyByClass          map[ResponseClass]Distribution `json:"requestLatencyByClass"`
}

// Summary reports generator validity separately from later performance gates.
// Timeouts/overload remain legitimate measured failures; generator misses,
// malformed responses or aborted runs make MeasurementValid false. This flag
// never claims a p99/throughput objective has passed.
type Summary struct {
	Version          int          `json:"version"`
	PlanDigest       string       `json:"planDigest"`
	MeasurementValid bool         `json:"measurementValid"`
	InvalidReasons   []string     `json:"invalidReasons"`
	Warmup           PhaseSummary `json:"warmup"`
	Measurement      PhaseSummary `json:"measurement"`
}

type phaseCollector struct {
	summary                                PhaseSummary
	scheduler, request, scheduled, written []time.Duration
	newOrders                              []time.Duration
	byIntent                               map[Intent][]time.Duration
	byClass                                map[ResponseClass][]time.Duration
}

type logicalResult struct {
	request Request
	first   *Sample
	unknown bool
}

func newCollector(phase Phase, duration time.Duration) *phaseCollector {
	return &phaseCollector{summary: PhaseSummary{Phase: phase, Duration: duration, LocalFailures: map[LocalFailure]int{}, Intents: map[Intent]int{}, Responses: map[ResponseClass]int{}, Codes: map[string]int{}}, byIntent: map[Intent][]time.Duration{}, byClass: map[ResponseClass][]time.Duration{}}
}

// Summarize accounts for every planned request and counts each durable key once.
// It rejects inconsistent result identities/sequences/timestamps instead of
// quietly presenting a partial or inflated performance report. Raw samples stay
// owned by the caller even when this validation fails.
func Summarize(p *Plan, result RunResult) (Summary, error) {
	if p == nil || p.digest == "" || len(p.requests) == 0 || len(result.Samples) != p.Count() || result.Elapsed < 0 {
		return Summary{}, fmt.Errorf("missing plan, samples or valid elapsed time")
	}
	if !result.Aborted && result.Elapsed < p.spec.Warmup+p.spec.Duration {
		return Summary{}, fmt.Errorf("observation window ended early without an abort")
	}
	warmup, measured := newCollector(Warmup, p.spec.Warmup), newCollector(Measure, p.spec.Duration)
	collector := func(phase Phase) *phaseCollector {
		if phase == Warmup {
			return warmup
		}
		if phase == Measure {
			return measured
		}
		return nil
	}
	logical := make(map[d.Key]*logicalResult)
	sequences := make(map[d.Int]d.Key)
	for i := range result.Samples {
		sample, req := &result.Samples[i], p.requests[i]
		if err := validateSample(req, *sample, result.Elapsed); err != nil {
			return Summary{}, fmt.Errorf("sample %d: %w", i, err)
		}
		c := collector(req.Phase)
		c.summary.Scheduled++
		c.summary.Intents[req.Intent]++
		key := req.Command.Key()
		state := logical[key]
		if state == nil {
			state = &logicalResult{request: req}
			logical[key] = state
		}
		if sample.Local != "" {
			c.summary.LocalFailures[sample.Local]++
			if sample.Local != LocalCanceled {
				c.scheduler = append(c.scheduler, sample.FinishedAt-req.Scheduled)
			}
			continue
		}
		c.summary.Started++
		c.summary.Responses[sample.Outcome.Class]++
		c.summary.Codes[sample.Outcome.Code]++
		c.scheduler = append(c.scheduler, sample.StartedAt-req.Scheduled)
		c.request = append(c.request, sample.FinishedAt-sample.StartedAt)
		c.byIntent[req.Intent] = append(c.byIntent[req.Intent], sample.FinishedAt-sample.StartedAt)
		c.byClass[sample.Outcome.Class] = append(c.byClass[sample.Outcome.Class], sample.FinishedAt-sample.StartedAt)
		c.scheduled = append(c.scheduled, sample.FinishedAt-req.Scheduled)
		if sample.WrittenAt >= 0 {
			c.written = append(c.written, sample.WrittenAt-sample.StartedAt)
		}
		if sample.Outcome.Class != Business {
			state.unknown = state.unknown || sample.Outcome.Certainty == "UNKNOWN"
			continue
		}
		if other, found := sequences[sample.Outcome.CommandSeq]; found && other != key {
			return Summary{}, fmt.Errorf("command sequence belongs to multiple request keys")
		}
		sequences[sample.Outcome.CommandSeq] = key
		if previous := state.first; previous != nil {
			if previous.Outcome != sample.Outcome {
				return Summary{}, fmt.Errorf("same key returned different immutable business results")
			}
			if previous.FinishedAt <= sample.FinishedAt {
				continue
			}
		}
		state.first = sample
	}
	// Count repeats by completion time, not planned order. A retry can finish
	// before the original response while both refer to one durable command.
	for i, sample := range result.Samples {
		if sample.Local == "" && sample.Outcome.Class == Business && logical[p.requests[i].Command.Key()].first.Index != i {
			collector(p.requests[i].Phase).summary.RepeatedResults++
		}
	}
	for _, state := range logical {
		c := collector(state.request.OriginPhase)
		if c == nil { // preparation results never become measured logical commands
			continue
		}
		if state.first == nil {
			if state.unknown {
				c.summary.UnknownLogicalCommands++
			}
			continue
		}
		sample := state.first
		c.summary.UniqueBusinessResults++
		c.summary.UniqueTrades += sample.Outcome.TradeCount
		start, end := time.Duration(0), p.spec.Warmup
		if state.request.OriginPhase == Measure {
			start, end = p.spec.Warmup, p.spec.Warmup+p.spec.Duration
		}
		within := sample.FinishedAt >= start && sample.FinishedAt < end
		if within {
			c.summary.TradesFinishedWithinWindow += sample.Outcome.TradeCount
		}
		if state.request.Command.Kind == d.PlaceOrder && sample.Outcome.Code == string(d.Accepted) {
			c.summary.UniqueAcceptedOrders++
			if sample.Outcome.TradeCount > 0 {
				c.summary.UniqueMatchedOrders++
			}
			c.newOrders = append(c.newOrders, sample.FinishedAt-state.request.OriginScheduled)
			if within {
				c.summary.NewOrdersFinishedWithinWindow++
			}
		}
		if sample.Outcome.Code == string(d.CancelSucceeded) {
			c.summary.UniqueSuccessfulCancels++
			if within {
				c.summary.CancelsFinishedWithinWindow++
			}
		}
		if sample.Outcome.Code == string(d.InsufficientBalance) {
			c.summary.UniqueBusinessRejections++
		}
	}
	finish := func(c *phaseCollector) PhaseSummary {
		s := &c.summary
		if s.Duration > 0 {
			s.NewOrdersPerSecond = float64(s.NewOrdersFinishedWithinWindow) / s.Duration.Seconds()
			s.CancelsPerSecond = float64(s.CancelsFinishedWithinWindow) / s.Duration.Seconds()
			s.TradesPerSecond = float64(s.TradesFinishedWithinWindow) / s.Duration.Seconds()
		}
		if s.Scheduled > 0 {
			drops := s.LocalFailures[LocalCapacity] + s.LocalFailures[LocalLate] + s.LocalFailures[LocalCanceled]
			s.GeneratorDropRate = float64(drops) / float64(s.Scheduled)
		}
		if s.Started > 0 {
			s.TimeoutRate = float64(s.Responses[TransportTimeout]+s.Responses[ServerTimeout]) / float64(s.Started)
			s.OverloadRate = float64(s.Responses[Overload]) / float64(s.Started)
		}
		s.SchedulerDelay, s.RequestLatency = distribution(c.scheduler), distribution(c.request)
		s.ScheduledAttemptLatency, s.TransportWriteDelay = distribution(c.scheduled), distribution(c.written)
		s.UniqueNewOrderScheduledLatency = distribution(c.newOrders)
		s.RequestLatencyByIntent, s.RequestLatencyByClass = map[Intent]Distribution{}, map[ResponseClass]Distribution{}
		for intent, values := range c.byIntent {
			s.RequestLatencyByIntent[intent] = distribution(values)
		}
		for class, values := range c.byClass {
			s.RequestLatencyByClass[class] = distribution(values)
		}
		return *s
	}
	summary := Summary{Version: Version, PlanDigest: p.digest, MeasurementValid: true, InvalidReasons: []string{}, Warmup: finish(warmup), Measurement: finish(measured)}
	if result.Aborted {
		summary.InvalidReasons = append(summary.InvalidReasons, "RUN_ABORTED")
	}
	if summary.Warmup.GeneratorDropRate > 0 || summary.Measurement.GeneratorDropRate > 0 {
		summary.InvalidReasons = append(summary.InvalidReasons, "GENERATOR_MISSED_REQUESTS")
	}
	if warmup.summary.Responses[InvalidResponse]+measured.summary.Responses[InvalidResponse] > 0 {
		summary.InvalidReasons = append(summary.InvalidReasons, "INVALID_RESPONSES")
	}
	summary.MeasurementValid = len(summary.InvalidReasons) == 0
	return summary, nil
}

func validateSample(req Request, s Sample, elapsed time.Duration) error {
	if s.Index != req.Index || s.FinishedAt < 0 || s.FinishedAt > elapsed {
		return fmt.Errorf("missing, misindexed or out-of-window observation")
	}
	if s.Local != "" {
		if s.Local != LocalCapacity && s.Local != LocalLate && s.Local != LocalCanceled || s.StartedAt != -1 || s.WrittenAt != -1 || s.Outcome != (Outcome{}) || s.Local != LocalCanceled && s.FinishedAt < req.Scheduled {
			return fmt.Errorf("invalid unsent request observation")
		}
		return nil
	}
	if s.StartedAt < req.Scheduled || s.FinishedAt < s.StartedAt || s.WrittenAt != -1 && (s.WrittenAt < s.StartedAt || s.WrittenAt > s.FinishedAt) {
		return fmt.Errorf("execution timestamps violate planned/start/write/finish order")
	}
	o := s.Outcome
	if o.HTTPStatus != 0 && (o.HTTPStatus < 100 || o.HTTPStatus > 599) {
		return fmt.Errorf("invalid HTTP status")
	}
	if o.Class == Business {
		if o.HTTPStatus != 200 || o.Certainty != "COMPLETED" || o.CommandSeq < 1 || o.TradeCount < 0 || len(o.Fingerprint) != 64 || o.Detail != "" {
			return fmt.Errorf("invalid completed business result")
		}
		for _, c := range o.Fingerprint {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return fmt.Errorf("invalid business fingerprint")
			}
		}
		if req.Command.Kind == d.PlaceOrder {
			if o.TradeCount > req.Command.Place.QuantityLots || !(o.Code == string(d.Accepted) && (o.OrderStatus == d.Open || o.OrderStatus == d.PartiallyFilled || o.OrderStatus == d.Filled || o.OrderStatus == d.Cancelled) || o.Code == string(d.InsufficientBalance) && o.OrderStatus == d.Rejected && o.TradeCount == 0) {
				return fmt.Errorf("invalid place result code/status")
			}
		} else if o.TradeCount != 0 || !(o.Code == string(d.CancelSucceeded) && o.OrderStatus == d.Cancelled || o.Code == string(d.AlreadyCancelled) && o.OrderStatus == d.Cancelled || o.Code == string(d.AlreadyFilled) && o.OrderStatus == d.Filled || o.Code == string(d.OrderNotFound) && o.OrderStatus == "") {
			return fmt.Errorf("invalid cancel result code/status")
		}
		return nil
	}
	if o.CommandSeq != 0 || o.TradeCount != 0 || o.Fingerprint != "" || o.OrderStatus != "" || o.Certainty != "UNKNOWN" && o.Certainty != "NOT_ADMITTED" {
		return fmt.Errorf("non-business response claims durable result fields")
	}
	switch o.Class {
	case Overload:
		if o.HTTPStatus != 429 || o.Certainty != "NOT_ADMITTED" {
			return fmt.Errorf("invalid overload response")
		}
	case ServerTimeout:
		if o.HTTPStatus != 504 || o.Certainty != "UNKNOWN" {
			return fmt.Errorf("invalid server timeout response")
		}
	case TransportTimeout, TransportFailure, InvalidResponse:
		if o.Certainty != "UNKNOWN" {
			return fmt.Errorf("transport/malformed response cannot prove non-admission")
		}
	case ServerFailure:
		if o.HTTPStatus < 400 || o.HTTPStatus > 599 || o.HTTPStatus == 429 || o.HTTPStatus == 504 {
			return fmt.Errorf("invalid server error status")
		}
	default:
		return fmt.Errorf("missing or unknown response class")
	}
	return nil
}
