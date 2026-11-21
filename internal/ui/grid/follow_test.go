package grid

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/exec"
)

// followingResult is a following result with ids 1..n that keeps cap rows
// (polling is not started; the test appends like the follower does).
func followingResult(t *testing.T, n, cap int) *exec.Result {
	t.Helper()
	rows := make([]exec.Row, n)
	for i := range rows {
		rows[i] = exec.Row{strconv.Itoa(i + 1)}
	}
	r := exec.NewResult("logs", []exec.ColumnInfo{{Name: "id", Numeric: true}}, rows)
	err := r.SetFollow(exec.FollowSpec{SQL: "unused", Key: "id", Cap: cap,
		Conn: func(context.Context) (exec.Querier, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func appendIDs(r *exec.Result, from, to int) {
	var rows []exec.Row
	for i := from; i <= to; i++ {
		rows = append(rows, exec.Row{strconv.Itoa(i)})
	}
	r.Append(rows)
}

func currentID(t *testing.T, g *Grid) string {
	t.Helper()
	row, ok := g.CurrentRow()
	if !ok {
		t.Fatal("no current row")
	}
	return row[0].(string)
}

func TestFollowCursorOnLastRowFollowsTheEnd(t *testing.T) {
	r := followingResult(t, 3, 100)
	g := New(r, Options{})
	s := newScreen(t, 30, 12)
	draw(s, g)
	key(g, tcell.KeyEnd)
	if id := currentID(t, g); id != "3" {
		t.Fatalf("End: %s", id)
	}
	appendIDs(r, 4, 20) // more than fits on the screen
	draw(s, g)
	if id := currentID(t, g); id != "20" {
		t.Errorf("cursor on %s after append, want the new last row 20", id)
	}
	if !strings.Contains(line(s, 11), "20") {
		t.Errorf("the last row is not visible:\n%s", line(s, 11))
	}
}

func TestFollowCursorElsewhereStays(t *testing.T) {
	r := followingResult(t, 3, 100)
	g := New(r, Options{})
	s := newScreen(t, 30, 12)
	draw(s, g)
	key(g, tcell.KeyDown) // id 2
	appendIDs(r, 4, 6)
	draw(s, g)
	if id := currentID(t, g); id != "2" {
		t.Errorf("cursor moved to %s", id)
	}
}

func TestFollowCursorKeepsRecordWhenOldRowsAreDropped(t *testing.T) {
	r := followingResult(t, 5, 5)
	g := New(r, Options{})
	s := newScreen(t, 30, 12)
	draw(s, g)
	key(g, tcell.KeyDown)
	key(g, tcell.KeyDown)
	key(g, tcell.KeyDown) // id 4
	appendIDs(r, 6, 7)    // drops ids 1 and 2
	draw(s, g)
	if id := currentID(t, g); id != "4" {
		t.Errorf("cursor on %s, want record 4", id)
	}
	if st := g.Status(); !strings.HasPrefix(st, "rows 3–7 · following · 0 new rows") {
		t.Errorf("status %q", st)
	}
	key(g, tcell.KeyEnd)
	appendIDs(r, 8, 12) // drops everything shown before
	draw(s, g)
	if id := currentID(t, g); id != "12" {
		t.Errorf("cursor on %s, want the last row 12", id)
	}
}

func TestFollowHasNoPages(t *testing.T) {
	r := followingResult(t, 3, 100)
	g := New(r, Options{PageSize: 10})
	for _, a := range []string{"next_page", "prev_page"} {
		if !g.Action(a) {
			t.Fatalf("%s not handled", a)
		}
		if st := g.Status(); !strings.Contains(st, exec.ErrFollowing.Error()) {
			t.Errorf("%s: status %q", a, st)
		}
	}
}
