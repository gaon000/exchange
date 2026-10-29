// Package runtime coordinates bounded admission, one business-state owner,
// durable groups, and immutable command-boundary responses.
package runtime

import (
	"fmt"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/wal"
)

// Outcome describes this request attempt, not the history of its idempotency key.
type Outcome string

const (
	NotAdmitted Outcome = "NOT_ADMITTED"
	Unknown     Outcome = "UNKNOWN"
)

// Failure is an operational/admission failure, never a persisted business result.
type Failure struct {
	Code    string  `json:"code"`
	Outcome Outcome `json:"outcome"`
	Cause   error   `json:"-"`
}

// Error summarizes the code and certainty without exposing internal diagnostics.
func (f *Failure) Error() string { return fmt.Sprintf("%s (%s)", f.Code, f.Outcome) }

// Unwrap preserves a diagnostic cause without exposing it on the wire.
func (f *Failure) Unwrap() error { return f.Cause }
func failure(code string, outcome Outcome, cause error) *Failure {
	return &Failure{code, outcome, cause}
}

// Options bounds queued requests, result waiters, and each durable group.
// RequestLimit covers calls awaiting runtime replies; HTTP also holds its own
// request limit through response transmission. Both use the same configured limit.
type Options struct {
	CommandQueue   int
	QueryQueue     int
	RequestLimit   int
	PendingWaiters int
	BatchCommands  int
	BatchBytes     int
	BatchDelay     time.Duration
}

// DefaultOptions returns the initial v1 limits, not a measured performance claim.
func DefaultOptions() Options {
	return Options{8192, 1024, 16384, 16, 256, wal.MaxGroupPayload, time.Millisecond}
}

// Validate checks resource bounds without opening or changing a data directory.
func (o Options) Validate() error {
	if o.CommandQueue < 1 || o.CommandQueue > 1<<20 || o.QueryQueue < 1 || o.QueryQueue > 1<<20 || o.RequestLimit < 1 || o.RequestLimit > 1<<20 || o.PendingWaiters < 1 || o.PendingWaiters > o.RequestLimit || o.BatchCommands < 1 || o.BatchCommands > wal.MaxGroupCommands || o.BatchBytes < wal.MaxRecordBytes+4 || o.BatchBytes > wal.MaxGroupPayload || o.BatchDelay <= 0 {
		return d.ErrInvalid
	}
	return nil
}

// QueryKind selects a bounded owner-side read operation.
type QueryKind uint8

const (
	OrderQuery QueryKind = iota + 1
	BalancesQuery
	BookQuery
	TradesQuery
	ResultQuery
)

// Query carries validated account context and the fields relevant to its kind.
// Trade cursors require a fixed ThroughSeq when AfterSeq/AfterIndex are nonzero.
type Query struct {
	RunID       string
	AccountID   string
	Kind        QueryKind
	ID          string
	RequestKind d.Kind
	Limit       int
	AfterSeq    d.Int
	AfterIndex  d.Int
	ThroughSeq  *d.Int
}

// View is a detached read result from one complete command boundary. Result
// contains the original response without adding the current AsOfSeq to its body.
type View struct {
	RunID       string
	AsOfSeq     d.Int
	Order       *d.Order
	Balances    d.Balances
	Bids        []engine.Level
	Asks        []engine.Level
	Trades      []d.Trade
	ThroughSeq  d.Int
	Result      *d.Result
	ResultState string
}

type journal interface {
	Genesis() wal.Genesis
	Recovery() wal.Recovery
	Replay(func(wal.Record) error) error
	ConfirmRecovery() error
	Append([]wal.Record) error
	DurableSeq() d.Int
	Close() error
}
