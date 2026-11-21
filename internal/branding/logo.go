package branding

import _ "embed"

// BrandColor is the Datamedrix blue, RGB(0,105,180), taken from the official
// logo (docs/dmx_logo.png).
const BrandColor = "#0069B4"

// LogoWide and LogoCompact are text-art variants of the official Datamedrix
// word mark, made of Unicode quadrant block characters. They are generated
// from docs/dmx_logo.png by gen/logo2text.py (do not edit by hand; rerun the
// script). The wide variant is 60x6 cells, the compact one 30x3; the spec
// limits are 60x12 and 30x6 (spec 7.1).
//
// Approved by the owner on 2026-11-21 (spec 7.1, A14).
// Until approved, they must be treated as provisional.
var (
	//go:embed logo_wide.txt
	LogoWide string

	//go:embed logo_compact.txt
	LogoCompact string
)
