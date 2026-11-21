-- cd: change the current path (spec 8).
dbcc.command{
  name  = "cd",
  usage = "cd [path]",
  help  = "Change the current path: cd db, cd .., cd /conn/db[/schema], cd - (previous path), cd alone (the profile's default)",
  args  = { "path?" },
  run = function(ctx, args)
    local target = args[1]
    if target == nil then
      if ctx.profile == nil then return end -- at "/": nothing to go back to
      target = "/" .. ctx.profile.name
      if ctx.profile.database then target = target .. "/" .. ctx.profile.database end
    end
    local ok, err = pcall(ctx.chdir, ctx, target)
    if ok then return end
    local plain = not target:find("/", 1, true) and target ~= "-" and target ~= "." and target ~= ".."
    -- Leniency: on a database level of a dialect without schema level
    -- (MySQL, MariaDB), "cd other" means the sibling database.
    if plain and not ctx.has_schemas and ctx.path.level == "database" then
      ctx:chdir("../" .. target)
      return
    end
    -- On a schema level (PostgreSQL, MSSQL), "cd other" means a sibling
    -- schema, else a sibling database.
    if plain and ctx.has_schemas and ctx.path.level == "schema" then
      if pcall(ctx.chdir, ctx, "../" .. target) then return end
      if pcall(ctx.chdir, ctx, "../../" .. target) then return end
    end
    error(err, 0)
  end,
}
