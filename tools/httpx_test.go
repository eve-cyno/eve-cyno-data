package tools

import (
	"testing"
)

func TestFmtISK(t *testing.T) {
	// All cases verified against Python f"{v:,.2f}"
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0.00"},
		{1, "1.00"},
		{3.82, "3.82"},
		{1000, "1,000.00"},
		{1234567.89, "1,234,567.89"},
		{999999.999, "1,000,000.00"},
		{5.5, "5.50"},
		{100000, "100,000.00"},
		{1000000, "1,000,000.00"},
	}
	for _, c := range cases {
		got := fmtISK(c.in)
		if got != c.want {
			t.Errorf("fmtISK(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
