-- export: write the active result to a file (spec 10.7, A28). The formats
-- are export plugins registered with dbcc.exporter (exporter_csv.lua,
-- exporter_json.lua, user scripts); this script holds the whole flow:
--
--   with a file:    type from -t or the file extension, options from -o;
--                   no dialogs, so it works headless as well;
--   without a file: choose the type, its settings and the file in dialogs
--                   (this is what the grid key e runs), ask before
--                   overwriting, then write.

local USAGE = "export [-f] [-t type] [-o name=value]... [file]"

local function find(name)
  for _, e in ipairs(dbcc.exporters()) do
    if e.name == name then return e end
  end
end

local function type_names()
  local names = {}
  for _, e in ipairs(dbcc.exporters()) do names[#names + 1] = e.name end
  return table.concat(names, ", ")
end

local function by_type(name)
  local e = find(name)
  if not e then
    error("unknown export type " .. string.format("%q", name) .. " (types: " .. type_names() .. ")", 0)
  end
  return e
end

-- by_extension finds the type whose extensions contain the one of file.
local function by_extension(file)
  local ext = file:match("%.([^./\\]+)$")
  if ext then
    ext = ext:lower()
    for _, e in ipairs(dbcc.exporters()) do
      for _, x in ipairs(e.extensions) do
        if x:lower() == ext then return e end
      end
    end
  end
  local exts = {}
  for _, e in ipairs(dbcc.exporters()) do
    for _, x in ipairs(e.extensions) do exts[#exts + 1] = "." .. x end
  end
  error("unknown export type for " .. string.format("%q", file) .. ": use -t <type> or one of the extensions " ..
    table.concat(exts, ", ") .. " (types: " .. type_names() .. ")", 0)
end

-- options turns the -o name=value words into a table name -> text.
local function options(list)
  local out = {}
  for _, kv in ipairs(list or {}) do
    local k, v = kv:match("^([^=]+)=(.*)$")
    if not k then
      error("-o needs name=value, got " .. string.format("%q", kv) .. "; usage: " .. USAGE, 0)
    end
    out[k] = v
  end
  return out
end

-- file_safe turns a result title into a file name.
local function file_safe(title)
  local s = (title or ""):gsub("[^%w%-_.]+", "_"):gsub("^[_.]+", ""):gsub("_+$", "")
  if #s > 60 then s = s:sub(1, 60) end
  if s == "" then s = "export" end
  return s
end

local function written(ctx, n, file, e)
  ctx:message("export: " .. dbcc.fmt.count(n) .. " rows written to " .. file .. " (" .. e.name .. ")")
end

dbcc.command{
  name  = "export",
  usage = USAGE,
  help  = "Write the active result to a file; continues loading to the end, applies a local filter and sort. " ..
          "Without a file: choose type, settings and file in dialogs (grid key e). " ..
          "-t type (default: from the file extension), -o name=value sets an option of the type (repeatable), " ..
          "-f overwrites an existing file; help export lists the types and their options",
  args  = { "file?" },
  flags = { f = "bool", t = "string", o = "string..." },
  flag_values = { t = "exporters" },
  run = function(ctx, args, flags)
    local given = options(flags.o)
    -- file:<path> is accepted like everywhere else (A29)
    local file = args[1] and (args[1]:gsub("^file:", ""))
    if file then
      local e = flags.t and by_type(flags.t) or by_extension(file)
      local n = ctx:export_with(e.name, file, { options = given, force = flags.f == true })
      written(ctx, n, file, e)
      return
    end

    if not ctx.ui then error("a file is required without UI; usage: " .. USAGE, 0) end
    local info = ctx:result_info()
    if not info then error("no active result to export", 0) end
    local function cancelled() ctx:message("export cancelled") end

    -- 1. the type
    local e
    if flags.t then
      e = by_type(flags.t)
    else
      local list = dbcc.exporters()
      if #list == 0 then error("no export types are registered", 0) end
      if #list == 1 then
        e = list[1]
      else
        local items = {}
        for i, x in ipairs(list) do items[i] = { label = x.title, exporter = x } end
        local item = ctx:pick(items, { title = "Export as" })
        if not item then return cancelled() end
        e = item.exporter
      end
    end

    -- 2. its settings
    local values = given
    if #e.options > 0 then
      values = ctx:form(e.options, { title = "Export as " .. e.title, values = given })
      if not values then return cancelled() end
    end

    -- 3. the file
    local ext = e.extensions[1]
    local path, exists = ctx:save_file{
      title = "Export as " .. e.title .. " — save to",
      name = file_safe(info.title) .. (ext and ("." .. ext) or ""),
      extensions = e.extensions,
    }
    if not path then return cancelled() end

    -- 4. overwrite?
    local force = flags.f == true
    if exists and not force then
      if not ctx:confirm(path .. " exists. Overwrite?") then return cancelled() end
      force = true
    end

    -- 5. write
    local n = ctx:export_with(e.name, path, { options = values, force = force })
    written(ctx, n, path, e)
  end,
}
