// Package money implements Money as an immutable value object backed by
// int64 minor units (e.g. cents). float32/float64 must never be used for
// monetary values anywhere in this codebase - not for parsing, arithmetic,
// serialization or persistence.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Scale is the fixed number of decimal places supported by Money.
const Scale = 2

const scaleFactor int64 = 100 // 10^Scale

var (
	ErrEmptyAmount      = errors.New("money: amount is empty")
	ErrInvalidAmount    = errors.New("money: invalid amount format")
	ErrNegativeAmount   = errors.New("money: negative amount not allowed")
	ErrOverflow         = errors.New("money: arithmetic overflow")
	ErrInvalidCurrency  = errors.New("money: invalid ISO 4217 currency code")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
)

// externalAmountPattern matches exactly "<digits>.<2 digits>" with no sign,
// no exponent and no leading/trailing whitespace. Anything else (NaN,
// Infinity, scientific notation, more/less than 2 decimal places, a
// leading '-') is rejected before it ever reaches a numeric parser.
var externalAmountPattern = regexp.MustCompile(`^[0-9]+\.[0-9]{2}$`)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Money is an immutable monetary value in a single currency, stored as an
// integer count of minor units (e.g. cents for BRL) instead of a floating
// point type.
type Money struct {
	minorUnits int64
	currency   string
}

// New builds a Money from an already-known minor-units amount. Unlike
// ParseExternal, it accepts negative values: internal calculations (e.g.
// diffs during reconciliation) legitimately produce negative Money even
// though no external input or wallet balance may ever be negative.
func New(minorUnits int64, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{minorUnits: minorUnits, currency: cur}, nil
}

// Zero returns the zero amount for a currency. It panics on a malformed
// currency code because it is meant to be called with a compile-time
// constant, not with untrusted input - use ParseExternal for that.
func Zero(currency string) Money {
	m, err := New(0, currency)
	if err != nil {
		panic(err)
	}
	return m
}

// ParseExternal parses a decimal string as it arrives from an external
// caller (HTTP body, SQS message). It enforces the contract rules from the
// spec: no empty string, no NaN/Infinity, no scientific notation, exactly
// two decimal places, and no negative sign.
func ParseExternal(amount string, currency string) (Money, error) {
	if amount == "" {
		return Money{}, ErrEmptyAmount
	}
	if !externalAmountPattern.MatchString(amount) {
		return Money{}, fmt.Errorf("%w: %q must match ^[0-9]+\\.[0-9]{2}$", ErrInvalidAmount, amount)
	}

	minorUnits, err := decimalToMinorUnits(amount)
	if err != nil {
		return Money{}, err
	}
	return New(minorUnits, currency)
}

func decimalToMinorUnits(amount string) (int64, error) {
	intPart, fracPart, _ := strings.Cut(amount, ".")

	intUnits, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	fracUnits, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}

	scaled, ok := mulOverflows(intUnits, scaleFactor)
	if !ok {
		return 0, ErrOverflow
	}
	total, ok := addOverflows(scaled, fracUnits)
	if !ok {
		return 0, ErrOverflow
	}
	return total, nil
}

func normalizeCurrency(currency string) (string, error) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if !currencyPattern.MatchString(cur) {
		return "", fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	return cur, nil
}

// Add returns m + other. Both operands must share the same currency.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, ErrCurrencyMismatch
	}
	sum, ok := addOverflows(m.minorUnits, other.minorUnits)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: sum, currency: m.currency}, nil
}

// Sub returns m - other. Both operands must share the same currency. The
// result may be negative (e.g. reconciliation differences); callers that
// must never see a negative amount (wallet balances) enforce that
// separately.
func (m Money) Sub(other Money) (Money, error) {
	negOther, err := other.Negate()
	if err != nil {
		return Money{}, err
	}
	return m.Add(negOther)
}

// Negate returns -m in the same currency.
func (m Money) Negate() (Money, error) {
	if m.minorUnits == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

// Compare returns -1, 0 or 1 as m is less than, equal to, or greater than
// other. Both operands must share the same currency.
func (m Money) Compare(other Money) (int, error) {
	if m.currency != other.currency {
		return 0, ErrCurrencyMismatch
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether m and other have the same currency and amount.
func (m Money) Equal(other Money) bool {
	return m.currency == other.currency && m.minorUnits == other.minorUnits
}

func (m Money) IsZero() bool     { return m.minorUnits == 0 }
func (m Money) IsNegative() bool { return m.minorUnits < 0 }
func (m Money) IsPositive() bool { return m.minorUnits > 0 }

// MinorUnits returns the raw integer amount (e.g. cents for BRL).
func (m Money) MinorUnits() int64 { return m.minorUnits }

// Currency returns the ISO 4217 currency code.
func (m Money) Currency() string { return m.currency }

// SameCurrency reports whether m and other share a currency.
func (m Money) SameCurrency(other Money) bool { return m.currency == other.currency }

// String renders the decimal form, e.g. "25.00" or "-25.00".
func (m Money) String() string {
	return m.decimalString()
}

func (m Money) decimalString() string {
	negative := m.minorUnits < 0
	abs := m.minorUnits
	if negative {
		abs = -abs
	}
	intPart := abs / scaleFactor
	fracPart := abs % scaleFactor
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, intPart, fracPart)
}

type jsonMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON renders {"amount":"25.00","currency":"BRL"} per the wire contract.
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonMoney{Amount: m.decimalString(), Currency: m.currency})
}

// UnmarshalJSON parses the wire contract using the same strict rules as
// ParseExternal (this is what external HTTP/SQS payloads go through).
func (m *Money) UnmarshalJSON(data []byte) error {
	var j jsonMoney
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAmount, err)
	}
	parsed, err := ParseExternal(j.Amount, j.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func addOverflows(a, b int64) (int64, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

func mulOverflows(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	result := a * b
	if result/b != a {
		return 0, false
	}
	return result, true
}
