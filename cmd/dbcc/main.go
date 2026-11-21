// Command dbcc — Database Command & Control, a terminal database client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"dbcc/internal/app"
	"dbcc/internal/branding"
	"dbcc/internal/config"

	_ "dbcc/internal/dialect/mssql"
	_ "dbcc/internal/dialect/mysql"
	_ "dbcc/internal/dialect/postgres"
)

func main() { os.Exit(run()) }

func run() int {
	opts, err := app.ParseArgs(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return app.ExitOK
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbcc:", err)
		return app.ExitUsage
	}
	if opts.Version {
		fmt.Println(branding.Title, branding.Version)
		return app.ExitOK
	}
	if opts.Licenses {
		return app.PrintLicenses(os.Stdout)
	}
	path := opts.ConfigPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbcc:", err)
		return app.ExitUsage
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, "dbcc: warning:", w)
	}
	env := &app.Env{Opts: opts, Config: cfg, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Prompt: app.TTYPrompter}
	ctx := context.Background()
	if opts.ExportCommands != "" {
		return app.ExitCode(app.ExportCommands(opts.ExportCommands, os.Stderr))
	}
	if opts.Headless(os.Stdin) {
		err := app.RunHeadless(ctx, env)
		if err != nil && !app.Reported(err) {
			fmt.Fprintln(os.Stderr, "dbcc:", err) // e.g. missing or unknown profile, connection failure
		}
		return app.ExitCode(err)
	}
	return app.ExitCode(app.RunTUI(ctx, env))
}
