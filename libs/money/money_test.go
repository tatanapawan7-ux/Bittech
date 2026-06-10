package money

import (
	"errors"
	"math"
	"testing"
)

func TestAddSub(t *testing.T) {
	a, err := Amount(150000000).Add(50000000)
	if err != nil || a != 200000000 {
		t.Fatalf("Add: got %d, %v", a, err)
	}
	d, err := Amount(200000000).Sub(50000000)
	if err != nil || d != 150000000 {
		t.Fatalf("Sub: got %d, %v", d, err)
	}
}

func TestAddOverflow(t *testing.T) {
	if _, err := Amount(math.MaxInt64).Add(1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
	if _, err := Amount(math.MinInt64).Sub(1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestMul(t *testing.T) {
	// price 30000.00 (scale 2) * quantity factor 3 == 90000.00
	v, err := Amount(3000000).Mul(3)
	if err != nil || v != 9000000 {
		t.Fatalf("Mul: got %d, %v", v, err)
	}
	if _, err := Amount(math.MaxInt64).Mul(2); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in    string
		scale int
		want  Amount
	}{
		{"1.5", 8, 150000000},
		{"0.00000001", 8, 1},
		{"30000", 2, 3000000},
		{"-2.50", 2, -250},
		{"1", 0, 1},
	}
	for _, c := range cases {
		got, err := Parse(c.in, c.scale)
		if err != nil || got != c.want {
			t.Errorf("Parse(%q, %d) = %d, %v; want %d", c.in, c.scale, got, err, c.want)
		}
	}
}

func TestParseRejectsExcessPrecision(t *testing.T) {
	if _, err := Parse("1.123", 2); !errors.Is(err, ErrBadFormat) {
		t.Fatalf("expected bad format for excess precision, got %v", err)
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, s := range []string{"1.50000000", "0.00000001", "12345.67890000"} {
		a, err := Parse(s, 8)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		if got := a.String(8); got != s {
			t.Errorf("round trip: Parse(%q).String(8) = %q", s, got)
		}
	}
}
