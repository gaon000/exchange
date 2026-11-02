package bench

import "fmt"

// ValidateDispatch checks diagnostic boundaries against the original attempts.
// Old results without diagnostics remain valid inputs to Summarize unchanged.
func ValidateDispatch(p *Plan, result RunResult) error {
	if p == nil || p.Count() == 0 || !result.DispatchRecorded || len(result.DispatchSamples) != p.Count() || len(result.Samples) != p.Count() || result.Options.Validate() != nil {
		return fmt.Errorf("incomplete dispatch diagnostics")
	}
	for i, d := range result.DispatchSamples {
		s, req := result.Samples[i], p.Request(i)
		if d.Index != i || s.Index != i || s.FinishedAt < 0 || s.FinishedAt > result.Elapsed || d.DispatchedAt < -1 || d.WorkerAt < -1 ||
			d.DispatchedAt > s.FinishedAt || d.WorkerAt > s.FinishedAt ||
			d.DispatchedAt >= 0 && d.DispatchedAt < req.Scheduled ||
			d.WorkerAt >= 0 && (d.DispatchedAt < 0 || d.WorkerAt < d.DispatchedAt) {
			return fmt.Errorf("dispatch timestamps out of order at %d", i)
		}
		if s.Local != "" && (s.StartedAt != -1 || s.WrittenAt != -1 || s.Outcome != (Outcome{})) {
			return fmt.Errorf("unsent diagnostic claims an execution at %d", i)
		}
		switch s.Local {
		case "":
			if d.WorkerAt < 0 || d.WorkerAt != s.StartedAt || d.WorkerAt-req.Scheduled > result.Options.MaxLateness {
				return fmt.Errorf("dispatch does not match execution at %d", i)
			}
		case LocalLate:
			if d.WorkerAt >= 0 {
				if d.WorkerAt-req.Scheduled <= result.Options.MaxLateness || d.DispatchedAt-req.Scheduled > result.Options.MaxLateness {
					return fmt.Errorf("invalid worker lateness at %d", i)
				}
			} else if d.DispatchedAt < 0 || d.DispatchedAt-req.Scheduled <= result.Options.MaxLateness {
				return fmt.Errorf("invalid scheduler lateness at %d", i)
			}
		case LocalCapacity:
			if d.WorkerAt != -1 || d.DispatchedAt < 0 || d.DispatchedAt-req.Scheduled > result.Options.MaxLateness {
				return fmt.Errorf("invalid capacity observation at %d", i)
			}
		case LocalCanceled:
			// Cancellation can be observed before dispatch or by an admitted worker.
		default:
			return fmt.Errorf("unknown local failure at %d", i)
		}
	}
	return nil
}
