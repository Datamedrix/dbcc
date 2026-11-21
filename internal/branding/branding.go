// Package branding holds the product title, contact and version shown on
// the splash screen and About page (spec 3.7, 7.1).
package branding

// Version and BuildDate are set at link time:
//
//	-ldflags "-X dbcc/internal/branding.Version=v1.0.0 -X dbcc/internal/branding.BuildDate=2026-11-21"
var (
	Version   = "dev"
	BuildDate = ""
)

const (
	Title    = "dbcc"
	Subtitle = "Database Command & Control"
	Company  = "A product by Datamedrix GmbH"
	Contact  = "Contact: Anton Grünberg"
	Email    = "a.gruenberg@datamedrix.com"
	Web      = "https://www.datamedrix.com"
)
