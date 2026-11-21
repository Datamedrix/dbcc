-- grep: search object names, column names and the sources of views,
-- routines, triggers and events in a database. Case-insensitive, literal.
local function lines_matching(src, pat)
  local out = {}
  local low = pat:lower()
  local n = 0
  for line in (src .. "\n"):gmatch("(.-)\r?\n") do
    n = n + 1
    if line:lower():find(low, 1, true) then
      out[#out + 1] = "line " .. n .. ": " .. line:gsub("^%s+", "")
    end
  end
  if #out == 0 then out[1] = "(matches)" end
  return out
end

local function mysql_search(on, db, pat)
  local found = {}
  local function add(object, kind, match, detail)
    found[#found + 1] = { object, kind, match, detail }
  end
  local like = "LOCATE(LOWER(?), LOWER(%s)) > 0"
  local function q(sql, col)
    return on:query(sql .. " AND " .. like:format(col) .. " ORDER BY 1", db, pat)
  end
  for _, r in ipairs(q([[SELECT TABLE_NAME AS name, TABLE_TYPE AS type FROM information_schema.TABLES
      WHERE TABLE_SCHEMA = ?]], "TABLE_NAME")) do
    add(r.name, (r.type == "VIEW" or r.type == "SYSTEM VIEW") and "view" or "table", "name", "")
  end
  for _, r in ipairs(q([[SELECT ROUTINE_NAME AS name, ROUTINE_TYPE AS type FROM information_schema.ROUTINES
      WHERE ROUTINE_SCHEMA = ?]], "ROUTINE_NAME")) do
    add(r.name, r.type:lower(), "name", "")
  end
  for _, r in ipairs(q([[SELECT TRIGGER_NAME AS name FROM information_schema.TRIGGERS
      WHERE TRIGGER_SCHEMA = ?]], "TRIGGER_NAME")) do
    add(r.name, "trigger", "name", "")
  end
  for _, r in ipairs(q([[SELECT EVENT_NAME AS name FROM information_schema.EVENTS
      WHERE EVENT_SCHEMA = ?]], "EVENT_NAME")) do
    add(r.name, "event", "name", "")
  end
  for _, r in ipairs(q([[SELECT TABLE_NAME AS name, COLUMN_NAME AS col, COLUMN_TYPE AS type
      FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?]], "COLUMN_NAME")) do
    add(r.name, "column", "column", r.col .. " " .. r.type)
  end
  local sources = {
    { [[SELECT TABLE_NAME AS name, 'view' AS kind, VIEW_DEFINITION AS src FROM information_schema.VIEWS
        WHERE TABLE_SCHEMA = ?]], "VIEW_DEFINITION" },
    { [[SELECT ROUTINE_NAME AS name, LOWER(ROUTINE_TYPE) AS kind, ROUTINE_DEFINITION AS src
        FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?]], "ROUTINE_DEFINITION" },
    { [[SELECT TRIGGER_NAME AS name, 'trigger' AS kind, ACTION_STATEMENT AS src FROM information_schema.TRIGGERS
        WHERE TRIGGER_SCHEMA = ?]], "ACTION_STATEMENT" },
    { [[SELECT EVENT_NAME AS name, 'event' AS kind, EVENT_DEFINITION AS src FROM information_schema.EVENTS
        WHERE EVENT_SCHEMA = ?]], "EVENT_DEFINITION" },
  }
  for _, s in ipairs(sources) do
    for _, r in ipairs(q(s[1], s[2])) do
      for _, l in ipairs(lines_matching(r.src or "", pat)) do
        add(r.name, r.kind, "source", l)
      end
    end
  end
  return found
end

-- PostgreSQL: the catalog of the session's database; schema nil searches
-- all user schemas and qualifies the names.
local function postgres_search(on, schema, pat)
  local found = {}
  local where = "n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\\_%'"
  local args = {}
  if schema then
    where = "n.nspname = ?"
    args[1] = schema
  end
  local function q(sql, col)
    local a = { unpack(args) }
    a[#a + 1] = pat
    return on:query(sql .. " WHERE " .. where .. " AND strpos(lower(" .. col .. "), lower(?)) > 0 ORDER BY 1, 2", unpack(a))
  end
  local function name(r)
    if schema then return r.name end
    return r.sch .. "." .. r.name
  end
  local function add(r, kind, match, detail) found[#found + 1] = { name(r), kind, match, detail } end
  local relkind = [[CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
      WHEN 'S' THEN 'sequence' WHEN 'i' THEN 'index' WHEN 'I' THEN 'index' ELSE 'table' END]]
  local prokind = "CASE p.prokind WHEN 'p' THEN 'procedure' WHEN 'a' THEN 'aggregate' ELSE 'function' END"
  local rel = " FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace"
  -- Extra conditions extend the ON clause of the last (inner) join.
  local proc = " FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace"
  local trg = " FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace"
  for _, r in ipairs(q("SELECT n.nspname AS sch, c.relname AS name, " .. relkind .. " AS kind" .. rel ..
      " AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')", "c.relname")) do
    add(r, r.kind, "name", "")
  end
  for _, r in ipairs(q("SELECT DISTINCT n.nspname AS sch, p.proname AS name, " .. prokind .. " AS kind" .. proc, "p.proname")) do
    add(r, r.kind, "name", "")
  end
  for _, r in ipairs(q("SELECT n.nspname AS sch, t.tgname AS name" .. trg .. " AND NOT t.tgisinternal", "t.tgname")) do
    add(r, "trigger", "name", "")
  end
  for _, r in ipairs(q([[SELECT n.nspname AS sch, c.relname AS name, a.attname AS col,
      format_type(a.atttypid, a.atttypmod) AS type FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
      JOIN pg_namespace n ON n.oid = c.relnamespace AND a.attnum > 0 AND NOT a.attisdropped]], "a.attname")) do
    add(r, "column", "column", r.col .. " " .. r.type)
  end
  local sources = {
    { "SELECT n.nspname AS sch, c.relname AS name, " .. relkind .. " AS kind, pg_get_viewdef(c.oid) AS src" .. rel ..
      " AND c.relkind IN ('v', 'm')", "pg_get_viewdef(c.oid)" },
    { "SELECT n.nspname AS sch, p.proname AS name, " .. prokind .. " AS kind, p.prosrc AS src" .. proc ..
      " AND p.prokind IN ('f', 'p')", "p.prosrc" },
    { "SELECT n.nspname AS sch, t.tgname AS name, 'trigger' AS kind, pg_get_triggerdef(t.oid) AS src" .. trg ..
      " AND NOT t.tgisinternal", "pg_get_triggerdef(t.oid)" },
  }
  for _, s in ipairs(sources) do
    for _, r in ipairs(q(s[1], s[2])) do
      for _, l in ipairs(lines_matching(r.src or "", pat)) do add(r, r.kind, "source", l) end
    end
  end
  return found
end

-- MSSQL: [db].sys catalog views (three-part names: any database).
local mssql_kind = [[CASE o.type WHEN 'U' THEN 'table' WHEN 'V' THEN 'view' WHEN 'P' THEN 'procedure'
    WHEN 'TR' THEN 'trigger' WHEN 'SO' THEN 'sequence' WHEN 'SN' THEN 'synonym' ELSE 'function' END]]

local function mssql_search(on, db, schema, pat)
  local found = {}
  local x = on:quote(db) .. ".sys."
  local where = "o.is_ms_shipped = 0"
  local args = {}
  if schema then
    where = where .. " AND s.name = ?"
    args[1] = schema
  end
  local from = " FROM " .. x .. "objects o JOIN " .. x .. "schemas s ON s.schema_id = o.schema_id"
  local function q(sql, extra, col)
    local a = { unpack(args) }
    a[#a + 1] = pat
    return on:query(sql .. " WHERE " .. where .. extra .. " AND CHARINDEX(LOWER(?), LOWER(" .. col .. ")) > 0 ORDER BY 1, 2",
      unpack(a))
  end
  local function add(r, kind, match, detail)
    found[#found + 1] = { schema and r.name or (r.sch .. "." .. r.name), kind, match, detail }
  end
  local types = " AND o.type IN ('U', 'V', 'P', 'FN', 'IF', 'TF', 'FS', 'FT', 'TR', 'SO', 'SN')"
  for _, r in ipairs(q("SELECT s.name AS sch, o.name AS name, " .. mssql_kind .. " AS kind" .. from, types, "o.name")) do
    add(r, r.kind, "name", "")
  end
  for _, r in ipairs(q("SELECT s.name AS sch, o.name AS name, c.name AS col, ty.name AS type" .. from ..
      " JOIN " .. x .. "columns c ON c.object_id = o.object_id JOIN " .. x ..
      "types ty ON ty.user_type_id = c.user_type_id", " AND o.type IN ('U', 'V')", "c.name")) do
    add(r, "column", "column", r.col .. " " .. r.type)
  end
  for _, r in ipairs(q("SELECT s.name AS sch, o.name AS name, " .. mssql_kind .. " AS kind, m.definition AS src" .. from ..
      " JOIN " .. x .. "sql_modules m ON m.object_id = o.object_id", "", "m.definition")) do
    for _, l in ipairs(lines_matching(r.src or "", pat)) do add(r, r.kind, "source", l) end
  end
  return found
end

dbcc.command{
  name  = "grep",
  usage = "grep <pattern> [path]",
  help  = "Search object names, column names and the sources of views, routines, triggers and events " ..
          "in the current database or schema (or those of path); case-insensitive, no wildcards",
  args  = { "string", "path?" },
  run = function(ctx, args)
    local pat = args[1]
    if pat == "" then error("empty pattern", 0) end
    local p = ctx:abspath(args[2] or ".")
    if not p.database then error("grep needs a database: cd into one or give its path", 0) end
    local on = ctx:on(p)
    local d = on.dialect
    local found
    if d == "mysql" or d == "mariadb" then
      found = mysql_search(on, p.database, pat)
    elseif d == "postgres" then
      if on.conn == nil and ctx.path.database ~= p.database then
        error("grep on PostgreSQL reads the catalog of the current database; cd " .. p.str .. " first", 0)
      end
      found = postgres_search(on, p.schema, pat)
    elseif d == "mssql" then
      found = mssql_search(on, p.database, p.schema, pat)
    else
      error("grep is not supported for " .. tostring(d) .. " yet", 0)
    end
    ctx:rows({ "object", "kind", "match", "detail" }, found, { title = "grep " .. pat })
  end,
}
