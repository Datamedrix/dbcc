// Package commands holds the default command scripts, one Lua file per
// command, embedded in the binary (spec 8, 13). They use only the public
// Lua API, so they double as reference examples; a user script of the same
// name in ~/.config/dbcc/commands replaces any of them.
package commands

import "embed"

// FS contains the default command scripts (*.lua).
//
//go:embed *.lua
var FS embed.FS
