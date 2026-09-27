package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var (
	ErrInvalidMoney      = errors.New("invalid money")
	ErrCurrency          = errors.New("unsupported or mismatched currency")
	ErrOverflow          = errors.New("money overflow")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrInvalidState      = errors.New("invalid state")
	ErrInvalidOperation  = errors.New("invalid operation")
)

// Money is immutable. Its zero Go value is deliberately invalid.
// Supported currencies use two fractional digits; external operations use BRL.
type Money struct {
	minor    int64
	currency string
}

func FromMinor(minor int64, currency string) (Money, error) {
	switch currency {
	case "BRL", "USD", "EUR":
		return Money{minor, currency}, nil
	}
	return Money{}, ErrCurrency
}
func Zero(currency string) (Money, error) { return FromMinor(0, currency) }

// ParseMoney accepts nonnegative plain decimals with zero, one or two decimals.
// It normalizes leading zeroes and fractional padding, never rounding.
func ParseMoney(amount, currency string) (Money, error) {
	if amount == "" {
		return Money{}, ErrInvalidMoney
	}
	parts := strings.Split(amount, ".")
	if len(parts) > 2 || parts[0] == "" {
		return Money{}, ErrInvalidMoney
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return Money{}, ErrInvalidMoney
		}
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	for _, c := range parts[0] + fraction {
		if c < '0' || c > '9' {
			return Money{}, ErrInvalidMoney
		}
	}
	digits := strings.TrimLeft(parts[0]+fraction, "0")
	if digits == "" {
		digits = "0"
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Money{}, ErrOverflow
	}
	return FromMinor(n, currency)
}
func (m Money) Valid() bool      { _, err := FromMinor(m.minor, m.currency); return err == nil }
func (m Money) Minor() int64     { return m.minor }
func (m Money) Currency() string { return m.currency }
func (m Money) compatible(n Money) error {
	if !m.Valid() || !n.Valid() {
		return ErrInvalidMoney
	}
	if m.currency != n.currency {
		return ErrCurrency
	}
	return nil
}
func (m Money) Add(n Money) (Money, error) {
	if err := m.compatible(n); err != nil {
		return Money{}, err
	}
	if (n.minor > 0 && m.minor > math.MaxInt64-n.minor) || (n.minor < 0 && m.minor < math.MinInt64-n.minor) {
		return Money{}, ErrOverflow
	}
	return FromMinor(m.minor+n.minor, m.currency)
}
func (m Money) Sub(n Money) (Money, error) {
	if err := m.compatible(n); err != nil {
		return Money{}, err
	}
	if (n.minor < 0 && m.minor > math.MaxInt64+n.minor) || (n.minor > 0 && m.minor < math.MinInt64+n.minor) {
		return Money{}, ErrOverflow
	}
	return FromMinor(m.minor-n.minor, m.currency)
}
func (m Money) Negate() (Money, error) {
	if !m.Valid() {
		return Money{}, ErrInvalidMoney
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return FromMinor(-m.minor, m.currency)
}
func (m Money) Compare(n Money) (int, error) {
	if err := m.compatible(n); err != nil {
		return 0, err
	}
	if m.minor < n.minor {
		return -1, nil
	}
	if m.minor > n.minor {
		return 1, nil
	}
	return 0, nil
}
func (m Money) Amount() string {
	// Absolute value as uint64 also handles MinInt64.
	n := uint64(m.minor)
	sign := ""
	if m.minor < 0 {
		sign = "-"
		n = uint64(-(m.minor + 1)) + 1
	}
	return fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
}
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.Valid() {
		return nil, ErrInvalidMoney
	}
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{m.Amount(), m.currency})
}
func (m *Money) UnmarshalJSON(data []byte) error {
	var wire struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMoney, err)
	}
	parsed, err := ParseMoney(wire.Amount, wire.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
