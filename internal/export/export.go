// Package export writes results as aligned text, CSV, TSV or JSON.
package export

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rivo/uniseg"
)

// Value semantics follow internal/exec: nil = NULL, string, or a value
// implementing fmt.Stringer / []byte (binary). Writers take plain data so
// this package does not depend on exec.

// Cell renders one value for text formats.
type Cell func(v any) string

// Writer streams rows in a format.
type Writer interface {
	Header(cols []string) error
	Row(vals []any) error
	Close() error
}

// Format names.
const (
	Table = "table"
	CSV   = "csv"
	TSV   = "tsv"
	JSON  = "json"
)

// New returns a writer for format. numeric marks right-aligned columns
// for the table format (may be nil). cell renders values for text output.
func New(format string, w io.Writer, numeric []bool, cell Cell) (Writer, error) {
	switch format {
	case "", Table:
		return &tableWriter{w: w, numeric: numeric, cell: cell}, nil
	case CSV:
		return &csvWriter{c: csv.NewWriter(w), cell: cell}, nil
	case TSV:
		cw := csv.NewWriter(w)
		cw.Comma = '\t'
		return &csvWriter{c: cw, cell: cell, tsv: true}, nil
	case JSON:
		return &jsonWriter{w: bufio.NewWriter(w), cell: cell}, nil
	}
	return nil, fmt.Errorf("unknown format %q (table, csv, tsv, json)", format)
}

// FormatFromFile picks a format from a file extension (.csv, .json, .tsv).
func FormatFromFile(name string) (string, error) {
	low := strings.ToLower(name)
	switch {
	case strings.HasSuffix(low, ".csv"):
		return CSV, nil
	case strings.HasSuffix(low, ".tsv"):
		return TSV, nil
	case strings.HasSuffix(low, ".json"):
		return JSON, nil
	}
	return "", fmt.Errorf("unknown export format for %q: use .csv or .json", name)
}

// ---- aligned text table (buffers rows to compute widths) ----

type tableWriter struct {
	w       io.Writer
	numeric []bool
	cell    Cell
	header  []string
	rows    [][]string
}

func (t *tableWriter) Header(cols []string) error { t.header = cols; return nil }

func (t *tableWriter) Row(vals []any) error {
	r := make([]string, len(vals))
	for i, v := range vals {
		r[i] = oneLine(t.cell(v))
	}
	t.rows = append(t.rows, r)
	return nil
}

func (t *tableWriter) Close() error {
	_, err := io.WriteString(t.w, FormatTable(t.header, t.rows, t.numeric))
	return err
}

// FormatTable renders an aligned text table with a header separator.
func FormatTable(header []string, rows [][]string, numeric []bool) string {
	n := len(header)
	for _, r := range rows {
		if len(r) > n {
			n = len(r)
		}
	}
	width := make([]int, n)
	for i, h := range header {
		width[i] = Width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if w := Width(c); w > width[i] {
				width[i] = w
			}
		}
	}
	var b strings.Builder
	line := func(cells []string, isHeader bool) {
		for i := 0; i < n; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if i > 0 {
				b.WriteString(" | ")
			}
			pad := strings.Repeat(" ", width[i]-Width(c))
			right := !isHeader && i < len(numeric) && numeric[i]
			if right {
				b.WriteString(pad + c)
			} else if i == n-1 {
				b.WriteString(c)
			} else {
				b.WriteString(c + pad)
			}
		}
		b.WriteString("\n")
	}
	if len(header) > 0 {
		line(header, true)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString("-+-")
			}
			b.WriteString(strings.Repeat("-", width[i]))
		}
		b.WriteString("\n")
	}
	for _, r := range rows {
		line(r, false)
	}
	return b.String()
}

// Width is the display width of s in terminal cells.
func Width(s string) int { return uniseg.StringWidth(s) }

func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n\t") {
		return s
	}
	r := strings.NewReplacer("\r\n", "↵", "\n", "↵", "\r", "↵", "\t", " ")
	return r.Replace(s)
}

// ---- CSV / TSV ----

type csvWriter struct {
	c    *csv.Writer
	cell Cell
	tsv  bool
}

func (c *csvWriter) Header(cols []string) error { return c.c.Write(cols) }

func (c *csvWriter) Row(vals []any) error {
	r := make([]string, len(vals))
	for i, v := range vals {
		if v == nil {
			r[i] = "" // NULL is an empty field
			continue
		}
		r[i] = c.cell(v)
		if c.tsv {
			r[i] = oneLine(r[i])
		}
	}
	return c.c.Write(r)
}

func (c *csvWriter) Close() error { c.c.Flush(); return c.c.Error() }

// ---- JSON: an array of objects ----

type jsonWriter struct {
	w     *bufio.Writer
	cell  Cell
	cols  []string
	count int
}

func (j *jsonWriter) Header(cols []string) error {
	j.cols = cols
	_, err := j.w.WriteString("[")
	return err
}

func (j *jsonWriter) Row(vals []any) error {
	if j.count > 0 {
		j.w.WriteString(",")
	}
	j.count++
	j.w.WriteString("\n  {")
	for i, v := range vals {
		if i > 0 {
			j.w.WriteString(", ")
		}
		k, _ := json.Marshal(j.cols[i])
		j.w.Write(k)
		j.w.WriteString(": ")
		var val []byte
		if v == nil {
			val = []byte("null")
		} else {
			val, _ = json.Marshal(j.cell(v))
		}
		j.w.Write(val)
	}
	_, err := j.w.WriteString("}")
	return err
}

func (j *jsonWriter) Close() error {
	if j.count > 0 {
		j.w.WriteString("\n")
	}
	j.w.WriteString("]\n")
	return j.w.Flush()
}
