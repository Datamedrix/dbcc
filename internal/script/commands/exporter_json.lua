-- exporter_json: the JSON export format (A28): an array of objects, one per
-- row, keys are the column names, values strings, NULL as null. With
-- pretty = true (the default) the layout is the one `export file.json` has
-- always written: one object per line.
dbcc.exporter{
  name       = "json",
  title      = "JSON (array of objects)",
  help       = "One object per row; values as strings, NULL as null",
  extensions = { "json" },
  options = {
    { name = "pretty", label = "One object per line", type = "bool", default = true,
      help = "off: the whole array on one line" },
  },
  write = function(ctx, src, out, opts)
    local keys = {}
    for i, c in ipairs(src.columns) do keys[i] = dbcc.fmt.json(c.name) end
    local n = #keys
    local open, sep, comma, close = "\n  {", ": ", ", ", "\n]\n"
    if not opts.pretty then
      open, sep, comma, close = "{", ":", ",", "]\n"
    end
    local count = 0
    out:write("[")
    src:each(function(row)
      local parts = {}
      for i = 1, n do
        parts[i] = keys[i] .. sep .. dbcc.fmt.json(row[i])
      end
      if count > 0 then out:write(",") end
      count = count + 1
      out:write(open, table.concat(parts, comma), "}")
    end)
    if count > 0 then
      out:write(close)
    else
      out:write("]\n")
    end
  end,
}
