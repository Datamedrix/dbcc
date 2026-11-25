package script

// Text helpers for export formats (spec 13, A28): dbcc.fmt.cell,
// dbcc.fmt.csv and dbcc.fmt.json. dbcc.fmt.csv is the fast path of the
// CSV exporter: one call per record instead of string code per field.

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/exec"
)

// formatNumber renders a Lua number for text output: integral values
// without ".0" or exponent, others in the shortest exact form.
func formatNumber(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// cellString is the text of a row value: nil -> "", numbers as
// formatNumber, booleans as true/false.
func cellString(L *lua.LState, v lua.LValue) string {
	switch x := v.(type) {
	case *lua.LNilType:
		return ""
	case lua.LString:
		return string(x)
	case lua.LNumber:
		return formatNumber(float64(x))
	case lua.LBool:
		if x {
			return "true"
		}
		return "false"
	}
	return L.ToStringMeta(v).String()
}

// dbcc.fmt.cell(v) -> the text of a row value (nil -> "").
func luaFmtCell(L *lua.LState) int {
	L.Push(lua.LString(cellString(L, L.Get(1))))
	return 1
}

// csvQuoting modes of dbcc.fmt.csv.
var csvQuotings = map[string]bool{"minimal": true, "all": true, "nonnumeric": true, "none": true}

// csvOptions is the parsed opts table of dbcc.fmt.csv.
type csvOptions struct {
	delim, quote string
	quoting      string
	null         string
	// numeric marks fields (1-based) that "nonnumeric" leaves unquoted
	// although they are strings (values of numeric result columns).
	numeric *lua.LTable
}

func oneRune(L *lua.LState, opts *lua.LTable, field, def string) string {
	v := opts.RawGetString(field)
	if v == lua.LNil {
		return def
	}
	s, ok := v.(lua.LString)
	if !ok || utf8.RuneCountInString(string(s)) != 1 {
		L.ArgError(3, fmt.Sprintf("dbcc.fmt.csv: opts.%s must be exactly one character, got %s", field, describeLua(v)))
	}
	return string(s)
}

func parseCSVOptions(L *lua.LState, opts *lua.LTable) csvOptions {
	o := csvOptions{delim: ",", quote: `"`, quoting: "minimal"}
	if opts == nil {
		return o
	}
	o.delim = oneRune(L, opts, "delimiter", o.delim)
	o.quote = oneRune(L, opts, "quote", o.quote)
	if o.delim == o.quote {
		L.ArgError(3, "dbcc.fmt.csv: opts.delimiter and opts.quote must differ")
	}
	switch v := opts.RawGetString("quoting").(type) {
	case *lua.LNilType:
	case lua.LString:
		if !csvQuotings[string(v)] {
			L.ArgError(3, fmt.Sprintf("dbcc.fmt.csv: opts.quoting must be minimal, all, nonnumeric or none, got %q", string(v)))
		}
		o.quoting = string(v)
	default:
		L.ArgError(3, "dbcc.fmt.csv: opts.quoting must be a string")
	}
	switch v := opts.RawGetString("null").(type) {
	case *lua.LNilType:
	case lua.LString:
		o.null = string(v)
	default:
		L.ArgError(3, "dbcc.fmt.csv: opts.null must be a string")
	}
	switch v := opts.RawGetString("numeric").(type) {
	case *lua.LNilType:
	case *lua.LTable:
		o.numeric = v
	default:
		L.ArgError(3, "dbcc.fmt.csv: opts.numeric must be a list of booleans")
	}
	return o
}

// csvSpecial reports whether s contains the delimiter, the quote
// character, CR or LF: such a field cannot be written unquoted.
func csvSpecial(s, delim, quote string) bool {
	return strings.Contains(s, delim) || strings.Contains(s, quote) || strings.ContainsAny(s, "\r\n")
}

// csvNeedsQuotes is the "minimal" rule. It follows encoding/csv (the
// writer of export before A28) so that the default output stays
// byte-identical: quote a field that contains the delimiter, the quote
// character, CR or LF, that starts with a Unicode white-space character,
// or that is exactly `\.` (PostgreSQL's end-of-data marker). An empty
// field is never quoted.
func csvNeedsQuotes(s, delim, quote string) bool {
	if s == "" {
		return false
	}
	if s == `\.` || csvSpecial(s, delim, quote) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

// dbcc.fmt.csv(values, n, opts) -> one CSV record without line end.
// values is a list with nil for NULL, n the number of fields; opts =
// {delimiter=",", quote='"', quoting="minimal"|"all"|"nonnumeric"|"none",
// null="", numeric={…}}: "nonnumeric" leaves Lua numbers and the fields
// marked true in opts.numeric unquoted unless they contain the delimiter,
// the quote character, CR or LF.
func luaFmtCSV(L *lua.LState) int {
	vals := L.CheckTable(1)
	n := L.CheckInt(2)
	if n < 0 {
		L.ArgError(2, "dbcc.fmt.csv: the field count must not be negative")
	}
	o := parseCSVOptions(L, ensureTable(L, 3, "opts"))
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(o.delim)
		}
		v := vals.RawGetInt(i)
		if v == lua.LNil {
			b.WriteString(o.null)
			continue
		}
		var s string
		isNum := false
		switch x := v.(type) {
		case lua.LString:
			s = string(x)
		case lua.LNumber:
			s, isNum = formatNumber(float64(x)), true
		case lua.LBool:
			s = cellString(L, x)
		default:
			L.ArgError(1, fmt.Sprintf("dbcc.fmt.csv: field %d is a %s; fields must be strings, numbers, booleans or nil", i, v.Type()))
		}
		var quote bool
		switch o.quoting {
		case "all":
			quote = true
		case "nonnumeric":
			// A column counts as numeric by its type name (exec.isNumeric is a
			// substring match), so values such as PostgreSQL arrays ({1,2}),
			// ranges ([1,10)) or money ($1,234.56) arrive here too: a field
			// that cannot be written unquoted is quoted all the same.
			marked := o.numeric != nil && lua.LVAsBool(o.numeric.RawGetInt(i))
			quote = (!isNum && !marked) || csvSpecial(s, o.delim, o.quote)
		case "none":
			if csvSpecial(s, o.delim, o.quote) {
				L.RaiseError("dbcc.fmt.csv: field %d contains the delimiter, the quote character or a line break and cannot be written with quoting \"none\"", i)
			}
		default:
			quote = csvNeedsQuotes(s, o.delim, o.quote)
		}
		if !quote {
			b.WriteString(s)
			continue
		}
		b.WriteString(o.quote)
		b.WriteString(strings.ReplaceAll(s, o.quote, o.quote+o.quote))
		b.WriteString(o.quote)
	}
	L.Push(lua.LString(b.String()))
	return 1
}

