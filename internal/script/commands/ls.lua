-- ls: list what is at a path level (spec 8).
dbcc.command{
  name  = "ls",
  usage = "ls [path]",
  help  = "List objects at the path level with their kind; at / the connection profiles, at /conn the databases",
  args  = { "path?" },
  run = function(ctx, args)
    local p = ctx:abspath(args[1] or ".")
    local rows = {}
    if p.level == "root" then
      for _, pr in ipairs(ctx:profiles()) do
        local where = pr.socket
        if where == "" then
          where = (pr.host ~= "" and pr.host or "localhost") .. ":" .. pr.port
        end
        local note = pr.driver .. " " .. where
        if pr.database ~= "" then note = note .. "/" .. pr.database end
        if pr.current then
          note = note .. " (current)"
        elseif pr.open then
          note = note .. " (open)"
        end
        rows[#rows + 1] = { pr.name, "connection", note }
      end
    else
      for _, o in ipairs(ctx:objects(p)) do
        local note = o.comment
        if o.system then note = (note ~= "" and note .. " " or "") .. "(system)" end
        rows[#rows + 1] = { o.name, o.kind, note }
      end
    end
    ctx:rows({ "name", "kind", "comment" }, rows, { title = "ls " .. p.str })
  end,
}
