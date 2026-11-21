package editor

import (
	"strconv"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"
)

func (e *Editor) focused() bool { return e.HasFocus() || e.drawFocused }

func (e *Editor) onBg(st tcell.Style) tcell.Style {
	_, bg, _ := st.Decompose()
	if bg == tcell.ColorDefault {
		st = st.Background(e.GetBackgroundColor())
	}
	return st
}

func digits(n int) int { return len(strconv.Itoa(n)) }

// Draw implements tview.Primitive.
func (e *Editor) Draw(screen tcell.Screen) {
	e.Box.DrawForSubclass(screen, e)
	x, y, w, h := e.GetInnerRect()
	if w <= 0 || h <= 0 {
		return
	}
	numW := digits(len(e.lines))
	if numW < 2 {
		numW = 2
	}
	gutterW := numW + 2 // marker, number, space
	if w < gutterW+4 {
		gutterW = 0
	}
	tx, tw := x+gutterW, w-gutterW
	e.textX, e.textY, e.textW, e.textH = tx, y, tw, h
	e.scroll()

	hl := e.highlight()
	st := e.styles
	selA, selB, hasSel := e.selRange()

	stmtRow0, stmtRow1, stmtStart, stmtEnd := -1, -2, -1, -1
	if stmt, ok := e.CurrentStatement(); ok {
		stmtStart, stmtEnd = stmt.Start, stmt.End
		last := stmt.End - 1
		if last < stmt.Start {
			last = stmt.Start
		}
		stmtRow0, stmtRow1 = e.posOf(stmt.Start).row, e.posOf(last).row
	}

	for i := 0; i < h; i++ {
		row := e.rowOff + i
		sy := y + i
		if row >= len(e.lines) {
			break
		}
		inStmt := row >= stmtRow0 && row <= stmtRow1
		if gutterW > 0 {
			if inStmt {
				screen.SetContent(x, sy, st.MarkerRune, nil, e.onBg(st.Marker))
			}
			num := strconv.Itoa(row + 1)
			gs := st.Gutter
			if row == e.cur.row {
				gs = st.GutterCurrent
			}
			for j, c := range num {
				screen.SetContent(x+1+numW-len(num)+j, sy, c, nil, e.onBg(gs))
			}
		}
		line := e.lines[row]
		lineOff := e.offsetOf(pos{row, 0})
		dx, b, state := 0, 0, -1
		for b < len(line) && dx < e.colOff+tw {
			c, _, cw0, ns := uniseg.FirstGraphemeClusterInString(line[b:], state)
			state = ns
			cw := e.clusterWidth(c, cw0, dx)
			style := st.Text
			if b < len(hl[row]) {
				style = hl[row][b]
			}
			if st.StatementBg != tcell.ColorDefault && lineOff+b >= stmtStart && lineOff+b < stmtEnd {
				if _, bg, _ := style.Decompose(); bg == tcell.ColorDefault {
					style = style.Background(st.StatementBg)
				}
			}
			p := pos{row, b}
			if hasSel && !p.less(selA) && p.less(selB) {
				style = st.Selection
			}
			style = e.onBg(style)
			if dx+cw > e.colOff {
				blank := c == "\t" || cw0 <= 0 || dx < e.colOff || dx+cw > e.colOff+tw
				if blank {
					for k := 0; k < cw; k++ {
						if cx := dx + k - e.colOff; cx >= 0 && cx < tw {
							r := ' '
							if c != "\t" && cw0 <= 0 && k == 0 {
								r = '?'
							}
							screen.SetContent(tx+cx, sy, r, nil, style)
						}
					}
				} else {
					rs := []rune(c)
					screen.SetContent(tx+dx-e.colOff, sy, rs[0], rs[1:], style)
				}
			}
			dx += cw
			b += len(c)
		}
		// selected line break
		if hasSel && row >= selA.row && row < selB.row && dx >= e.colOff && dx < e.colOff+tw {
			screen.SetContent(tx+dx-e.colOff, sy, ' ', nil, st.Selection)
		}
	}
	if e.focused() {
		cx, cy := e.CursorScreenPos()
		if cx >= tx && cx < tx+tw && cy >= y && cy < y+h {
			screen.ShowCursor(cx, cy)
		}
	}
}

// scroll keeps the cursor visible (only after cursor-moving commands, so
// mouse-wheel scrolling is not undone).
func (e *Editor) scroll() {
	if max := len(e.lines) - 1; e.rowOff > max {
		e.rowOff = max
	}
	if !e.follow {
		return
	}
	e.follow = false
	h, tw := e.textH, e.textW
	if e.cur.row < e.rowOff {
		e.rowOff = e.cur.row
	}
	if e.cur.row >= e.rowOff+h {
		e.rowOff = e.cur.row - h + 1
	}
	dc := e.displayCol(e.cur.row, e.cur.col)
	if dc < e.colOff {
		e.colOff = dc
	}
	if dc >= e.colOff+tw {
		e.colOff = dc - tw + 1
	}
	if e.colOff < 0 {
		e.colOff = 0
	}
}

// ScrollOffset returns the first visible line (0-based) and the first
// visible display column.
func (e *Editor) ScrollOffset() (row, col int) { return e.rowOff, e.colOff }

// MouseHandler implements tview.Primitive: click places the cursor, drag
// selects, the wheel scrolls.
func (e *Editor) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return e.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		mx, my := ev.Position()
		if !e.InRect(mx, my) {
			return false, nil
		}
		at := func() pos {
			row := e.rowOff + my - e.textY
			if row < 0 {
				row = 0
			}
			if row >= len(e.lines) {
				row = len(e.lines) - 1
			}
			return pos{row, e.colForX(row, mx-e.textX+e.colOff)}
		}
		switch action {
		case tview.MouseLeftDown:
			setFocus(e)
			e.moveTo(at(), false)
			e.selecting, e.anchor = true, e.cur
			e.resetCmd()
			return true, e
		case tview.MouseMove:
			if ev.Buttons()&tcell.Button1 != 0 {
				e.cur = e.clamp(at())
				e.follow = true
				return true, e
			}
		case tview.MouseLeftUp:
			return true, nil
		case tview.MouseScrollUp:
			if e.rowOff > 0 {
				e.rowOff--
			}
			return true, nil
		case tview.MouseScrollDown:
			if e.rowOff < len(e.lines)-1 {
				e.rowOff++
			}
			return true, nil
		}
		return false, nil
	})
}
