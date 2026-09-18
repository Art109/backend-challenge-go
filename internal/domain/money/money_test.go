package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"backend-challenge-go/internal/domain/money"
)

func TestParseExternal_Valid(t *testing.T) {
	cases := []struct {
		amount   string
		currency string
		want     int64
	}{
		{"25.00", "BRL", 2500},
		{"0.01", "BRL", 1},
		{"1000.00", "brl", 100000}, // lowercase currency is normalized to upper
		{"0.00", "BRL", 0},
	}
	for _, tc := range cases {
		got, err := money.ParseExternal(tc.amount, tc.currency)
		if err != nil {
			t.Fatalf("ParseExternal(%q, %q) unexpected error: %v", tc.amount, tc.currency, err)
		}
		if got.MinorUnits() != tc.want {
			t.Errorf("ParseExternal(%q, %q).MinorUnits() = %d, want %d", tc.amount, tc.currency, got.MinorUnits(), tc.want)
		}
		if got.Currency() != "BRL" {
			t.Errorf("ParseExternal(%q, %q).Currency() = %q, want BRL", tc.amount, tc.currency, got.Currency())
		}
	}
}

func TestParseExternal_Rejects(t *testing.T) {
	cases := []struct {
		name     string
		amount   string
		currency string
	}{
		{"empty", "", "BRL"},
		{"nan", "NaN", "BRL"},
		{"infinity", "Infinity", "BRL"},
		{"scientific notation", "2.5e1", "BRL"},
		{"excess scale", "25.001", "BRL"},
		{"missing scale", "25", "BRL"},
		{"negative", "-25.00", "BRL"},
		{"letters", "abc.de", "BRL"},
		{"trailing space", "25.00 ", "BRL"},
		{"leading space", " 25.00", "BRL"},
		{"comma decimal", "25,00", "BRL"},
		{"invalid currency", "25.00", "R$"},
		{"empty currency", "25.00", ""},
		{"lowercase-length currency", "25.00", "br"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := money.ParseExternal(tc.amount, tc.currency)
			if err == nil {
				t.Fatalf("ParseExternal(%q, %q) expected error, got nil", tc.amount, tc.currency)
			}
		})
	}
}

func TestParseExternal_Overflow(t *testing.T) {
	// The integer part alone fits in int64, but multiplying by the scale
	// factor (100) to convert to minor units overflows - this is the
	// overflow path the multiplication guard exists for.
	_, err := money.ParseExternal("92233720368547759.00", "BRL")
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
}

func TestParseExternal_IntPartTooLargeForInt64(t *testing.T) {
	// The integer part itself doesn't fit in int64: strconv.ParseInt
	// rejects it directly, before the overflow-checked multiplication
	// ever runs. Still correctly rejected, just via ErrInvalidAmount.
	_, err := money.ParseExternal("999999999999999999999.00", "BRL")
	if err == nil {
		t.Fatalf("expected an error for an integer part that overflows int64")
	}
}

func TestZero(t *testing.T) {
	z := money.Zero("BRL")
	if !z.IsZero() {
		t.Errorf("Zero(BRL).IsZero() = false, want true")
	}
	if z.String() != "0.00" {
		t.Errorf("Zero(BRL).String() = %q, want 0.00", z.String())
	}
}

func TestAdd(t *testing.T) {
	a, _ := money.ParseExternal("25.00", "BRL")
	b, _ := money.ParseExternal("10.50", "BRL")
	got, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "35.50" {
		t.Errorf("got %s, want 35.50", got.String())
	}
}

func TestAdd_CurrencyMismatch(t *testing.T) {
	a, _ := money.ParseExternal("25.00", "BRL")
	b, _ := money.ParseExternal("10.50", "USD")
	_, err := a.Add(b)
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
}

func TestAdd_Overflow(t *testing.T) {
	a, err := money.New(math.MaxInt64, "BRL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	one, _ := money.ParseExternal("0.01", "BRL")
	_, err = a.Add(one)
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
}

func TestSub_CanGoNegative(t *testing.T) {
	a, _ := money.ParseExternal("10.00", "BRL")
	b, _ := money.ParseExternal("25.00", "BRL")
	got, err := a.Sub(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsNegative() {
		t.Errorf("expected negative result, got %s", got.String())
	}
	if got.String() != "-15.00" {
		t.Errorf("got %s, want -15.00", got.String())
	}
}

func TestSub_CurrencyMismatch(t *testing.T) {
	a, _ := money.ParseExternal("25.00", "BRL")
	b, _ := money.ParseExternal("10.00", "USD")
	_, err := a.Sub(b)
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
}

func TestNegate(t *testing.T) {
	a, _ := money.ParseExternal("25.00", "BRL")
	neg, err := a.Negate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if neg.String() != "-25.00" {
		t.Errorf("got %s, want -25.00", neg.String())
	}
	back, err := neg.Negate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !back.Equal(a) {
		t.Errorf("double negate = %s, want %s", back.String(), a.String())
	}
}

func TestNegate_Overflow(t *testing.T) {
	m, err := money.New(math.MinInt64, "BRL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = m.Negate()
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
}

func TestCompare(t *testing.T) {
	a, _ := money.ParseExternal("10.00", "BRL")
	b, _ := money.ParseExternal("25.00", "BRL")

	if cmp, err := a.Compare(b); err != nil || cmp != -1 {
		t.Errorf("a.Compare(b) = %d, %v; want -1, nil", cmp, err)
	}
	if cmp, err := b.Compare(a); err != nil || cmp != 1 {
		t.Errorf("b.Compare(a) = %d, %v; want 1, nil", cmp, err)
	}
	if cmp, err := a.Compare(a); err != nil || cmp != 0 {
		t.Errorf("a.Compare(a) = %d, %v; want 0, nil", cmp, err)
	}
	if _, err := a.Compare(money.Zero("USD")); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got %v", err)
	}
}

func TestEqual(t *testing.T) {
	a, _ := money.ParseExternal("25.00", "BRL")
	b, _ := money.ParseExternal("25.00", "BRL")
	c, _ := money.ParseExternal("25.00", "USD")
	if !a.Equal(b) {
		t.Errorf("expected a.Equal(b)")
	}
	if a.Equal(c) {
		t.Errorf("expected !a.Equal(c) (different currency)")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	a, _ := money.ParseExternal("25.00", "BRL")
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if string(data) != `{"amount":"25.00","currency":"BRL"}` {
		t.Errorf("Marshal = %s, want {\"amount\":\"25.00\",\"currency\":\"BRL\"}", data)
	}

	var b money.Money
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if !a.Equal(b) {
		t.Errorf("round trip mismatch: %s != %s", a.String(), b.String())
	}
}

func TestJSONUnmarshal_RejectsNegative(t *testing.T) {
	var m money.Money
	err := json.Unmarshal([]byte(`{"amount":"-25.00","currency":"BRL"}`), &m)
	if err == nil {
		t.Fatalf("expected error unmarshaling negative amount")
	}
}

func TestJSONUnmarshal_RejectsFloatLeakage(t *testing.T) {
	// Guards against a regression where amount is accidentally decoded as
	// a JSON number (which round-trips through float64) instead of string.
	var m money.Money
	err := json.Unmarshal([]byte(`{"amount":25.00,"currency":"BRL"}`), &m)
	if err == nil {
		t.Fatalf("expected error unmarshaling numeric amount, money.Money.Amount must be a JSON string")
	}
}
