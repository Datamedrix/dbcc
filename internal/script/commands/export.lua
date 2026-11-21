-- export: write the active result to a file (spec 10.7).
dbcc.command{
  name  = "export",
  usage = "export [-f] <file.csv|file.json>",
  help  = "Write the active result to a .csv or .json file; continues loading to the end, applies a local " ..
          "filter and sort; -f overwrites an existing file",
  args  = { "file" },
  flags = { f = "bool" },
  run = function(ctx, args, flags)
    local n = ctx:export(args[1], { force = flags.f == true })
    ctx:message("export: " .. dbcc.fmt.count(n) .. " rows written to " .. args[1])
  end,
}
