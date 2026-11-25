package exec

import (
	"strconv"
	"strings"
)

// ParseNumber returns the numeric value of a cell's text in a column with
// the given decimal separator (ColumnInfo.Decimal): the one rule for the
// grid's local sort and filters.
//
//   - '.' or 0 (the default, every SQL result): strconv.ParseFloat of the
//     trimmed text, exactly as before.
//   - ',' (decimal comma, A29): an optional sign, an integer part that is
//     either plain digits or 1–3 digits followed by groups of a dot and
//     exactly three digits, and an optional comma with digits. "1.234,50"
//     is 1234.5, "-3,5" is -3.5, "12.345.678" is 12345678, "1200" is 1200;
//     "1.23,4", "1,2,3", ",5" and "1.234." are not numbers.
func ParseNumber(s string, decimal rune) (float64, bool) {
	s = strings.TrimSpace(s)
	if decimal != ',' {
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	if s == "" {
		return 0, false
	}
	sign := ""
	if s[0] == '-' || s[0] == '+' {
		sign, s = s[:1], s[1:]
	}
	intPart, frac, hasFrac := strings.Cut(s, ",")
	if hasFrac && (frac == "" || !allDigits(frac)) {
		return 0, false
	}
	if intPart == "" {
		return 0, false
	}
	if strings.Contains(intPart, ".") {
		groups := strings.Split(intPart, ".")
		if len(groups[0]) < 1 || len(groups[0]) > 3 || !allDigits(groups[0]) {
			return 0, false
		}
		for _, g := range groups[1:] {
			if len(g) != 3 || !allDigits(g) {
				return 0, false
			}
		}
		intPart = strings.Join(groups, "")
	} else if !allDigits(intPart) {
		return 0, false
	}
	text := sign + intPart
	if hasFrac {
		text += "." + frac
	}
	f, err := strconv.ParseFloat(text, 64)
	return f, err == nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
