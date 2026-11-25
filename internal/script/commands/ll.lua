-- ll: all rows of a table or view in the result grid (spec 3.2, 8).
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "ll",
  usage = "ll <table|view|file:path>",
  help  = "SELECT * FROM the object, opened in the result grid (table-origin result); file:path opens a file (CSV …) through its import format",
  args  = { "object" },
  run = function(ctx, args)
    local obj = ctx:resolve(args[1])
    if obj.kind == "file" then return ctx:file_grid(obj) end
    if not data_kinds[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. ", not a table or view", 0)
    end
    ctx:grid("SELECT * FROM " .. obj.sql, { origin = obj })
  end,
}
