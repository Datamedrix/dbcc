-- wc: count the rows of a table or view (spec 8).
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "wc",
  usage = "wc <table|view>",
  help  = "Count the rows: SELECT COUNT(*)",
  args  = { "object" },
  run = function(ctx, args)
    local obj = ctx:resolve(args[1])
    if not data_kinds[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. ", not a table or view", 0)
    end
    local res = ctx:on(obj):query("SELECT COUNT(*) AS n FROM " .. obj.sql)
    ctx:rows({ "rows", "object" }, { { tonumber(res[1].n), obj.name } }, { title = "wc " .. obj.name })
  end,
}
