-- cat: structure of a table (spec 8.1); source of a view, procedure,
-- function, trigger or event.
local aliases = { proc = "procedure", func = "function", fn = "function", mview = "materialized view" }

-- Types whose literal defaults are shown bare (everything else is quoted).
local bare_types = {
  tinyint = true, smallint = true, mediumint = true, int = true, integer = true, bigint = true,
  decimal = true, numeric = true, dec = true, fixed = true, float = true, double = true, real = true,
  bit = true, year = true, bool = true, boolean = true, serial = true, money = true,
}

local function yesno(b) if b then return "YES" else return "NO" end end

local function quote_literal(s) return "'" .. (s:gsub("'", "''")) .. "'" end

local function show_default(c)
  if c.generated then return "" end
  if c.default == nil then
    if c.nullable then return "NULL" end
    return ""
  end
  if c.default_expr then return c.default end
  local base = (c.data_type:match("^%a+") or ""):lower()
  if bare_types[base] then return c.default end
  return quote_literal(c.default)
end

local function list(xs) return table.concat(xs, ", ") end

-- wrapped reports whether s is one parenthesised expression: "(a + b)",
-- not "(a) + (b)".
local function wrapped(s)
  if s:sub(1, 1) ~= "(" or s:sub(-1) ~= ")" then return false end
  local depth = 0
  for i = 1, #s do
    local ch = s:sub(i, i)
    if ch == "(" then depth = depth + 1 elseif ch == ")" then depth = depth - 1 end
    if depth == 0 and i < #s then return false end
  end
  return true
end

local function paren(s)
  if wrapped(s) then return s end
  return "(" .. s .. ")"
end

