-- pwd: print the current path (spec 8).
dbcc.command{
  name  = "pwd",
  usage = "pwd",
  help  = "Print the current path",
  args  = {},
  run = function(ctx, args)
    ctx:text(tostring(ctx.path), nil, { title = "pwd" })
  end,
}
