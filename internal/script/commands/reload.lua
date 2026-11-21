-- reload: reload all commands (embedded defaults and user scripts) without
-- restarting dbcc. Install a command at run time: put a .lua file into the
-- user command directory, then run `reload`. A broken script is reported
-- in the message log and skipped; a broken override keeps the default.

dbcc.command{
  name  = "reload",
  usage = "reload",
  help  = "Reload all commands: the built-in ones and the user scripts in ~/.config/dbcc/commands (install new scripts without restarting)",
  args  = {},
  run = function(ctx, args)
    local dir = ctx:reload_commands()
    ctx:message("reloading commands; user scripts are read from " .. dir)
  end,
}
