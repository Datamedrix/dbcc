-- cp: copy a table (spec 8.2). Data only, or with -a structure and data.
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "cp",
  usage = "cp [-a] <src> <new>",
  help  = "Copy a table: CREATE TABLE new AS SELECT * FROM src. -a copies the structure too " ..
          "(columns, defaults, keys, indexes, checks; no foreign keys or triggers), then the data",
  args  = { "object", "string" },
  flags = { a = "bool" },
  write = true,
  run = function(ctx, args, flags)
    local src = ctx:resolve(args[1])
    if not data_kinds[src.kind] then
      error(src.name .. " is a " .. src.kind .. ", not a table or view", 0)
    end
    local on = ctx:on(src)
    local stmts, dst = on:copy_sql(src, args[2], { structure = flags.a == true })
    local what = flags.a and "copy with structure" or "copy (data only)"
    if not ctx:confirm("cp: " .. what .. " of " .. src.sql .. " to " .. dst.sql .. " runs on " .. src.conn ..
        ":\n\n" .. table.concat(stmts, ";\n") .. ";") then
      error("cancelled", 0)
    end
    -- The row count is that of the statement copying the data (the
    -- first one, or the INSERT of cp -a; index and sequence statements
    -- may follow).
    local rows
    for i, st in ipairs(stmts) do
      local n = on:exec(st)
      if rows == nil and (not flags.a or st:find("INSERT INTO", 1, true)) then rows = n end
    end
    rows = rows or 0
    ctx:message("cp: " .. src.sql .. " -> " .. dst.sql .. " (" .. dbcc.fmt.count(rows) .. " rows)")
  end,
}
