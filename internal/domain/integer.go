// Package domain defines deterministic v1 values without I/O or wall clocks.
package domain

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// Int is a nonnegative int64 encoded as a decimal JSON string. Field names
// distinguish lots, atoms, price ticks, and sequence numbers.
type Int int64

var (
	ErrInteger  = errors.New("invalid nonnegative integer")
	ErrOverflow = errors.New("integer overflow or underflow")
)

// ParseInt accepts 1-19 ASCII digits, normalizing leading zeroes without floats.
func ParseInt(s string) (Int, error) {
	if len(s) == 0 || len(s) > 19 {
		return 0, ErrInteger
	}
	var n Int
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrInteger
		}
		d := Int(s[i] - '0')
		if n > (math.MaxInt64-d)/10 {
			return 0, ErrOverflow
		}
		n = n*10 + d
	}
	return n, nil
}

// String returns the canonical decimal representation.
func (n Int) String() string { return strconv.FormatInt(int64(n), 10) }

// MarshalJSON preserves precision in clients that use floating point numbers.
func (n Int) MarshalJSON() ([]byte, error) {
	if n < 0 {
		return nil, ErrInteger
	}
	return json.Marshal(n.String())
}

// UnmarshalJSON rejects numbers, null, fractions, signs, and overflow.
func (n *Int) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return ErrInteger
	}
	v, err := ParseInt(s)
	if err != nil {
		return err
	}
	*n = v
	return nil
}

// Add checks nonnegative addition before evaluating it.
func Add(a, b Int) (Int, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// Sub checks nonnegative subtraction before evaluating it.
func Sub(a, b Int) (Int, error) {
	if a < 0 || b < 0 || b > a {
		return 0, ErrOverflow
	}
	return a - b, nil
}

// Mul checks nonnegative multiplication before evaluating it.
func Mul(a, b Int) (Int, error) {
	if a < 0 || b < 0 || (b != 0 && a > math.MaxInt64/b) {
		return 0, ErrOverflow
	}
	return a * b, nil
}
