package ui

import (
	"github.com/gdamore/tcell/v2"
)

// Theme holds the colours of the UI. Defaults use the terminal's own
// background and foreground and only the 16 basic colours, so the UI
// degrades gracefully to 256 and 16 colours (spec 5.2); the brand colour
// is used where the terminal supports 256 colours or more.
type Theme struct {
	Text, Dim, Accent, Brand, Error, Warn, OK tcell.Color
	Border, BorderFocus, Title                tcell.Color
	FooterKey, FooterLabel                    tcell.Color
	StatusBg, StatusFg                        tcell.Color
}

// NewTheme picks colours for a terminal with the given number of colours.
func NewTheme(colors int) *Theme {
	t := &Theme{
		Text:        tcell.ColorDefault,
		Dim:         tcell.ColorGray,
		Accent:      tcell.ColorTeal,
		Brand:       tcell.ColorBlue,
		Error:       tcell.ColorRed,
		Warn:        tcell.ColorYellow,
		OK:          tcell.ColorGreen,
		Border:      tcell.ColorGray,
		BorderFocus: tcell.ColorTeal,
		Title:       tcell.ColorDefault,
		FooterKey:   tcell.ColorDefault,
		FooterLabel: tcell.ColorBlack,
		StatusBg:    tcell.ColorNavy,
		StatusFg:    tcell.ColorWhite,
	}
	if colors >= 256 {
		brand := tcell.NewRGBColor(0x00, 0x69, 0xB4) // Datamedrix blue
		t.Brand = brand
		t.BorderFocus = brand
		t.StatusBg = brand
		t.Accent = tcell.NewRGBColor(0x3d, 0x9a, 0xe0)
	}
	if colors < 16 {
		t.Dim, t.Accent, t.Border, t.BorderFocus = tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault
	}
	return t
}
