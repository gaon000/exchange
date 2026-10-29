package domain

import "encoding/json"

const (
	Version                 = 1
	Symbol                  = "BTC-USDT"
	MaxPriceTicks       Int = 100_000_000
	MaxQuantityLots     Int = 1_000_000
	MaxNotionalAtoms    Int = 100_000_000_000_000
	MaxBaseSupplyLots   Int = 1_000_000_000_000
	MaxQuoteSupplyAtoms Int = 1_000_000_000_000_000_000
	DefaultMaxCommands  Int = 1_000_000
)

// Side is the side of a limit order.
type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

// Kind separates order and cancellation idempotency namespaces.
type Kind string

const (
	PlaceOrder  Kind = "PLACE_ORDER"
	CancelOrder Kind = "CANCEL_ORDER"
)

// Status is an order's business state, independent of admission or persistence.
type Status string

const (
	Open            Status = "OPEN"
	PartiallyFilled Status = "PARTIALLY_FILLED"
	Filled          Status = "FILLED"
	Cancelled       Status = "CANCELLED"
	Rejected        Status = "REJECTED"
)

// Code identifies a completed business result.
type Code string

const (
	Accepted            Code = "ACCEPTED"
	InsufficientBalance Code = "INSUFFICIENT_BALANCE"
	CancelSucceeded     Code = "CANCELLED"
	AlreadyFilled       Code = "ALREADY_FILLED"
	AlreadyCancelled    Code = "ALREADY_CANCELLED"
	OrderNotFound       Code = "ORDER_NOT_FOUND"
)

// Reason uses a value type to keep returned orders independent of engine state.
type Reason string

const (
	SelfTradePrevention Reason = "SELF_TRADE_PREVENTION"
	UserCancel          Reason = "USER_CANCEL"
	InsufficientFunds   Reason = "INSUFFICIENT_BALANCE"
)

// MarshalJSON writes an absent reason as null, per the response contract.
func (r Reason) MarshalJSON() ([]byte, error) {
	if r == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(r))
}

// Place is the normalized, comparable limit-order payload.
type Place struct {
	ClientOrderID string `json:"clientOrderId"`
	Symbol        string `json:"symbol"`
	Side          Side   `json:"side"`
	Type          string `json:"type"`
	TimeInForce   string `json:"timeInForce"`
	PriceTicks    Int    `json:"priceTicks"`
	QuantityLots  Int    `json:"quantityLots"`
}

// Cancel is the normalized cancellation payload.
type Cancel struct {
	CancelRequestID string `json:"cancelRequestId"`
	ClientOrderID   string `json:"clientOrderId"`
}

// Command is an in-process tagged union, not the WAL or HTTP wire encoding.
// The inactive payload must be zero. Sequence is supplied separately to Apply.
type Command struct {
	RunID     string
	AccountID string
	Kind      Kind
	Place     Place
	Cancel    Cancel
}

// Key identifies one request within a run and account.
type Key struct {
	RunID     string
	AccountID string
	Kind      Kind
	RequestID string
}

// Key returns the request's idempotency key; validate the command before use.
func (c Command) Key() Key {
	id := c.Place.ClientOrderID
	if c.Kind == CancelOrder {
		id = c.Cancel.CancelRequestID
	}
	return Key{c.RunID, c.AccountID, c.Kind, id}
}

// Order is a value snapshot with explicit units and no mutable references.
type Order struct {
	ClientOrderID      string `json:"clientOrderId"`
	Symbol             string `json:"symbol"`
	Side               Side   `json:"side"`
	PriceTicks         Int    `json:"priceTicks"`
	OriginalQty        Int    `json:"originalQty"`
	ExecutedQty        Int    `json:"executedQty"`
	OpenQty            Int    `json:"openQty"`
	CancelledQty       Int    `json:"cancelledQty"`
	ExecutedQuoteAtoms Int    `json:"executedQuoteAtoms"`
	Status             Status `json:"status"`
	Reason             Reason `json:"reason"`
	CreatedSeq         Int    `json:"createdSeq"`
	UpdatedSeq         Int    `json:"updatedSeq"`
}

// Active reports whether the order belongs in the order book.
func (o Order) Active() bool { return o.Status == Open || o.Status == PartiallyFilled }

// Result is the immutable initial command outcome, not the current order state.
type Result struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	AccountID     string `json:"accountId"`
	Kind          Kind   `json:"kind"`
	RequestID     string `json:"requestId"`
	CommandSeq    Int    `json:"commandSeq"`
	Code          Code   `json:"code"`
	Order         *Order `json:"order"`
	TradeCount    Int    `json:"tradeCount"`
}

// Clone returns a result whose optional order can be safely changed by a caller.
func (r Result) Clone() Result {
	if r.Order != nil {
		o := *r.Order
		r.Order = &o
	}
	return r
}

// Balance holds a single asset's spendable and reserved integer units.
type Balance struct {
	Available Int
	Reserved  Int
}

// Balances holds BTC lots and USDT atoms for one account.
type Balances struct {
	Base  Balance
	Quote Balance
}

// OrderRef scopes a client order ID to its owning account.
type OrderRef struct {
	AccountID     string
	ClientOrderID string
}

// Trade identifies both counterparties without nondeterministic timestamps.
type Trade struct {
	TradeID      string
	CommandSeq   Int
	MatchIndex   Int
	Maker        OrderRef
	Taker        OrderRef
	TakerSide    Side
	PriceTicks   Int
	QuantityLots Int
}
