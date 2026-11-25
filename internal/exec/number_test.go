package exec

import "testing"

func TestParseNumber(t *testing.T) {
	cases := []struct {
		s    string
		dec  rune
		want float64
		ok   bool
	}{
		// a point: strconv.ParseFloat as before
		{"1.5", 0, 1.5, true},
		{" -2 ", '.', -2, true},
		{"1e3", 0, 1000, true},
		{"1,5", 0, 0, false},
		{"", 0, 0, false},
		{"x", '.', 0, false},
		// a decimal comma
		{"1.234,50", ',', 1234.5, true},
		{"-3,5", ',', -3.5, true},
		{"+3,5", ',', 3.5, true},
		{"1200", ',', 1200, true},
		{"1200,50", ',', 1200.5, true},
		{"12.345.678", ',', 12345678, true},
		{"1.234", ',', 1234, true},
		{" 0,25 ", ',', 0.25, true},
		{"999.999,999", ',', 999999.999, true},
		{"1.23,4", ',', 0, false},
		{"1,2,3", ',', 0, false},
		{",5", ',', 0, false},
		{"1,", ',', 0, false},
		{"1.234.", ',', 0, false},
		{".234", ',', 0, false},
		{"1234.567", ',', 0, false}, // a first group of four digits
		{"1.2345", ',', 0, false},
		{"1.5", ',', 0, false},
		{"1e3", ',', 0, false},
		{"--1", ',', 0, false},
		{"", ',', 0, false},
		{"-", ',', 0, false},
		{"abc", ',', 0, false},
	}
	for _, c := range cases {
		got, ok := ParseNumber(c.s, c.dec)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseNumber(%q, %q) = %v, %v; want %v, %v", c.s, c.dec, got, ok, c.want, c.ok)
		}
	}
}
