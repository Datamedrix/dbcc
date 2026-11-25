-- open: show a file (CSV …) in the result grid (A29). The formats are
-- import plugins registered with dbcc.importer (importer_csv.lua, user
-- scripts); this script holds the flow:
--
--   with a file:    type from -t or the file extension, options from -o;
--                   no dialogs, so it works headless as well (the rows are
--                   printed like those of ll);
--   without a file: choose the file in the file dialog, then the type and
--                   its settings, then open it.

local USAGE = "open [-t type] [-o name=value]... [file]"

local function type_names()
  local names = {}
  for _, e in ipairs(dbcc.importers()) do names[#names + 1] = e.name end
  return table.concat(names, ", ")
end

local function by_type(name)
  for _, e in ipairs(dbcc.importers()) do
    if e.name == name then return e end
  end
  error("unknown import type " .. string.format("%q", name) .. " (types: " .. type_names() .. ")", 0)
end

-- by_extension lists the types whose extensions match the file.
local function by_extension(file)
  local ext = (file:match("%.([^./\\]+)$") or ""):lower()
  local out = {}
  for _, e in ipairs(dbcc.importers()) do
    for _, x in ipairs(e.extensions) do
      if x:lower() == ext then out[#out + 1] = e break end
    end
  end
  return out
end

-- options turns the -o name=value words into a table name -> text.
local function options(list)
  local out, any = {}, false
  for _, kv in ipairs(list or {}) do
    local k, v = kv:match("^([^=]+)=(.*)$")
    if not k then
      error("-o needs name=value, got " .. string.format("%q", kv) .. "; usage: " .. USAGE, 0)
    end
    out[k], any = v, true
  end
  return out, any
end

dbcc.command{
  name  = "open",
  usage = USAGE,
  help  = "Open a file (CSV …) in the result grid, read by an import format; the commands ll, head, tail, wc and cat " ..
          "take files as file:path. Without a file: choose file, type and settings in dialogs. " ..
          "-t type (default: from the file extension), -o name=value sets an option (repeatable); help open lists the types",
  args  = { "file?" },
  flags = { t = "string", o = "string..." },
  flag_values = { t = "importers" },
  run = function(ctx, args, flags)
    local given, any = options(flags.o)
    local file = args[1]
    if flags.t then by_type(flags.t) end
    if file then
      local opts = {}
      if flags.t then opts.importer = flags.t end
      if any then opts.options = given end
      ctx:file_grid(file, opts)
      return
    end

    if not ctx.ui then error("a file is required without UI; usage: " .. USAGE, 0) end
    local function cancelled() ctx:message("open cancelled") end
    local exts = {}
    for _, e in ipairs(dbcc.importers()) do
      for _, x in ipairs(e.extensions) do exts[#exts + 1] = x end
    end
    if #dbcc.importers() == 0 then error("no import types are registered", 0) end

    -- 1. the file
    local path = ctx:open_file{ title = "Open file", extensions = exts }
    if not path then return cancelled() end

    -- 2. the type: -t, the only one matching the extension, else a list
    local imp
    if flags.t then
      imp = by_type(flags.t)
    else
      local list = by_extension(path)
      if #list == 1 then
        imp = list[1]
      else
        if #list == 0 then list = dbcc.importers() end
        local items = {}
        for i, x in ipairs(list) do items[i] = { label = x.title, importer = x } end
        local item = ctx:pick(items, { title = "Open as" })
        if not item then return cancelled() end
        imp = item.importer
      end
    end

    -- 3. its settings
    local values = given
    if #imp.options > 0 then
      values = ctx:form(imp.options, { title = "Open " .. path:match("[^/]*$") .. " as " .. imp.title, values = given })
      if not values then return cancelled() end
    end

    -- 4. open
    ctx:file_grid(path, { importer = imp.name, options = values })
  end,
}
