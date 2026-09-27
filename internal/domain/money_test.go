package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestMoneyParsing(t *testing.T) {
	for _, s := range []string{"0", "00.0", "0.00", "000"} {
		m, e := ParseMoney(s, "BRL")
		if e != nil || m.Amount() != "0.00" {
			t.Fatalf("%q: %v %v", s, m, e)
		}
	}
	for _, s := range []string{"", "-1", "-0", "+1", " 1", "1 ", "1e2", "NaN", "Infinity", "1.001", "1.", ".1", "1..0", "١", "92233720368547758.08"} {
		if _, e := ParseMoney(s, "BRL"); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	m, e := ParseMoney("92233720368547758.07", "BRL")
	if e != nil || m.Minor() != math.MaxInt64 {
		t.Fatal(m, e)
	}
	m, e = ParseMoney("00025.5", "BRL")
	if e != nil || m.Amount() != "25.50" {
		t.Fatal(m, e)
	}
	if _, e = ParseMoney("1", "XYZ"); !errors.Is(e, ErrCurrency) {
		t.Fatal(e)
	}
}
func TestMoneyArithmeticBoundaries(t *testing.T) {
	max, _ := FromMinor(math.MaxInt64, "BRL")
	min, _ := FromMinor(math.MinInt64, "BRL")
	one, _ := FromMinor(1, "BRL")
	negative, _ := FromMinor(-1, "BRL")
	for _, f := range []func() (Money, error){func() (Money, error) { return max.Add(one) }, func() (Money, error) { return min.Add(negative) }, func() (Money, error) { return min.Sub(one) }, func() (Money, error) { return max.Sub(negative) }, min.Negate} {
		if _, e := f(); !errors.Is(e, ErrOverflow) {
			t.Fatal(e)
		}
	}
	z, e := min.Sub(min)
	if e != nil || z.Minor() != 0 {
		t.Fatal(z, e)
	}
	if min.Amount() != "-92233720368547758.08" {
		t.Fatal(min.Amount())
	}
	usd, _ := FromMinor(1, "USD")
	if _, e = one.Add(usd); !errors.Is(e, ErrCurrency) {
		t.Fatal(e)
	}
	if _, e = one.Compare(usd); !errors.Is(e, ErrCurrency) {
		t.Fatal(e)
	}
	if _, e = (Money{}).Add(one); !errors.Is(e, ErrInvalidMoney) {
		t.Fatal(e)
	}
	if _, e = json.Marshal(Money{}); e == nil {
		t.Fatal("zero value serialized")
	}
}
func TestMoneyJSON(t *testing.T) {
	m, _ := ParseMoney("25", "BRL")
	b, e := json.Marshal(m)
	if e != nil || string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatal(string(b), e)
	}
	for _, raw := range []string{`{"amount":25,"currency":"BRL"}`, `null`, `{"amount":"-1","currency":"BRL"}`, `{"amount":"1","currency":"BRL","extra":true}`} {
		var n Money
		if json.Unmarshal([]byte(raw), &n) == nil {
			t.Fatal(raw)
		}
	}
	var n Money
	if e = json.Unmarshal(b, &n); e != nil || n != m {
		t.Fatal(n, e)
	}
}
func FuzzMoneyRoundTrip(f *testing.F) {
	for _, n := range []int64{0, 1, 100, math.MaxInt64} {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, n int64) {
		if n < 0 {
			return
		}
		m, _ := FromMinor(n, "BRL")
		parsed, e := ParseMoney(m.Amount(), "BRL")
		if e != nil || parsed != m {
			t.Fatal(n, parsed, e)
		}
	})
}
