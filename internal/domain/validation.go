package domain

import (
	"errors"
	"fmt"
)

var ErrInvalid = errors.New("invalid v1 input")

// ValidID accepts only the case-sensitive ASCII v1 identifier alphabet.
func ValidID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ValidRunID accepts lowercase UUID v4 identifiers without generating randomness.
func ValidRunID(id string) bool {
	if len(id) != 36 || id[14] != '4' || !(id[19] == '8' || id[19] == '9' || id[19] == 'a' || id[19] == 'b') {
		return false
	}
	for i := range len(id) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if id[i] != '-' {
				return false
			}
			continue
		}
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
			return false
		}
	}
	return true
}

// Validate checks command shape and bounds without consulting mutable balances.
func (c Command) Validate() error {
	if !ValidRunID(c.RunID) || !ValidID(c.AccountID) {
		return ErrInvalid
	}
	switch c.Kind {
	case PlaceOrder:
		p := c.Place
		if c.Cancel != (Cancel{}) || !ValidID(p.ClientOrderID) || p.Symbol != Symbol || (p.Side != Buy && p.Side != Sell) || p.Type != "LIMIT" || p.TimeInForce != "GTC" {
			return ErrInvalid
		}
		if p.PriceTicks < 1 || p.PriceTicks > MaxPriceTicks || p.QuantityLots < 1 || p.QuantityLots > MaxQuantityLots {
			return ErrInvalid
		}
		n, err := Mul(p.PriceTicks, p.QuantityLots)
		if err != nil || n > MaxNotionalAtoms {
			return ErrInvalid
		}
	case CancelOrder:
		if c.Place != (Place{}) || !ValidID(c.Cancel.CancelRequestID) || !ValidID(c.Cancel.ClientOrderID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// InitialAccount is the initial supply assigned to one test account.
type InitialAccount struct {
	AccountID  string `json:"accountId"`
	BaseLots   Int    `json:"baseLots"`
	QuoteAtoms Int    `json:"quoteAtoms"`
}

// Config is the semantic v1 configuration. Wire decoding and file I/O belong to
// later adapters; Validate must run before any state is created.
type Config struct {
	SchemaVersion       int              `json:"schemaVersion"`
	RulesVersion        int              `json:"rulesVersion"`
	ResponseVersion     int              `json:"responseVersion"`
	Symbol              string           `json:"symbol"`
	BaseLotDecimals     int              `json:"baseLotDecimals"`
	QuoteAtomDecimals   int              `json:"quoteAtomDecimals"`
	MaxPriceTicks       Int              `json:"maxPriceTicks"`
	MaxQuantityLots     Int              `json:"maxQuantityLots"`
	MaxNotionalAtoms    Int              `json:"maxNotionalAtoms"`
	MaxBaseSupplyLots   Int              `json:"maxBaseSupplyLots"`
	MaxQuoteSupplyAtoms Int              `json:"maxQuoteSupplyAtoms"`
	MaxCommands         Int              `json:"maxCommands"`
	Accounts            []InitialAccount `json:"accounts"`
}

// DefaultConfig copies accounts and fills the fixed v1 units and limits.
func DefaultConfig(accounts []InitialAccount) Config {
	return Config{Version, Version, Version, Symbol, 4, 6, MaxPriceTicks, MaxQuantityLots, MaxNotionalAtoms, MaxBaseSupplyLots, MaxQuoteSupplyAtoms, DefaultMaxCommands, append([]InitialAccount(nil), accounts...)}
}

// Validate checks versions, fixed units, IDs, and aggregate supply before use.
func (c Config) Validate() error {
	if c.SchemaVersion != Version || c.RulesVersion != Version || c.ResponseVersion != Version || c.Symbol != Symbol || c.BaseLotDecimals != 4 || c.QuoteAtomDecimals != 6 || c.MaxPriceTicks != MaxPriceTicks || c.MaxQuantityLots != MaxQuantityLots || c.MaxNotionalAtoms != MaxNotionalAtoms || c.MaxBaseSupplyLots != MaxBaseSupplyLots || c.MaxQuoteSupplyAtoms != MaxQuoteSupplyAtoms || c.MaxCommands < 1 {
		return fmt.Errorf("%w: configuration", ErrInvalid)
	}
	if len(c.Accounts) < 1 || len(c.Accounts) > 10_000 {
		return fmt.Errorf("%w: account count", ErrInvalid)
	}
	seen := make(map[string]bool, len(c.Accounts))
	var base, quote Int
	for _, a := range c.Accounts {
		if !ValidID(a.AccountID) || seen[a.AccountID] || a.BaseLots < 0 || a.QuoteAtoms < 0 {
			return fmt.Errorf("%w: account", ErrInvalid)
		}
		seen[a.AccountID] = true
		var err error
		base, err = Add(base, a.BaseLots)
		if err != nil || base > MaxBaseSupplyLots {
			return fmt.Errorf("%w: base supply", ErrInvalid)
		}
		quote, err = Add(quote, a.QuoteAtoms)
		if err != nil || quote > MaxQuoteSupplyAtoms {
			return fmt.Errorf("%w: quote supply", ErrInvalid)
		}
	}
	return nil
}
