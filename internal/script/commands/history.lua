-- history: the statement history (spec 12). In the UI the chosen entry is
-- loaded into the editor; without UI the list is printed.
dbcc.command{
  name  = "history",
  usage = "history [n]",
  help  = "Show the last n statements (default 100); in the UI Enter loads one into the editor",
  args  = { "int?" },
  run = function(ctx, args)
    local n = tonumber(args[1]) or 100
    if n <= 0 then error("n must be positive", 0) end
    local entries = ctx:history(n)
    if #entries == 0 then
      ctx:message("history: no statements recorded yet")
      return
    end
    -- Newest first in the list.
    local items = {}
    for i = #entries, 1, -1 do
      local e = entries[i]
      local stmt = e.stmt:gsub("%s+", " ")
      items[#items + 1] = { label = e.time .. "  " .. e.path .. "  " .. (e.ok and "" or "[error] ") .. stmt, entry = e }
    end
    local ok, item = pcall(ctx.pick, ctx, items, { title = "history" })
    if ok then
      if item then ctx:editor_set(item.entry.stmt) end
      return
    end
    -- No UI: print the list, oldest first, like a shell history.
    local rows = {}
    for i, e in ipairs(entries) do
      rows[#rows + 1] = { i, e.time, e.profile, e.path, string.format("%.1f", e.duration_ms), e.outcome, e.stmt }
    end
    ctx:rows({ "#", "time", "profile", "path", "ms", "outcome", "statement" }, rows, { title = "history" })
  end,
}
