// Package splash renders the splash screen and the About page (spec 3.7,
// 7.1). One component serves both: the splash closes after a timer or on any
// key, the About page on Esc. On the About page, L switches to a scrollable
// view of the third-party licence notices (A16); L or Esc returns.
package splash

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"dbcc/internal/branding"
)

// Mode selects splash or About behaviour.
type Mode int

const (
	// ModeSplash closes after the timer (Start) or on any key.
	ModeSplash Mode = iota
	// ModeAbout has no timer and closes on Esc.
	ModeAbout
)

// Duration is how long the splash screen stays (spec 7.1).
const Duration = 3 * time.Second

// TextOnlyWord is shown instead of the logo when the terminal is smaller
// than the compact logo plus the text.
const TextOnlyWord = "Datamedrix"

// Splash is a tview.Primitive showing logo, title, version, company and
// contact, centred.
type Splash struct {
	*tview.Box
	mode Mode
	done func()

	once  sync.Once
	mu    sync.Mutex
	timer *time.Timer

	// Licence view (About mode only); touched on the UI goroutine only.
	licences  bool
	scroll    int      // first visible content line
	page      int      // content rows at the last draw
	maxScroll int      // at the last draw
	wrapW     int      // width the wrapped lines were made for
	wrapped   []string // ThirdPartyLicenses wrapped to wrapW
}

// New creates the component. done is called once, when the splash times out
// or is dismissed, or when the About page is closed with Esc.
func New(mode Mode, done func()) *Splash {
	return &Splash{Box: tview.NewBox(), mode: mode, done: done}
}

// Mode returns the mode.
func (s *Splash) Mode() Mode { return s.mode }

// Start starts the timer of a splash screen (no-op in About mode). When d
// elapses, the close is handed to queue, typically app.QueueUpdateDraw, so
// done runs on the UI goroutine. d <= 0 means Duration. A nil queue calls
// done directly on the timer goroutine.
func (s *Splash) Start(queue func(func()), d time.Duration) {
	if s.mode != ModeSplash {
		return
	}
	if d <= 0 {
		d = Duration
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(d, func() {
		if queue != nil {
			queue(s.Close)
		} else {
			s.Close()
		}
	})
}

// Close stops the timer and calls done (once).
func (s *Splash) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Stop()
		}
		s.mu.Unlock()
		if s.done != nil {
			s.done()
		}
	})
}

// ShowingLicences reports whether the About page shows the licence view.
func (s *Splash) ShowingLicences() bool { return s.licences }

// InputHandler: on the splash any key dismisses it early. On the About page
// Esc closes it and L opens the licence view; there the cursor keys scroll
// and L or Esc return to the About content.
func (s *Splash) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return s.WrapInputHandler(func(event *tcell.EventKey, _ func(p tview.Primitive)) {
		if s.mode == ModeSplash {
			s.Close()
			return
		}
		isL := event.Key() == tcell.KeyRune && (event.Rune() == 'l' || event.Rune() == 'L')
		if !s.licences {
			switch {
			case event.Key() == tcell.KeyEscape:
				s.Close()
			case isL:
				s.licences, s.scroll = true, 0
			}
			return
		}
		page := max(1, s.page-1)
		switch event.Key() {
		case tcell.KeyEscape:
			s.licences = false
		case tcell.KeyUp:
			s.scroll--
		case tcell.KeyDown:
			s.scroll++
		case tcell.KeyPgUp:
			s.scroll -= page
		case tcell.KeyPgDn:
			s.scroll += page
		case tcell.KeyHome:
			s.scroll = 0
		case tcell.KeyEnd:
			s.scroll = s.maxScroll
		default:
			if isL {
				s.licences = false
			}
		}
		s.scroll = max(0, min(s.scroll, s.maxScroll))
	})
}

// Logo variants chosen by Layout.
const (
	LogoWide = iota
	LogoCompact
	LogoText
)

// textLines are the lines below the logo; "" is a blank spacer.
func textLines() []string {
	version := "Version " + branding.Version
	if branding.BuildDate != "" {
		version += " (" + branding.BuildDate + ")"
	}
	return []string{
		branding.Title,
		branding.Subtitle,
		version,
		"",
		branding.Company,
		"",
		branding.Contact,
		branding.Email,
		branding.Web,
	}
}

func logoLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func blockWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, uniseg.StringWidth(l))
	}
	return w
}

// Layout returns which logo fits a width x height area and the lines to
// draw (logo lines first, then a blank line, then the text). The text needs
// textLines() rows plus the logo plus one spacer row.
func Layout(width, height int) (variant int, logo, text []string) {
	text = textLines()
	textW := blockWidth(text)
	fits := func(l []string) bool {
		return blockWidth(l) <= width && textW <= width && len(l)+1+len(text) <= height
	}
	if l := logoLines(branding.LogoWide); fits(l) {
		return LogoWide, l, text
	}
	if l := logoLines(branding.LogoCompact); fits(l) {
		return LogoCompact, l, text
	}
	return LogoText, []string{TextOnlyWord}, text
}

// Licence hints on the About page, longest first; the first that fits the
// width is drawn below the text, or none when rows are short.
var licenceHints = []string{"Third-party licences: L", "Licences: L"}

// aboutHint returns the lines added below the About text in a width x
// height area whose layout already uses used rows: a spacer and the hint,
// only the hint, or nothing.
func aboutHint(width, height, used int) []string {
	free := height - used
	for _, h := range licenceHints {
		if uniseg.StringWidth(h) > width {
			continue
		}
		switch {
		case free >= 2:
			return []string{"", h}
		case free == 1:
			return []string{h}
		}
		return nil
	}
	return nil
}

