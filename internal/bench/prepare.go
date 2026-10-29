package bench

import (
	"context"
	"fmt"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

// InitialBalance is a verified public balance view before preparation. AccountID
// is the request context; the wire response does not repeat the account ID.
type InitialBalance struct {
	AccountID  string `json:"accountId"`
	BaseLots   d.Int  `json:"baseLots"`
	QuoteAtoms d.Int  `json:"quoteAtoms"`
}

// PreparationSample preserves every attempted seed order, including failures.
type PreparationSample struct {
	Index       int       `json:"index"`
	StartedUTC  time.Time `json:"startedUTC"`
	FinishedUTC time.Time `json:"finishedUTC"`
	Outcome     Outcome   `json:"outcome"`
}

// PreparationReport is untimed evidence; failed preparation never starts Run.
type PreparationReport struct {
	StartedUTC  time.Time           `json:"startedUTC"`
	FinishedUTC time.Time           `json:"finishedUTC"`
	Balances    []InitialBalance    `json:"balances"`
	Samples     []PreparationSample `json:"samples"`
	Complete    bool                `json:"complete"`
}

type balanceWire struct {
	RunID   string `json:"runId"`
	AsOfSeq d.Int  `json:"asOfSeq"`
	BTC     struct {
		Available d.Int `json:"availableLots"`
		Reserved  d.Int `json:"reservedLots"`
	} `json:"BTC"`
	USDT struct {
		Available d.Int `json:"availableAtoms"`
		Reserved  d.Int `json:"reservedAtoms"`
	} `json:"USDT"`
}

// ValidatePreparation checks configuration and seed funding without any writes.
// The caller must bind this config to the server's verified config hash.
func ValidatePreparation(p *Plan, config d.Config) error {
	if p == nil || p.digest == "" {
		return fmt.Errorf("built plan required")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if d.Int(p.UniqueCommands()) > config.MaxCommands {
		return fmt.Errorf("plan exceeds run command capacity")
	}
	accounts := map[string]d.InitialAccount{}
	for _, a := range config.Accounts {
		accounts[a.AccountID] = a
	}
	for _, id := range p.spec.Accounts {
		if _, ok := accounts[id]; !ok {
			return fmt.Errorf("unregistered trading account %s", id)
		}
	}
	if p.spec.Mix.Reject > 0 {
		a, ok := accounts[p.spec.RejectAccount]
		if !ok || a.BaseLots != 0 || a.QuoteAtoms != 0 {
			return fmt.Errorf("reject probe account must be registered with zero initial supply")
		}
	}
	for _, req := range p.preparation {
		a, c := accounts[req.Command.AccountID], req.Command.Place
		if c.Side == d.Buy {
			a.QuoteAtoms -= c.PriceTicks * c.QuantityLots
		} else {
			a.BaseLots -= c.QuantityLots
		}
		if a.QuoteAtoms < 0 || a.BaseLots < 0 {
			return fmt.Errorf("insufficient seed funding for %s", a.AccountID)
		}
		accounts[a.AccountID] = a
	}
	return nil
}

func (e *HTTPExecutor) balance(ctx context.Context, run, account string) (balanceWire, error) {
	w := e.call(ctx, "GET", "/v1/balances", run, account, "")
	var b balanceWire
	if w.err != nil || w.invalid != "" || w.status != 200 {
		return b, fmt.Errorf("balance query failed for %s (HTTP %d)", account, w.status)
	}
	if err := strictjson.Decode(w.body, &b); err != nil || b.RunID != run || b.AsOfSeq < 0 || b.BTC.Available < 0 || b.BTC.Reserved < 0 || b.USDT.Available < 0 || b.USDT.Reserved < 0 {
		return b, fmt.Errorf("invalid balance response for %s", account)
	}
	return b, nil
}

// Prepare verifies pristine public state and executes seed orders sequentially.
// It never initializes/restarts a server or retries a failed command. A failure
// preserves its partial report and the run's existing files/orders. Use a fresh
// run for a new experiment, with no other client writing concurrently.
func (e *HTTPExecutor) Prepare(ctx context.Context, p *Plan, config d.Config, timeout time.Duration) (report PreparationReport, err error) {
	report = PreparationReport{StartedUTC: time.Now().UTC(), Balances: []InitialBalance{}, Samples: []PreparationSample{}}
	defer func() { report.FinishedUTC = time.Now().UTC() }()
	if timeout <= 0 || timeout > time.Minute {
		return report, fmt.Errorf("invalid preparation timeout")
	}
	if err := ValidatePreparation(p, config); err != nil {
		return report, err
	}
	for _, account := range config.Accounts {
		call, cancel := context.WithTimeout(ctx, timeout)
		b, err := e.balance(call, p.runID, account.AccountID)
		cancel()
		if err != nil {
			return report, err
		}
		if b.AsOfSeq != 0 || b.BTC.Reserved != 0 || b.USDT.Reserved != 0 || b.BTC.Available != account.BaseLots || b.USDT.Available != account.QuoteAtoms {
			return report, fmt.Errorf("run is not pristine or initial balance differs for %s", account.AccountID)
		}
		report.Balances = append(report.Balances, InitialBalance{account.AccountID, b.BTC.Available, b.USDT.Available})
	}
	for i, req := range p.preparation {
		call, cancel := context.WithTimeout(ctx, timeout)
		sample := PreparationSample{Index: i, StartedUTC: time.Now().UTC()}
		x := e.Execute(call, req)
		cancel()
		sample.FinishedUTC, sample.Outcome = time.Now().UTC(), x.Outcome
		report.Samples = append(report.Samples, sample)
		if x.Outcome.Class != Business || x.Outcome.Code != string(d.Accepted) || x.Outcome.OrderStatus != d.Open || x.Outcome.TradeCount != 0 || x.Outcome.CommandSeq != d.Int(i+1) {
			return report, fmt.Errorf("seed order %d failed or observed interfering commands", i)
		}
	}
	report.Complete = true
	return report, nil
}
