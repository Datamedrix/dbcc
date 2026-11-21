package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/exec"
)

// Footer shows the F-key labels of the focused pane, like Midnight
// Commander: "1Help 2Conn 3View …" (spec 3.6, 7).
type Footer struct {
	*tview.Box
	theme  *Theme
	labels [10]string
}

func NewFooter(theme *Theme) *Footer { return &Footer{Box: tview.NewBox(), theme: theme} }

func (f *Footer) SetLabels(l [10]string) { f.labels = l }

func (f *Footer) Draw(screen tcell.Screen) {
	f.Box.DrawForSubclass(screen, f)
	x, y, w, _ := f.GetInnerRect()
	if w <= 0 {
		return
	}
	slot := w / 10
	if slot < 3 {
		slot = 3
	}
	keyStyle := tcell.StyleDefault.Foreground(f.theme.FooterKey).Bold(true)
	labelStyle := tcell.StyleDefault.Background(tcell.ColorTeal).Foreground(f.theme.FooterLabel)
	if f.theme.Brand != tcell.ColorBlue {
		labelStyle = tcell.StyleDefault.Background(f.theme.Brand).Foreground(tcell.ColorWhite)
	}
	for i := 0; i < 10; i++ {
		cx := x + i*slot
		num := fmt.Sprint(i + 1)
		width := slot
		if i == 9 {
			width = x + w - cx
		}
		col := cx
		for _, r := range num {
			if col < cx+width {
				screen.SetContent(col, y, r, nil, keyStyle)
				col++
			}
		}
		label := []rune(f.labels[i])
		for j := 0; col < cx+width; j++ {
			r := ' '
			if j < len(label) {
				r = label[j]
			}
			if col < cx+width-1 || i == 9 {
				screen.SetContent(col, y, r, nil, labelStyle)
			} else {
				screen.SetContent(col, y, ' ', nil, tcell.StyleDefault)
			}
			col++
		}
	}
}

// StatusInfo is what the status line shows (spec 7).
type StatusInfo struct {
	Profile  string
	ReadOnly bool
	Manual   bool // manual transactions
	TxnOpen  bool
	Rows     string // "1,204+ rows", "partial", "capped"
	Duration time.Duration
	Sort     string
	Filter   string
	Hint     string // "sorted within N loaded rows" …
	Busy     bool
	Message  string // transient message (errors, notes)
	IsError  bool
}

// StatusLine renders StatusInfo on one line.
type StatusLine struct {
	*tview.TextView
	theme *Theme
	info  StatusInfo
}

func NewStatusLine(theme *Theme) *StatusLine {
	s := &StatusLine{TextView: tview.NewTextView().SetDynamicColors(true).SetWrap(false), theme: theme}
	s.SetBackgroundColor(theme.StatusBg)
	s.SetTextColor(theme.StatusFg)
	return s
}

func (s *StatusLine) Set(info StatusInfo) {
	s.info = info
	var parts []string
	if info.Profile == "" {
		parts = append(parts, "not connected")
	} else {
		parts = append(parts, tview.Escape(info.Profile))
	}
	if info.ReadOnly {
		parts = append(parts, "[::b]READ ONLY[::-]")
	}
	switch {
	case info.Manual && info.TxnOpen:
		parts = append(parts, "[::b]TXN OPEN[::-]")
	case info.Manual:
		parts = append(parts, "manual txn")
	case info.Profile != "":
		parts = append(parts, "autocommit")
	}
	if info.Busy {
		parts = append(parts, "running… (F8 cancels)")
	}
	if info.Rows != "" {
		parts = append(parts, info.Rows)
	}
	if info.Duration > 0 {
		parts = append(parts, FormatDuration(info.Duration))
	}
	if info.Sort != "" {
		parts = append(parts, "sort: "+tview.Escape(info.Sort))
	}
	if info.Filter != "" {
		parts = append(parts, "filter: "+tview.Escape(info.Filter))
	}
	if info.Hint != "" {
		parts = append(parts, tview.Escape(info.Hint))
	}
	if info.Message != "" {
		m := tview.Escape(oneLine(info.Message))
		if info.IsError {
			m = "[::b]" + m + "[::-]"
		}
		parts = append(parts, m)
	}
	s.SetText(" " + strings.Join(parts, " | "))
}

func (s *StatusLine) Info() StatusInfo { return s.info }

// FormatDuration renders 38 ms, 1.2 s, 2m03s.
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// MessageLog is the Messages tab: rows affected, duration, warnings,
// generated statements (spec 3.3, 9.4). Horizontally scrollable, no wrap.
type MessageLog struct {
	*tview.TextView
	theme *Theme
	count int
}

func NewMessageLog(theme *Theme) *MessageLog {
	l := &MessageLog{TextView: tview.NewTextView().SetDynamicColors(true).SetWrap(false).SetScrollable(true), theme: theme}
	l.SetMaxLines(5000)
	return l
}

// Add appends one entry.
func (l *MessageLog) Add(m exec.Message) {
	l.count++
	ts := m.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[gray]%s[-] ", ts.Format("15:04:05"))
	if m.Line > 0 {
		fmt.Fprintf(&b, "[gray]line %d[-] ", m.Line)
	}
	if m.Stmt != "" {
		b.WriteString(tview.Escape(exec.Shorten(m.Stmt, 80)))
		b.WriteString("  ")
	}
	switch {
	case m.Err != nil:
		fmt.Fprintf(&b, "[red::b]ERROR[-::-] [red]%s[-]", tview.Escape(oneLine(m.Err.Error())))
	case m.Returned >= 0:
		fmt.Fprintf(&b, "[green]%s rows[-]", exec.FormatCount(m.Returned))
	case m.Affected >= 0:
		fmt.Fprintf(&b, "[green]%s rows affected[-]", exec.FormatCount(int(m.Affected)))
	case m.Stmt != "":
		b.WriteString("[green]ok[-]")
	}
	if m.Duration > 0 {
		fmt.Fprintf(&b, " [gray](%s)[-]", FormatDuration(m.Duration))
	}
	if m.Info != "" {
		if m.Stmt != "" {
			b.WriteString("\n         ")
		}
		b.WriteString(tview.Escape(m.Info))
	}
	if m.ImplicitCommit {
		b.WriteString("\n         [yellow]note: this statement committed the open transaction implicitly[-]")
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(&b, "\n         [yellow]%s[-]", tview.Escape(w))
	}
	b.WriteString("\n")
	fmt.Fprint(l, b.String())
	l.ScrollToEnd()
}

// Count returns the number of entries.
func (l *MessageLog) Count() int { return l.count }
