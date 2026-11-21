// Package deps pins dependencies that later milestones use, so that
// `go mod tidy` keeps the versions verified in November 2026 (spec 0).
package deps

import (
	_ "github.com/alecthomas/chroma/v2"
	_ "github.com/gdamore/tcell/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/rivo/tview"
	_ "github.com/yuin/gopher-lua"
)
