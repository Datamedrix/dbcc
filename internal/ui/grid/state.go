package grid

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"dbcc/internal/exec"
)

// SortKey is one sort key (spec 10.4). Col indexes the result columns.
type SortKey struct {
	Col  int
	Desc bool
}

// FilterOp is the operator of a column filter (spec 10.5).
type FilterOp int

// Column filter operators, in the order the filter prompt lists them.
const (
	OpContains FilterOp = iota
	OpStartsWith
	OpEquals
	OpNotEquals
	OpLess
	OpLessEq
	OpGreater
	OpGreaterEq
	OpIsNull
	OpNotNull
)

// FilterOps lists all operators in prompt order.
var FilterOps = []FilterOp{OpContains, OpStartsWith, OpEquals, OpNotEquals, OpLess, OpLessEq, OpGreater, OpGreaterEq, OpIsNull, OpNotNull}

// String is the operator as shown in the prompt and the status line.
func (op FilterOp) String() string {
	switch op {
	case OpContains:
		return "contains"
	case OpStartsWith:
		return "starts with"
	case OpEquals:
		return "="
	case OpNotEquals:
		return "<>"
	case OpLess:
		return "<"
	case OpLessEq:
		return "<="
	case OpGreater:
		return ">"
	case OpGreaterEq:
		return ">="
	case OpIsNull:
		return "is NULL"
	case OpNotNull:
		return "is not NULL"
	}
	return "?"
}

// NeedsValue reports whether the operator takes a value.
func (op FilterOp) NeedsValue() bool { return op != OpIsNull && op != OpNotNull }

// ColumnFilter is a filter on one column (spec 10.5 kind 2).
type ColumnFilter struct {
	Col   int
	Op    FilterOp
	Value string
}

// State is the sort and filter state of a grid. All parts combine with AND.
type State struct {
	Sort    []SortKey
	Search  string // quick search; "" = none
	Filters []ColumnFilter
	Where   string // free WHERE (table-origin only); "" = none
}

// Active reports whether any sort or filter is set.
func (s State) Active() bool { return len(s.Sort) > 0 || s.Filtered() }

// Filtered reports whether any filter is set.
func (s State) Filtered() bool { return s.Search != "" || len(s.Filters) > 0 || s.Where != "" }

func (s State) clone() State {
	return State{Sort: slices.Clone(s.Sort), Search: s.Search, Filters: slices.Clone(s.Filters), Where: s.Where}
}

func (s State) sortRank(col int) (rank int, desc bool) {
	for i, k := range s.Sort {
		if k.Col == col {
			return i + 1, k.Desc
		}
	}
	return 0, false
}

// cycleSort implements the sort keys (spec 10.4): add=false is `s` (cycle the
// column asc → desc → off; a column that is not a key replaces the sort),
// add=true is `S` (append the column as a further key, or cycle it in place
// if it already is one).
func (s State) cycleSort(col int, add bool) State {
	n := s.clone()
	for i, k := range n.Sort {
		if k.Col != col {
			continue
		}
		if !k.Desc {
			n.Sort[i].Desc = true
		} else {
			n.Sort = slices.Delete(n.Sort, i, i+1)
		}
		return n
	}
	if add {
		n.Sort = append(n.Sort, SortKey{Col: col})
	} else {
		n.Sort = []SortKey{{Col: col}}
	}
	return n
}

// rowView is an immutable local sort and filter over rows; it implements
// exec.View (spec 10.7) and is safe for concurrent use.
type rowView struct {
	cols    []exec.ColumnInfo
	sort    []SortKey
	search  string // lower-cased
	filters []ColumnFilter
}

var _ exec.View = (*rowView)(nil)

func newRowView(cols []exec.ColumnInfo, st State) *rowView {
	return &rowView{cols: cols, sort: slices.Clone(st.Sort), search: strings.ToLower(st.Search), filters: slices.Clone(st.Filters)}
}

func (v *rowView) filtered() bool { return v.search != "" || len(v.filters) > 0 }

