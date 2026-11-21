package branding

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

func checkLogo(t *testing.T, name, logo string, maxW, maxH int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(logo, "\n"), "\n")
	if len(lines) == 0 || len(lines) > maxH {
		t.Fatalf("%s: %d rows, want 1..%d", name, len(lines), maxH)
	}
	for i, l := range lines {
		if w := uniseg.StringWidth(l); w > maxW {
			t.Errorf("%s line %d: width %d > %d", name, i, w, maxW)
		}
		for _, r := range l {
			if r != ' ' && (r < 0x2580 || r > 0x259F) {
				t.Errorf("%s line %d: unexpected rune %q", name, i, r)
			}
		}
	}
}

func TestLogoSizes(t *testing.T) {
	checkLogo(t, "wide", LogoWide, 60, 12)
	checkLogo(t, "compact", LogoCompact, 30, 6)
	// The installed variants are 60x6 and 30x3; the splash layout relies on
	// those heights fitting 120x40 and 40x15 with the text.
	if n := strings.Count(LogoWide, "\n"); n != 6 {
		t.Errorf("wide: %d rows, want 6", n)
	}
	if n := strings.Count(LogoCompact, "\n"); n != 3 {
		t.Errorf("compact: %d rows, want 3", n)
	}
	if BrandColor != "#0069B4" {
		t.Fatal(BrandColor)
	}
}

func TestWeb(t *testing.T) {
	if Web != "https://www.datamedrix.com" {
		t.Fatal(Web)
	}
}
