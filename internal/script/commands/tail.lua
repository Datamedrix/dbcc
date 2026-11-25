-- tail: the last n rows of a table or view by its key, in ascending order;
-- with -f new rows are appended as they arrive (spec 8, A26).
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "tail",
  usage = "tail [-f] [-i seconds] [-k column] <table|view|file:path> [n]",
  help  = "Show the last n rows (default 10) by primary key or -k column, ascending. "
       .. "-f follows: rows with a key above the last one are appended as they are inserted "
       .. "(polled every -i seconds, default 2, on the auxiliary connection; closing the tab or F8 stops it). "
       .. "file:path: the last n records of the file (no -f, no -k)",
  args  = { "object", "int?" },
  flags = { f = "bool", i = "string", k = "string" },
  run = function(ctx, args, flags)
    local n = tonumber(args[2]) or 10
    if n < 0 then error("n must not be negative", 0) end
    if flags.f and n < 1 then error("tail -f needs n of at least 1", 0) end
    local interval
    if flags.i then
      if not flags.f then error("-i needs -f", 0) end
      interval = tonumber(flags.i)
      if not interval or interval < 0.5 then
        error("-i: the interval is a number of seconds, at least 0.5", 0)
      end
    end
    local obj = ctx:resolve(args[1])
    if obj.kind == "file" then
      if flags.f or flags.k then error("-f and -k are not available for files", 0) end
      return ctx:file_grid(obj, { tail = n })
    end
    if not data_kinds[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. ", not a table or view", 0)
    end
    -- The object may live on another connection (object tree, absolute path).
    local on = ctx:on(obj)
    local keys
    if flags.k then
      keys = { flags.k }
    else
      local pk = obj.kind == "table" and on:meta(obj).primary_key
      if not pk or #pk.columns == 0 then
        error("tail needs a primary key or -k <column>: " .. obj.name .. " has no primary key", 0)
      end
      keys = pk.columns
    end
    if flags.f and #keys > 1 then
      error("tail -f needs a single-column key, but the primary key of " .. obj.name .. " is ("
        .. table.concat(keys, ", ") .. "): use -k <column>", 0)
    end
    local desc, asc = {}, {}
    for i, k in ipairs(keys) do
      desc[i] = on:quote(k) .. " DESC"
      asc[i] = on:quote(k) .. " ASC"
    end
    -- The last n rows by key (the limit applies to the descending order),
    -- shown ascending like head.
    local last = on:limit("SELECT * FROM " .. obj.sql .. " ORDER BY " .. table.concat(desc, ", "), n)
    local q = "SELECT * FROM (" .. last .. ") AS dbcc_tail ORDER BY " .. table.concat(asc, ", ")
    local opts = { origin = obj }
    if flags.f then
      local k = on:quote(keys[1])
      opts.follow = {
        sql = "SELECT * FROM " .. obj.sql .. " WHERE " .. k .. " > ? ORDER BY " .. k .. " ASC",
        key = keys[1],
        interval = interval,
      }
    end
    on:grid(q, opts)
  end,
}
