package export

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Stream writes a block of rows and then further rows as they arrive
// (headless tail -f). Rows written before the first Flush form the first
// block; every Flush writes out what is pending.
//
//   - table: the first Flush prints the aligned table (header and rows);
//     later rows are printed without repeating the header, aligned to the
//     column widths of the first block (a longer value widens only its line).
//   - csv, tsv: the header, then one record per row.
//   - json: JSON Lines (one object per line, no enclosing array), because
//     a JSON array could only be closed at the end.
type Stream interface {
	Writer
	Flush() error
}

// NewStream returns a Stream for format (see Stream); numeric and cell as
// for New.
func NewStream(format string, w io.Writer, numeric []bool, cell Cell) (Stream, error) {
	switch format {
	case "", Table:
		return &tableStream{w: w, numeric: numeric, cell: cell}, nil
	case CSV, TSV:
		cw := csv.NewWriter(w)
		if format == TSV {
			cw.Comma = '\t'
		}
		return &csvStream{csvWriter{c: cw, cell: cell, tsv: format == TSV}}, nil
	case JSON:
		return &jsonLines{w: bufio.NewWriter(w), cell: cell}, nil
	}
	return nil, fmt.Errorf("unknown format %q (table, csv, tsv, json)", format)
}

type tableStream struct {
	w       io.Writer
	numeric []bool
	cell    Cell
	header  []string
	rows    [][]string
	widths  []int // set by the first Flush
}

func (t *tableStream) Header(cols []string) error { t.header = cols; return nil }

func (t *tableStream) Row(vals []any) error {
	r := make([]string, len(vals))
	for i, v := range vals {
		r[i] = oneLine(t.cell(v))
	}
	t.rows = append(t.rows, r)
	return nil
}

func (t *tableStream) Flush() error {
	var out string
	if t.widths == nil {
		out = FormatTable(t.header, t.rows, t.numeric)
		t.widths = make([]int, len(t.header))
		for i, h := range t.header {
			t.widths[i] = Width(h)
		}
		for _, r := range t.rows {
			for i, c := range r {
				if i < len(t.widths) {
					t.widths[i] = max(t.widths[i], Width(c))
				}
			}
		}
	} else {
		var b strings.Builder
		for _, r := range t.rows {
			for i, c := range r {
				if i > 0 {
					b.WriteString(" | ")
				}
				w := 0
				if i < len(t.widths) {
					w = t.widths[i]
				}
				pad := strings.Repeat(" ", max(w-Width(c), 0))
				switch {
				case i < len(t.numeric) && t.numeric[i]:
					b.WriteString(pad + c)
				case i == len(r)-1:
					b.WriteString(c)
				default:
					b.WriteString(c + pad)
				}
			}
			b.WriteString("\n")
		}
		out = b.String()
	}
	t.rows = nil
	_, err := io.WriteString(t.w, out)
	return err
}

func (t *tableStream) Close() error { return t.Flush() }

type csvStream struct{ csvWriter }

func (c *csvStream) Flush() error { c.c.Flush(); return c.c.Error() }

type jsonLines struct {
	w    *bufio.Writer
	cell Cell
	cols []string
}

func (j *jsonLines) Header(cols []string) error { j.cols = cols; return nil }

func (j *jsonLines) Row(vals []any) error {
	j.w.WriteString("{")
	for i, v := range vals {
		if i > 0 {
			j.w.WriteString(", ")
		}
		k, _ := json.Marshal(j.cols[i])
		j.w.Write(k)
		j.w.WriteString(": ")
		val := []byte("null")
		if v != nil {
			val, _ = json.Marshal(j.cell(v))
		}
		j.w.Write(val)
	}
	_, err := j.w.WriteString("}\n")
	return err
}

func (j *jsonLines) Flush() error { return j.w.Flush() }
func (j *jsonLines) Close() error { return j.w.Flush() }
