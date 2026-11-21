-- ll: all rows of a table or view in the result grid (spec 3.2, 8).
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "ll",
  usage = "ll <table|view>",
  help  = "SELECT * FROM the object, opened in the result grid (table-origin result)",
  args  = { "object" },
  run = function(ctx, args)
    local obj = ctx:resolve(args[1])
    if not data_kinds[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. ", not a table or view", 0)
    end
    ctx:grid("SELECT * FROM " .. obj.sql, { origin = obj })
  end,
}
