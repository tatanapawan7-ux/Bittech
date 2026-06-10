// Package money provides a fixed-point integer money type for the exchange.
//
// All monetary values in the system are represented as integers in an asset's
// smallest indivisible unit (e.g. satoshis for BTC, wei-scaled units for tokens).
// Floating-point numbers are NEVER used for balances, prices, or quantities:
// binary floats cannot represent decimal fractions exactly, and the resulting
// rounding errors are unacceptable in a double-entry ledger that must balance to
// the unit. This mirrors how production exchanges represent money internally.
package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Amount is a signed quantity of an asset expressed in its smallest unit.
type Amount int64

var (
	// ErrOverflow is returned when an operation would exceed the int64 range.
	ErrOverflow = errors.New("money: arithmetic overflow")
	// ErrNegative is returned when a value is negative but a non-negative value
	// was required.
	ErrNegative = errors.New("money: negative amount")
	// ErrBadFormat is returned when a decimal string cannot be parsed.
	ErrBadFormat = errors.New("money: invalid decimal format")
)

// Add returns a+b, or ErrOverflow if the result does not fit in an Amount.
func (a Amount) Add(b Amount) (Amount, error) {
	s := a + b
	// Overflow occurred iff the sign of the result is inconsistent with the
	// sign of the addend.
	if (s > a) != (b > 0) && b != 0 {
		return 0, ErrOverflow
	}
	return s, nil
}

// Sub returns a-b, or ErrOverflow if the result does not fit in an Amount.
func (a Amount) Sub(b Amount) (Amount, error) {
	d := a - b
	if (d < a) != (b > 0) && b != 0 {
		return 0, ErrOverflow
	}
	return d, nil
}

// IsNegative reports whether the amount is below zero.
func (a Amount) IsNegative() bool { return a < 0 }

// IsZero reports whether the amount is exactly zero.
func (a Amount) IsZero() bool { return a == 0 }

// Mul multiplies the amount by an integer factor, guarding against overflow.
// This is used, for example, to compute a trade's quote value from a price and
// a base quantity before rescaling.
func (a Amount) Mul(factor int64) (Amount, error) {
	if a == 0 || factor == 0 {
		return 0, nil
	}
	r := a * Amount(factor)
	if r/Amount(factor) != a {
		return 0, ErrOverflow
	}
	return r, nil
}

// Parse converts a human decimal string (e.g. "1.2345") into an Amount given the
// asset's scale, where scale is the number of decimal places in the smallest
// unit. Parse("1.5", 8) == 150000000. It rejects values with more fractional
// digits than the scale allows so that no precision is silently lost.
func Parse(s string, scale int) (Amount, error) {
	if scale < 0 || scale > 18 {
		return 0, fmt.Errorf("%w: scale %d out of range", ErrBadFormat, scale)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrBadFormat
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}

	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if hasFrac && len(fracPart) > scale {
		return 0, fmt.Errorf("%w: more than %d fractional digits", ErrBadFormat, scale)
	}
	// Pad the fractional part out to the full scale.
	fracPart = fracPart + strings.Repeat("0", scale-len(fracPart))

	digits := intPart + fracPart
	if digits == "" {
		return 0, ErrBadFormat
	}
	n := new(big.Int)
	if _, ok := n.SetString(digits, 10); !ok {
		return 0, ErrBadFormat
	}
	if !n.IsInt64() {
		return 0, ErrOverflow
	}
	v := n.Int64()
	if neg {
		v = -v
	}
	return Amount(v), nil
}

// String renders the amount as a decimal string using the given scale.
func (a Amount) String(scale int) string {
	if scale <= 0 {
		return fmt.Sprintf("%d", int64(a))
	}
	v := int64(a)
	neg := v < 0
	if neg {
		v = -v
	}
	div := int64(math.Pow10(scale))
	whole := v / div
	frac := v % div
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d", sign, whole, scale, frac)
}