// Keep reports whether r passes the quick search and the column filters.
func (v *rowView) Keep(r exec.Row) bool {
	if v.search != "" {
		hit := false
		for _, c := range r {
			if s, ok := c.(string); ok && containsFold(s, v.search) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	for _, f := range v.filters {
		var c any
		if f.Col < len(r) {
			c = r[f.Col]
		}
		numeric := f.Col < len(v.cols) && v.cols[f.Col].Numeric
		if !matchFilter(c, f, numeric) {
			return false
		}
	}
	return true
}

// Sorted reports whether a local sort is set.
func (v *rowView) Sorted() bool { return len(v.sort) > 0 }

// Order returns the rows that pass Keep, stably sorted by the sort keys
// (type-aware, NULL sorts lowest, so NULLs come first in ascending order).
// rows is not modified.
func (v *rowView) Order(rows []exec.Row) []exec.Row {
	idx := v.indices(len(rows), func(i int) exec.Row { return rows[i] })
	out := make([]exec.Row, len(idx))
	for i, j := range idx {
		out[i] = rows[j]
	}
	return out
}

// indices returns the indices of rows [0,n) that pass Keep, in sort order.
func (v *rowView) indices(n int, row func(int) exec.Row) []int {
	idx := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if !v.filtered() || v.Keep(row(i)) {
			idx = append(idx, i)
		}
	}
	if len(v.sort) == 0 {
		return idx
	}
	// Precompute sort values once per kept row.
	nk := len(v.sort)
	keys := make([]sortVal, len(idx)*nk)
	for p, i := range idx {
		r := row(i)
		for k, sk := range v.sort {
			var c any
			if sk.Col < len(r) {
				c = r[sk.Col]
			}
			keys[p*nk+k] = makeSortVal(c, sk.Col < len(v.cols) && v.cols[sk.Col].Numeric)
		}
	}
	perm := make([]int, len(idx))
	for i := range perm {
		perm[i] = i
	}
	slices.SortStableFunc(perm, func(a, b int) int {
		for k, sk := range v.sort {
			c := compareVals(keys[a*nk+k], keys[b*nk+k])
			if sk.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return 0
	})
	out := make([]int, len(idx))
	for i, p := range perm {
		out[i] = idx[p]
	}
	return out
}

type sortVal struct {
	null  bool
	isNum bool
	num   float64
	s     string
	b     []byte
	isBin bool
}

func makeSortVal(c any, numeric bool) sortVal {
	switch x := c.(type) {
	case nil:
		return sortVal{null: true}
	case exec.Binary:
		return sortVal{b: x, isBin: true}
	case string:
		if numeric {
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				return sortVal{isNum: true, num: f}
			}
		}
		return sortVal{s: x}
	default:
		return sortVal{s: fmt.Sprint(x)}
	}
}

// compareVals orders NULL < numbers < strings/binary.
func compareVals(a, b sortVal) int {
	switch {
	case a.null || b.null:
		return boolCmp(!a.null, !b.null)
	case a.isNum && b.isNum:
		switch {
		case a.num < b.num:
			return -1
		case a.num > b.num:
			return 1
		}
		return 0
	case a.isNum != b.isNum:
		return boolCmp(!a.isNum, !b.isNum)
	case a.isBin || b.isBin:
		return bytes.Compare(a.bytes(), b.bytes())
	}
	return strings.Compare(a.s, b.s)
}

func (v sortVal) bytes() []byte {
	if v.isBin {
		return v.b
	}
	return []byte(v.s)
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

// matchFilter applies one column filter locally. NULL only matches is NULL.
func matchFilter(c any, f ColumnFilter, numeric bool) bool {
	switch f.Op {
	case OpIsNull:
		return c == nil
	case OpNotNull:
		return c != nil
	}
	s, ok := c.(string)
	if !ok {
		if b, isBin := c.(exec.Binary); isBin {
			s = string(b)
		} else if c == nil {
			return false
		} else {
			s = fmt.Sprint(c)
		}
	}
	switch f.Op {
	case OpContains:
		return containsFold(s, strings.ToLower(f.Value))
	case OpStartsWith:
		return strings.HasPrefix(strings.ToLower(s), strings.ToLower(f.Value))
	}
	cmp := compareVals(makeSortVal(s, numeric), makeSortVal(f.Value, numeric))
	switch f.Op {
	case OpEquals:
		return cmp == 0
	case OpNotEquals:
		return cmp != 0
	case OpLess:
		return cmp < 0
	case OpLessEq:
		return cmp <= 0
	case OpGreater:
		return cmp > 0
	case OpGreaterEq:
		return cmp >= 0
	}
	return false
}

// containsFold reports whether s contains lower (already lower-cased),
// ignoring case.
func containsFold(s, lower string) bool {
	if lower == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), lower)
}

// localView maps display rows to source rows for a local sort/filter on a
// free result. It is owned by the UI goroutine.
type localView struct {
	*rowView
	idx []int // display row → source row
	n   int   // source rows processed
}

// refresh brings the view up to date with the first n source rows and
// reports whether it changed. Filter-only views extend incrementally;
// sorted views are recomputed.
func (v *localView) refresh(n int, row func(int) exec.Row) bool {
	if n == v.n {
		return false
	}
	if n < v.n || v.Sorted() {
		v.idx = v.indices(n, row)
	} else {
		for i := v.n; i < n; i++ {
			if v.Keep(row(i)) {
				v.idx = append(v.idx, i)
			}
		}
	}
	v.n = n
	return true
}

// position returns the display row of source row src, or -1.
func (v *localView) position(src int) int {
	for i, j := range v.idx {
		if j == src {
			return i
		}
	}
	return -1
}

// filterText renders a column filter for the status line.
func filterText(name string, f ColumnFilter) string {
	if !f.Op.NeedsValue() {
		return name + " " + f.Op.String()
	}
	return name + " " + f.Op.String() + " " + strconv.Quote(f.Value)
}