// dbcc.fmt.number(text, decimal) -> the number, or nil: text read as the
// grid reads a numeric column (exec.ParseNumber); decimal "." (default)
// or "," (a decimal comma with optional grouping dots, 1.234,50).
func luaFmtNumber(L *lua.LState) int {
	text := L.CheckString(1)
	dec := L.OptString(2, ".")
	if dec != "." && dec != "," {
		L.ArgError(2, `dbcc.fmt.number: decimal must be "." or ","`)
	}
	f, ok := exec.ParseNumber(text, rune(dec[0]))
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(lua.LNumber(f))
	return 1
}

// dbcc.fmt.json(v) -> JSON text of a string, number, boolean or nil.
// Strings are escaped like encoding/json (the writer of export before
// A28), including its escaping of <, > and &.
func luaFmtJSON(L *lua.LState) int {
	switch x := L.Get(1).(type) {
	case *lua.LNilType:
		L.Push(lua.LString("null"))
	case lua.LBool:
		L.Push(lua.LString(cellString(L, x)))
	case lua.LNumber:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			L.ArgError(1, "dbcc.fmt.json: NaN and infinite numbers have no JSON form")
		}
		L.Push(lua.LString(formatNumber(f)))
	case lua.LString:
		b, _ := json.Marshal(string(x))
		L.Push(lua.LString(b))
	default:
		L.ArgError(1, "dbcc.fmt.json: only strings, numbers, booleans and nil, got "+x.Type().String())
	}
	return 1
}

// describeLua renders a Lua value for error messages.
func describeLua(v lua.LValue) string {
	switch x := v.(type) {
	case lua.LString:
		return strconv.Quote(string(x))
	case lua.LNumber, lua.LBool:
		return x.String()
	}
	return "a " + v.Type().String()
}