// colour reports whether the screen gets the brand colour (true-colour and
// 256-colour terminals) or monochrome (spec 7.1).
func colour(screen tcell.Screen) bool { return screen.Colors() >= 256 }

// Draw draws the component centred in its rectangle.
func (s *Splash) Draw(screen tcell.Screen) {
	s.Box.DrawForSubclass(screen, s)
	x, y, width, height := s.GetInnerRect()
	if width <= 0 || height <= 0 {
		return
	}
	base := tcell.StyleDefault.Background(s.GetBackgroundColor())
	brand := base.Bold(true)
	if colour(screen) {
		brand = brand.Foreground(tcell.GetColor(branding.BrandColor))
	}
	if s.mode == ModeAbout && s.licences {
		s.drawLicences(screen, x, y, width, height, base, brand)
		return
	}
	variant, logo, text := Layout(width, height)
	nText := len(text)
	if s.mode == ModeAbout {
		text = append(text, aboutHint(width, height, len(logo)+1+len(text))...)
	}

	total := len(logo) + 1 + len(text)
	top := y + max(0, (height-total)/2)
	row := top

	// The logo is centred as a block, so its lines stay aligned.
	lw := blockWidth(logo)
	lx := x + max(0, (width-lw)/2)
	for _, l := range logo {
		if variant == LogoText {
			putCentered(screen, x, width, row, l, brand, y+height)
		} else {
			put(screen, lx, row, l, brand, x+width, y+height)
		}
		row++
	}
	row++
	for i, l := range text {
		st := base
		switch i {
		case 0: // title
			st = brand
		case 1: // subtitle
			st = base.Bold(true)
		}
		if i >= nText { // licence hint
			st = base.Dim(true)
		}
		putCentered(screen, x, width, row, l, st, y+height)
		row++
	}
}

// Licence view content width: wide enough for the summary table, narrow
// enough to read; wider lines are wrapped.
const licenceWidth = 88

// drawLicences draws the licence view: a header row, the scrolled text and
// a key-hint footer (dropped below 4 rows).
func (s *Splash) drawLicences(screen tcell.Screen, x, y, width, height int, base, brand tcell.Style) {
	cw := min(width, licenceWidth)
	if s.wrapW != cw || s.wrapped == nil {
		s.wrapW, s.wrapped = cw, wrapText(branding.ThirdPartyLicenses, cw)
	}
	footer := height >= 4
	rows := height - 1
	if footer {
		rows--
	}
	rows = max(rows, 0)
	s.page = rows
	s.maxScroll = max(0, len(s.wrapped)-rows)
	s.scroll = max(0, min(s.scroll, s.maxScroll))

	left := x + (width-cw)/2
	maxX, maxY := x+width, y+height
	title := "Third-party licences"
	pos := fmt.Sprintf("%d-%d/%d", min(s.scroll+1, len(s.wrapped)), min(s.scroll+rows, len(s.wrapped)), len(s.wrapped))
	put(screen, left, y, title, brand, maxX, maxY)
	if pw := uniseg.StringWidth(pos); uniseg.StringWidth(title)+1+pw <= cw {
		put(screen, left+cw-pw, y, pos, base.Dim(true), maxX, maxY)
	}
	for i := 0; i < rows && s.scroll+i < len(s.wrapped); i++ {
		put(screen, left, y+1+i, s.wrapped[s.scroll+i], base, maxX, maxY)
	}
	if footer {
		hint := "Up/Down PgUp/PgDn Home/End: scroll   L/Esc: back"
		if uniseg.StringWidth(hint) > cw {
			hint = "L/Esc: back"
		}
		put(screen, left, y+height-1, hint, base.Dim(true), maxX, maxY)
	}
}

// wrapText splits text into lines of at most width cells, breaking long
// lines at the last space that fits (or hard when there is none).
func wrapText(text string, width int) []string {
	width = max(width, 1)
	var out []string
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		for uniseg.StringWidth(l) > width {
			cut, lastSpace, w := 0, -1, 0
			state := -1
			rest := l
			for len(rest) > 0 {
				var cluster string
				var cwid int
				cluster, rest, cwid, state = uniseg.FirstGraphemeClusterInString(rest, state)
				if w+cwid > width {
					break
				}
				w += cwid
				if cluster == " " {
					lastSpace = cut
				}
				cut += len(cluster)
			}
			if cut == 0 { // a cluster wider than the line
				_, rest, _, _ := uniseg.FirstGraphemeClusterInString(l, -1)
				cut = len(l) - len(rest)
			}
			if lastSpace > 0 {
				out = append(out, strings.TrimRight(l[:lastSpace], " "))
				l = strings.TrimLeft(l[lastSpace:], " ")
			} else {
				out = append(out, l[:cut])
				l = l[cut:]
			}
		}
		out = append(out, l)
	}
	return out
}

func putCentered(screen tcell.Screen, x, width, y int, s string, st tcell.Style, maxY int) {
	w := uniseg.StringWidth(s)
	put(screen, x+max(0, (width-w)/2), y, s, st, x+width, maxY)
}

// put draws s at (x, y), clipped to x < maxX and y < maxY.
func put(screen tcell.Screen, x, y int, s string, st tcell.Style, maxX, maxY int) {
	if y >= maxY {
		return
	}
	state := -1
	for len(s) > 0 {
		var cluster string
		var w int
		cluster, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		if x+w > maxX {
			return
		}
		runes := []rune(cluster)
		if runes[0] != ' ' {
			screen.SetContent(x, y, runes[0], runes[1:], st)
		}
		x += w
	}
}
