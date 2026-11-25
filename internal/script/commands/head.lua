-- head: the first n rows of a table or view (spec 8, 13).
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "head",
  usage = "head <table|view|file:path> [n]",
  help  = "Show the first n rows (default 10) of a table, view or file:path",
  args  = { "object", "int?" },
  run = function(ctx, args)
    local n = tonumber(args[2]) or 10
    if n < 0 then error("n must not be negative", 0) end
    local obj = ctx:resolve(args[1])
    if obj.kind == "file" then return ctx:file_grid(obj, { limit = n }) end
    if not data_kinds[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. ", not a table or view", 0)
    end
    -- The object may live on another connection (object tree, absolute path).
    local on = ctx:on(obj)
    on:grid(on:limit("SELECT * FROM " .. obj.sql, n), { origin = obj })
  end,
}