local function section(out, title, headers, rows)
  out[#out + 1] = title
  if #rows == 0 then
    out[#out + 1] = "(none)\n"
  else
    -- no trailing blanks when the last column is empty
    out[#out + 1] = (dbcc.fmt.table(headers, rows):gsub(" +\n", "\n"))
  end
end

local option_order = { "engine", "charset", "collation", "rows", "auto_increment", "row_format",
  "create_options", "partitioning" }
local option_label = { rows = "rows (estimate)" }

-- identity_clause returns the identity definition of column c from the
-- options (PostgreSQL identity.<column>, MSSQL identity) and the option
-- key it consumed, or nil.
local function identity_clause(m, c)
  local k = "identity." .. c.name
  local v = m.options[k]
  if v then return "GENERATED " .. v .. " AS IDENTITY", k end
  v = m.options.identity
  if v and c.auto_increment then return v, "identity" end
  return nil
end

local function describe(ctx, obj)
  local m = ctx:meta(obj)
  local table_coll = m.options.collation
  local matview = m.kind == "materialized view"
  local consumed = {} -- option keys shown elsewhere

  -- DESCRIBE-style Key: PRI, UNI (first column of a one-column unique
  -- index), MUL (first column of any other index).
  local pk = {}
  if m.primary_key then
    for _, c in ipairs(m.primary_key.columns) do pk[c] = true end
  end
  local key = {}
  for _, ix in ipairs(m.indexes) do
    local first = ix.columns[1]
    if first and not first.expression and not pk[first.name] then
      if ix.unique and #ix.columns == 1 then
        key[first.name] = "UNI"
      elseif key[first.name] == nil then
        key[first.name] = "MUL"
      end
    end
  end

  local out = {}
  local title = m.kind .. " " .. m.sql
  if m.comment ~= "" then title = title .. " -- " .. m.comment end
  out[#out + 1] = title .. "\n"

  local rows = {}
  for _, c in ipairs(m.columns) do
    local extra = {}
    local ident, key_used = identity_clause(m, c)
    if ident then
      extra[#extra + 1] = ident
      consumed[key_used] = true
    elseif c.auto_increment then
      extra[#extra + 1] = "auto_increment"
    end
    if c.generated then
      extra[#extra + 1] = "GENERATED ALWAYS AS " .. paren(c.generated.expression) .. " " ..
        (c.generated.stored and "STORED" or "VIRTUAL")
    end
    if c.on_update ~= "" then extra[#extra + 1] = "on update " .. c.on_update end
    if c.collation ~= "" and table_coll and c.collation ~= table_coll then
      extra[#extra + 1] = "COLLATE " .. c.collation
    end
    local k = pk[c.name] and "PRI" or key[c.name] or ""
    rows[#rows + 1] = { c.position, c.name, c.data_type, yesno(c.nullable), k, show_default(c), list(extra), c.comment }
  end
  section(out, "Columns", { "#", "Field", "Type", "Null", "Key", "Default", "Extra", "Comment" }, rows)

  if not matview then
    rows = {}
    if m.primary_key then
      rows[#rows + 1] = { "PRIMARY KEY", m.primary_key.name, list(m.primary_key.columns) }
    end
    for _, k in ipairs(m.unique_keys) do
      rows[#rows + 1] = { "UNIQUE", k.name, list(k.columns) }
    end
    section(out, "Primary and unique keys", { "Key", "Name", "Columns" }, rows)

    rows = {}
    for _, fk in ipairs(m.foreign_keys) do
      rows[#rows + 1] = { fk.name, list(fk.columns), fk.ref_table.sql .. " (" .. list(fk.ref_columns) .. ")",
        fk.on_update, fk.on_delete }
    end
    section(out, "Foreign keys", { "Name", "Columns", "References", "On update", "On delete" }, rows)

    rows = {}
    for _, ck in ipairs(m.checks) do
      rows[#rows + 1] = { ck.name, ck.expression, yesno(not ck.not_enforced) }
    end
    section(out, "Check constraints", { "Name", "Expression", "Enforced" }, rows)
  end

  -- Include and Filter columns only where some index has them
  -- (PostgreSQL, MSSQL).
  local has_include, has_filter = false, false
  for _, ix in ipairs(m.indexes) do
    if #ix.include > 0 then has_include = true end
    if ix.filter ~= "" then has_filter = true end
  end
  rows = {}
  for _, ix in ipairs(m.indexes) do
    local cols = {}
    for _, c in ipairs(ix.columns) do
      local s = c.name
      if c.expression then s = paren(s) end
      if c.sub_part > 0 then s = s .. "(" .. c.sub_part .. ")" end
      if c.descending then s = s .. " DESC" end
      cols[#cols + 1] = s
    end
    local row = { ix.name, list(cols) }
    if has_include then row[#row + 1] = list(ix.include) end
    if has_filter then row[#row + 1] = ix.filter end
    for _, v in ipairs({ yesno(ix.unique), ix.type, yesno(not ix.invisible), ix.comment }) do row[#row + 1] = v end
    rows[#rows + 1] = row
  end
  local headers = { "Name", "Columns" }
  if has_include then headers[#headers + 1] = "Include" end
  if has_filter then headers[#headers + 1] = "Filter" end
  for _, h in ipairs({ "Unique", "Type", "Visible", "Comment" }) do headers[#headers + 1] = h end
  section(out, "Indexes", headers, rows)

  if not matview then
    rows = {}
    for _, tr in ipairs(m.triggers) do
      rows[#rows + 1] = { tr.name, tr.timing, tr.event }
    end
    section(out, "Triggers", { "Name", "Timing", "Event" }, rows)
  end

  rows = {}
  local seen = {}
  for _, k in ipairs(option_order) do
    if m.options[k] then
      rows[#rows + 1] = { option_label[k] or k, m.options[k] }
      seen[k] = true
    end
  end
  local rest = {}
  for k in pairs(m.options) do
    if not seen[k] and not consumed[k] then rest[#rest + 1] = k end
  end
  table.sort(rest)
  for _, k in ipairs(rest) do rows[#rows + 1] = { option_label[k] or k, m.options[k] } end
  rows[#rows + 1] = { "comment", m.comment }
  section(out, matview and "Options" or "Table options", { "Option", "Value" }, rows)
  if matview then
    out[#out + 1] = "Definition"
    out[#out + 1] = ctx:source(obj, obj.kind)
  end

  ctx:text(table.concat(out, "\n"), "sql", { title = "cat " .. obj.name })
end

-- note_ambiguous tells the owner when other objects share the name.
local function note_ambiguous(ctx, obj)
  local p = obj.parts
  local at = "/" .. p.conn .. "/" .. p.database .. (p.schema and ("/" .. p.schema) or "")
  local ok, objs = pcall(ctx.objects, ctx, at)
  if not ok then return end
  for _, o in ipairs(objs) do
    if o.name == obj.name and o.kind ~= obj.kind then
      ctx:message(obj.name .. " is also a " .. o.kind .. "; use: cat -t " .. o.kind .. " " .. obj.name)
    end
  end
end

dbcc.command{
  name  = "cat",
  usage = "cat [-t kind] <object>",
  help  = "Structure of a table (columns, keys, foreign keys, checks, indexes, triggers, options); " ..
          "source of a view, procedure, function, trigger or event. -t table|view|procedure|function|trigger|event",
  args  = { "object" },
  flags = { t = "string" },
  run = function(ctx, args, flags)
    local kind = flags.t and (aliases[flags.t] or flags.t)
    local obj = ctx:resolve(args[1], kind)
    if not kind then note_ambiguous(ctx, obj) end
    if obj.kind == "table" or obj.kind == "materialized view" then
      describe(ctx, obj)
      return
    end
    local src = ctx:source(obj, obj.kind)
    ctx:text(src, "sql", { title = "cat " .. obj.name })
  end,
}
