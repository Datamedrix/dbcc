package app

import (
	"context"

	"dbcc/internal/ui"
)

// RunTUI starts the full-screen UI.
func RunTUI(ctx context.Context, env *Env) error {
	return ui.Run(ctx, ui.Options{
		Config:         env.Config,
		Profile:        env.Opts.Profile,
		NoSplash:       env.Opts.NoSplash,
		NoUserCommands: env.Opts.NoUserCommands,
		ConfigPath:     env.Opts.ConfigPath, // F2 saves connections to the file given with --config
	})
}
