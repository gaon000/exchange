package bench

import (
	"fmt"
	"time"
)

// WindowSummary groups attempts by their scheduled measurement time, including
// responses observed after that window. It does not turn duplicates into orders.
type WindowSummary struct {
	Start                   time.Duration `json:"startNanos"`
	End                     time.Duration `json:"endNanos"`
	Scheduled               int           `json:"scheduled"`
	Started                 int           `json:"started"`
	LocalFailures           int           `json:"localFailures"`
	OperationalFailures     int           `json:"operationalFailures"`
	RequestLatency          Distribution  `json:"requestLatency"`
	ScheduledAttemptLatency Distribution  `json:"scheduledAttemptLatency"`
}

// SummarizeWindows checks each observation and reports exact attempt latency
// quantiles in equal measurement windows. Call Summarize as well to validate
// cross-request identities and to measure unique business results and throughput.
func SummarizeWindows(p *Plan, result RunResult, width time.Duration) ([]WindowSummary, error) {
	if p == nil || p.digest == "" || len(result.Samples) != p.Count() || width <= 0 || p.spec.Duration%width != 0 || p.spec.Duration/width > 1000 {
		return nil, fmt.Errorf("valid plan/samples and at most 1000 equal windows required")
	}
	n := int(p.spec.Duration / width)
	windows := make([]WindowSummary, n)
	request, scheduled := make([][]time.Duration, n), make([][]time.Duration, n)
	for i := range windows {
		windows[i].Start = p.spec.Warmup + time.Duration(i)*width
		windows[i].End = windows[i].Start + width
	}
	for i, s := range result.Samples {
		req := p.requests[i]
		if err := validateSample(req, s, result.Elapsed); err != nil {
			return nil, fmt.Errorf("sample %d: %w", i, err)
		}
		if req.Phase != Measure {
			continue
		}
		bin := int((req.Scheduled - p.spec.Warmup) / width)
		w := &windows[bin]
		w.Scheduled++
		if s.Local != "" {
			w.LocalFailures++
			continue
		}
		w.Started++
		if s.Outcome.Class != Business {
			w.OperationalFailures++
		}
		request[bin] = append(request[bin], s.FinishedAt-s.StartedAt)
		scheduled[bin] = append(scheduled[bin], s.FinishedAt-req.Scheduled)
	}
	for i := range windows {
		windows[i].RequestLatency = distribution(request[i])
		windows[i].ScheduledAttemptLatency = distribution(scheduled[i])
	}
	return windows, nil
}
