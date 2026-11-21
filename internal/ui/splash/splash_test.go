package splash

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"dbcc/internal/branding"
)

// colorScreen overrides the colour count of the simulation screen (which
// always reports 256).
type colorScreen struct {
	tcell.SimulationScreen
	colors int
}

func (c colorScreen) Colors() int { return c.colors }

func newScreen(t *testing.T, w, h, colors int) colorScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	return colorScreen{s, colors}
}

func render(s colorScreen, p tview.Primitive) {
	w, h := s.Size()
	s.Clear()
	p.SetRect(0, 0, w, h)
	p.Draw(s)
	s.Show()
}

func line(s tcell.Screen, y int) string {
	w, _ := s.Size()
	var b strings.Builder
	for x := 0; x < w; {
		str, _, width := s.Get(x, y)
		if str == "" {
			str = " "
		}
		b.WriteString(str)
		x += max(width, 1)
	}
	return b.String()
}

func lines(s tcell.Screen) []string {
	_, h := s.Size()
	out := make([]string, h)
	for y := range out {
		out[y] = line(s, y)
	}
	return out
}

// find returns the screen position of text.
func find(t *testing.T, s tcell.Screen, text string) (x, y int) {
	t.Helper()
	for y, l := range lines(s) {
		if i := strings.Index(l, text); i >= 0 {
			return uniseg.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, strings.Join(lines(s), "\n"))
	return 0, 0
}

// assertCentred checks that text is horizontally centred (±1) in width w.
func assertCentred(t *testing.T, s tcell.Screen, text string, w int) {
	t.Helper()
	x, _ := find(t, s, text)
	tw := uniseg.StringWidth(text)
	left, right := x, w-x-tw
	if d := left - right; d < -1 || d > 1 {
		t.Errorf("%q not centred: left %d right %d", text, left, right)
	}
}

func firstLogoLine(logo string) string {
	return strings.TrimSpace(strings.SplitN(logo, "\n", 2)[0])
}

func TestWideAt120x40(t *testing.T) {
	s := newScreen(t, 120, 40, 1<<24)
	render(s, New(ModeSplash, nil))
	for _, txt := range []string{
		branding.Title, branding.Subtitle, "Version " + branding.Version,
		branding.Company, branding.Contact, branding.Email, branding.Web,
	} {
		assertCentred(t, s, txt, 120)
	}
	_, ty := find(t, s, branding.Subtitle)
	_, cy := find(t, s, branding.Contact)
	_, ey := find(t, s, branding.Email)
	if ey != cy+1 {
		t.Errorf("e-mail not on the line after the contact: %d vs %d", ey, cy)
	}
	if _, wy := find(t, s, branding.Web); wy != ey+1 {
		t.Errorf("web address not on the line after the e-mail: %d vs %d", wy, ey)
	}
	lx, ly := find(t, s, firstLogoLine(branding.LogoWide))
	if ly >= ty {
		t.Error("logo not above the title")
	}
	// Logo block centred.
	lw := 0
	for _, l := range strings.Split(strings.TrimRight(branding.LogoWide, "\n"), "\n") {
		lw = max(lw, uniseg.StringWidth(l))
	}
	if d := lx - (120-lw)/2; d < -1 || d > 1 {
		t.Errorf("logo at x=%d, want ~%d", lx, (120-lw)/2)
	}
	// Vertically centred: as many blank rows above as below (±1).
	all := lines(s)
	topBlank, bottomBlank := 0, 0
	for _, l := range all {
		if strings.TrimSpace(l) != "" {
			break
		}
		topBlank++
	}
	for i := len(all) - 1; i >= 0 && strings.TrimSpace(all[i]) == ""; i-- {
		bottomBlank++
	}
	if d := topBlank - bottomBlank; d < -1 || d > 1 {
		t.Errorf("not vertically centred: %d above, %d below", topBlank, bottomBlank)
	}
	if v, _, _ := Layout(120, 40); v != LogoWide {
		t.Errorf("variant %d", v)
	}
}

func TestCompactAt40x15(t *testing.T) {
	s := newScreen(t, 40, 15, 256)
	render(s, New(ModeAbout, nil))
	find(t, s, firstLogoLine(branding.LogoCompact))
	assertCentred(t, s, branding.Title, 40)
	assertCentred(t, s, branding.Email, 40)
	assertCentred(t, s, branding.Web, 40)
	if v, _, _ := Layout(40, 15); v != LogoCompact {
		t.Errorf("variant %d", v)
	}
	for _, l := range lines(s) {
		if strings.Contains(l, firstLogoLine(branding.LogoWide)) {
			t.Error("wide logo at 40 columns")
		}
	}
}

func TestTextOnlyAt20x10(t *testing.T) {
	s := newScreen(t, 20, 10, 256)
	render(s, New(ModeSplash, nil))
	assertCentred(t, s, TextOnlyWord, 20)
	assertCentred(t, s, branding.Title, 20)
	if v, _, _ := Layout(20, 10); v != LogoText {
		t.Errorf("variant %d", v)
	}
	text := strings.Join(lines(s), "\n")
	if strings.ContainsAny(text, "█▀▄▌▐") {
		t.Errorf("block characters at 20x10:\n%s", text)
	}
}

func TestColourDependsOnScreen(t *testing.T) {
	brand := tcell.GetColor(branding.BrandColor)
	for _, c := range []struct {
		colors int
		want   bool
	}{{8, false}, {16, false}, {256, true}, {1 << 24, true}} {
		s := newScreen(t, 120, 40, c.colors)
		render(s, New(ModeSplash, nil))
		x, y := find(t, s, branding.Title)
		_, st, _ := s.Get(x, y)
		fg, _, _ := st.Decompose()
		if got := fg == brand; got != c.want {
			t.Errorf("colors=%d: title fg %v, brand=%v", c.colors, fg, got)
		}
		x, y = find(t, s, firstLogoLine(branding.LogoWide))
		_, st, _ = s.Get(x, y)
		fg, _, _ = st.Decompose()
		if got := fg == brand; got != c.want {
			t.Errorf("colors=%d: logo fg %v", c.colors, fg)
		}
	}
}

func press(p tview.Primitive, k tcell.Key, r rune) {
	p.InputHandler()(tcell.NewEventKey(k, r, tcell.ModNone), func(tview.Primitive) {})
}

func TestKeyDismissesSplash(t *testing.T) {
	calls := 0
	sp := New(ModeSplash, func() { calls++ })
	sp.Start(nil, time.Hour)
	press(sp, tcell.KeyRune, 'x')
	press(sp, tcell.KeyEnter, 0)
	if calls != 1 {
		t.Fatalf("done called %d times", calls)
	}
}

func TestAboutClosesOnEscOnly(t *testing.T) {
	calls := 0
	sp := New(ModeAbout, func() { calls++ })
	sp.Start(nil, time.Millisecond) // no timer in About mode
	time.Sleep(20 * time.Millisecond)
	press(sp, tcell.KeyRune, 'x')
	press(sp, tcell.KeyEnter, 0)
	if calls != 0 {
		t.Fatalf("About closed without Esc")
	}
	press(sp, tcell.KeyEscape, 0)
	if calls != 1 {
		t.Fatalf("Esc: done called %d times", calls)
	}
}

func TestTimerFires(t *testing.T) {
	done := make(chan struct{})
	queued := make(chan func(), 1)
	sp := New(ModeSplash, func() { close(done) })
	sp.Start(func(f func()) { queued <- f }, 10*time.Millisecond)
	select {
	case f := <-queued: // the "UI goroutine" runs the queued close
		f()
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not fire")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("done not called")
	}
	sp.Close() // idempotent
}

func TestDefaultDuration(t *testing.T) {
	if Duration != 3*time.Second {
		t.Fatal(Duration)
	}
	sp := New(ModeSplash, func() {})
	sp.Start(nil, 0)
	sp.mu.Lock()
	ok := sp.timer != nil
	sp.mu.Unlock()
	if !ok {
		t.Fatal("no timer")
	}
	sp.Close()
}

func TestVersionWithBuildDate(t *testing.T) {
	old := branding.BuildDate
	branding.BuildDate = "2026-11-21"
	defer func() { branding.BuildDate = old }()
	s := newScreen(t, 120, 40, 256)
	render(s, New(ModeAbout, nil))
	assertCentred(t, s, "Version "+branding.Version+" (2026-11-21)", 120)
}

// TestLayoutThresholds pins where the variants switch: the wide logo needs
// 60 columns and logo + spacer + text rows, the compact one 30 columns
// (and the widest text line), otherwise the plain word.
func TestLayoutThresholds(t *testing.T) {
	text := textLines()
	textW := blockWidth(text)
	wide := logoLines(branding.LogoWide)
	compact := logoLines(branding.LogoCompact)
	wideH := len(wide) + 1 + len(text)
	compactH := len(compact) + 1 + len(text)
	for _, c := range []struct {
		w, h, want int
	}{
		{120, 40, LogoWide},
		{60, wideH, LogoWide},
		{60, wideH - 1, LogoCompact},
		{59, wideH, LogoCompact},
		{40, 15, LogoCompact},
		{max(30, textW), compactH, LogoCompact},
		{max(30, textW), compactH - 1, LogoText},
		{20, 10, LogoText},
	} {
		if v, _, _ := Layout(c.w, c.h); v != c.want {
			t.Errorf("Layout(%d, %d) = %d, want %d", c.w, c.h, v, c.want)
		}
	}
	if last := text[len(text)-1]; last != branding.Web {
		t.Errorf("last text line %q, want the web address", last)
	}
}

func TestAboutShowsLicenceHint(t *testing.T) {
	s := newScreen(t, 120, 40, 256)
	render(s, New(ModeAbout, nil))
	assertCentred(t, s, licenceHints[0], 120)
	_, wy := find(t, s, branding.Web)
	if _, hy := find(t, s, licenceHints[0]); hy != wy+2 {
		t.Errorf("hint at row %d, web at %d", hy, wy)
	}
	// Not on the splash screen.
	render(s, New(ModeSplash, nil))
	for _, l := range lines(s) {
		if strings.Contains(l, "Licences: L") || strings.Contains(l, licenceHints[0]) {
			t.Fatal("licence hint on the splash screen")
		}
	}
}

// TestAboutHintDroppedWhenTight: at the exact layout thresholds the hint is
// left out, and the logo variant does not change because of it.
func TestAboutHintDroppedWhenTight(t *testing.T) {
	text := textLines()
	wideH := len(logoLines(branding.LogoWide)) + 1 + len(text)
	for _, c := range []struct {
		w, h  int
		hints int
	}{{60, wideH, 0}, {60, wideH + 1, 1}, {60, wideH + 2, 2}, {20, 10, 0}} {
		s := newScreen(t, c.w, c.h, 256)
		render(s, New(ModeAbout, nil))
		v, logo, txt := Layout(c.w, c.h)
		if got := len(aboutHint(c.w, c.h, len(logo)+1+len(txt))); got != c.hints {
			t.Errorf("%dx%d: %d hint lines, want %d", c.w, c.h, got, c.hints)
		}
		if c.w == 60 && v != LogoWide {
			t.Errorf("%dx%d: variant %d", c.w, c.h, v)
		}
		if c.w == 60 {
			find(t, s, branding.Web) // the text still fits
		}
		if c.hints > 0 {
			find(t, s, licenceHints[0])
		}
	}
	if h := aboutHint(15, 40, 10); len(h) != 2 || h[1] != "Licences: L" {
		t.Errorf("narrow hint %q", h)
	}
	if h := aboutHint(5, 40, 10); h != nil {
		t.Errorf("hint at 5 columns: %q", h)
	}
}

func contentRow(s tcell.Screen, y int) string { return strings.TrimSpace(line(s, y)) }

func TestLicenceViewKeys(t *testing.T) {
	calls := 0
	sp := New(ModeAbout, func() { calls++ })
	s := newScreen(t, 100, 30, 256)
	render(s, sp)
	press(sp, tcell.KeyRune, 'l')
	if !sp.ShowingLicences() {
		t.Fatal("l did not open the licence view")
	}
	render(s, sp)
	find(t, s, "Third-party licences")
	find(t, s, "THIRD-PARTY LICENCES")
	find(t, s, "L/Esc: back")
	for _, l := range branding.Licenses[:10] { // the summary is at the top
		find(t, s, l.Module)
	}
	for _, l := range lines(s) {
		if strings.Contains(l, branding.Web) {
			t.Fatal("About content still drawn")
		}
	}

	// Down scrolls one line: row 2 moves to row 1.
	second := contentRow(s, 2)
	press(sp, tcell.KeyDown, 0)
	render(s, sp)
	if got := contentRow(s, 1); got != second {
		t.Errorf("Down: row 1 %q, want %q", got, second)
	}
	press(sp, tcell.KeyUp, 0)
	press(sp, tcell.KeyUp, 0) // clamped at the top
	render(s, sp)
	if sp.scroll != 0 || contentRow(s, 1) != "THIRD-PARTY LICENCES" {
		t.Errorf("Up: scroll %d, row 1 %q", sp.scroll, contentRow(s, 1))
	}

	// PgDn moves by a page minus one line.
	rows := 30 - 2
	last := contentRow(s, rows) // last content row
	press(sp, tcell.KeyPgDn, 0)
	render(s, sp)
	if sp.scroll != rows-1 || contentRow(s, 1) != last {
		t.Errorf("PgDn: scroll %d, row 1 %q, want %q", sp.scroll, contentRow(s, 1), last)
	}
	press(sp, tcell.KeyPgUp, 0)
	render(s, sp)
	if sp.scroll != 0 {
		t.Errorf("PgUp: scroll %d", sp.scroll)
	}

	// End shows the end of the last licence text; Home returns.
	press(sp, tcell.KeyEnd, 0)
	render(s, sp)
	wrapped := wrapText(branding.ThirdPartyLicenses, licenceWidth)
	if sp.scroll != len(wrapped)-rows || contentRow(s, rows) != strings.TrimSpace(wrapped[len(wrapped)-1]) {
		t.Errorf("End: scroll %d of %d, last row %q", sp.scroll, len(wrapped), contentRow(s, rows))
	}
	press(sp, tcell.KeyDown, 0) // clamped at the bottom
	render(s, sp)
	if sp.scroll != len(wrapped)-rows {
		t.Errorf("Down at the end: scroll %d", sp.scroll)
	}
	press(sp, tcell.KeyHome, 0)
	if sp.scroll != 0 {
		t.Errorf("Home: scroll %d", sp.scroll)
	}

	// Esc returns to the About content, a second Esc closes About.
	press(sp, tcell.KeyEscape, 0)
	if sp.ShowingLicences() || calls != 0 {
		t.Fatalf("Esc in licence view: showing %v, done %d", sp.ShowingLicences(), calls)
	}
	render(s, sp)
	find(t, s, branding.Web)
	press(sp, tcell.KeyRune, 'L')
	if !sp.ShowingLicences() {
		t.Fatal("L did not open the licence view")
	}
	press(sp, tcell.KeyRune, 'L')
	if sp.ShowingLicences() || calls != 0 {
		t.Fatal("L did not return to About")
	}
	press(sp, tcell.KeyEscape, 0)
	if calls != 1 {
		t.Fatalf("second Esc: done called %d times", calls)
	}
}

func TestSplashIgnoresLicenceKey(t *testing.T) {
	calls := 0
	sp := New(ModeSplash, func() { calls++ })
	sp.Start(nil, time.Hour)
	press(sp, tcell.KeyRune, 'l')
	if calls != 1 || sp.ShowingLicences() {
		t.Fatalf("l on the splash: done %d, licences %v", calls, sp.ShowingLicences())
	}
}

func TestLicenceViewSmallSizes(t *testing.T) {
	for _, sz := range [][2]int{{20, 10}, {12, 3}, {1, 1}, {200, 60}} {
		sp := New(ModeAbout, nil)
		press(sp, tcell.KeyRune, 'l')
		s := newScreen(t, sz[0], sz[1], 256)
		render(s, sp)
		press(sp, tcell.KeyEnd, 0)
		render(s, sp)
		press(sp, tcell.KeyPgUp, 0)
		render(s, sp)
		if sz[1] >= 3 {
			find(t, s, "Third")
		}
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("short\nthe quick brown fox jumps\nabcdefghijkl\n\nü ü", 10)
	want := []string{"short", "the quick", "brown fox", "jumps", "abcdefghij", "kl", "", "ü ü"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%q", got)
	}
	for _, l := range wrapText(branding.ThirdPartyLicenses, licenceWidth) {
		if uniseg.StringWidth(l) > licenceWidth {
			t.Fatalf("line too wide: %q", l)
		}
	}
}
