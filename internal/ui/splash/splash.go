// Package splash renders the splash screen and the About page (spec 3.7,
// 7.1). One component serves both: the splash closes after a timer or on any
// key, the About page on Esc.
package splash

import (
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

// InputHandler: on the splash any key dismisses it early; on the About
// page Esc closes it.
func (s *Splash) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return s.WrapInputHandler(func(event *tcell.EventKey, _ func(p tview.Primitive)) {
		if s.mode == ModeSplash || event.Key() == tcell.KeyEscape {
			s.Close()
		}
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
	variant, logo, text := Layout(width, height)

	base := tcell.StyleDefault.Background(s.GetBackgroundColor())
	brand := base.Bold(true)
	if colour(screen) {
		brand = brand.Foreground(tcell.GetColor(branding.BrandColor))
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
		putCentered(screen, x, width, row, l, st, y+height)
		row++
	}
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
