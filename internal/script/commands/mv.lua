-- mv: rename a table or view. Renaming is dialect-specific SQL without a
-- primitive, so the command branches on ctx.dialect.
local movable = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "mv",
  usage = "mv <table|view> <new-name>",
  help  = "Rename a table or view (within its database or schema)",
  args  = { "object", "string" },
  write = true,
  run = function(ctx, args)
    local obj = ctx:resolve(args[1])
    if not movable[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. "; mv renames tables and views", 0)
    end
    local new = args[2]
    if new == "" or new:find("[./]") then
      error("the new name must be a plain name (mv renames within " ..
        (obj.parts.schema or obj.parts.database) .. ")", 0)
    end
    local on = ctx:on(obj)
    local d = on.dialect
    local sql
    if d == "mysql" or d == "mariadb" then
      sql = "RENAME TABLE " .. obj.sql .. " TO " .. on:quote(obj.parts.database) .. "." .. on:quote(new)
    elseif d == "postgres" then
      sql = "ALTER " .. obj.kind:upper() .. " " .. obj.sql .. " RENAME TO " .. on:quote(new)
    elseif d == "mssql" then
      -- sp_rename works in its own database: [db].sys.sp_rename with the
      -- two-part name.
      local two = on:quote(obj.parts.schema) .. "." .. on:quote(obj.name)
      sql = "EXEC " .. on:quote(obj.parts.database) .. ".sys.sp_rename N'" .. (two:gsub("'", "''")) ..
        "', N'" .. (new:gsub("'", "''")) .. "'"
    else
      error("mv is not supported for " .. tostring(d) .. " yet", 0)
    end
    on:exec(sql)
    ctx:message("mv: " .. obj.sql .. " -> " .. new)
  end,
}
