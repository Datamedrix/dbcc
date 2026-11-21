-- du: data and index size and row estimate of tables (spec 8). The
-- catalog queries differ per dialect, so the command branches on
-- ctx.dialect.
local function num(v) return tonumber(v) or 0 end

local function collect(res, qualify)
  local out = {}
  for _, r in ipairs(res) do
    local name = r.name
    if qualify and r.sch then name = r.sch .. "." .. name end
    out[#out + 1] = { name = name, engine = r.engine or "", rows = math.max(num(r.row_estimate), 0),
      data = num(r.data), index = num(r.idx), free = num(r.free) }
  end
  return out
end

local function mysql_rows(on, db, schema, name)
  if on.dialect == "mysql" then
    -- MySQL 8 caches information_schema.TABLES statistics for a day.
    pcall(on.query, on, "SET SESSION information_schema_stats_expiry = 0")
  end
  local q = [[SELECT TABLE_NAME AS name, ENGINE AS engine, TABLE_ROWS AS row_estimate,
      DATA_LENGTH AS data, INDEX_LENGTH AS idx, DATA_FREE AS free
    FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = ? AND TABLE_TYPE <> 'VIEW' AND TABLE_TYPE <> 'SYSTEM VIEW']]
  if name then
    return collect(on:query(q .. " AND TABLE_NAME = ?", db, name))
  end
  return collect(on:query(q .. " ORDER BY DATA_LENGTH + INDEX_LENGTH DESC, TABLE_NAME", db))
end

-- PostgreSQL: the catalog of the session's database only.
local function postgres_rows(on, db, schema, name)
  local q = [[SELECT n.nspname AS sch, c.relname AS name, COALESCE(am.amname, '') AS engine,
      c.reltuples::bigint AS row_estimate, pg_table_size(c.oid) AS data,
      pg_indexes_size(c.oid) AS idx, 0 AS free
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_am am ON am.oid = c.relam
    WHERE c.relkind IN ('r', 'p', 'm') AND current_database() = ?]]
  local args = { db }
  if schema then
    q = q .. " AND n.nspname = ?"
    args[#args + 1] = schema
  else
    q = q .. " AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\\_%'"
  end
  if name then
    q = q .. " AND c.relname = ?"
    args[#args + 1] = name
  end
  q = q .. " ORDER BY pg_total_relation_size(c.oid) DESC, n.nspname, c.relname"
  return collect(on:query(q, unpack(args)), schema == nil)
end

-- MSSQL: sys.partitions and sys.allocation_units (8 KiB pages), through
-- three-part names, so any database works.
local function mssql_rows(on, db, schema, name)
  local x = on:quote(db) .. ".sys."
  local q = [[SELECT s.name AS sch, t.name AS name,
      MAX(CASE WHEN p.index_id = 1 THEN 'clustered' WHEN p.index_id = 0 THEN 'heap' END) AS engine,
      (SELECT SUM(pp.rows) FROM ]] .. x .. [[partitions pp WHERE pp.object_id = t.object_id AND pp.index_id IN (0, 1)) AS row_estimate,
      SUM(CASE WHEN p.index_id IN (0, 1) THEN a.used_pages ELSE 0 END) * 8192 AS data,
      SUM(CASE WHEN p.index_id > 1 THEN a.used_pages ELSE 0 END) * 8192 AS idx,
      SUM(a.total_pages - a.used_pages) * 8192 AS free
    FROM ]] .. x .. [[tables t
    JOIN ]] .. x .. [[schemas s ON s.schema_id = t.schema_id
    JOIN ]] .. x .. [[partitions p ON p.object_id = t.object_id
    JOIN ]] .. x .. [[allocation_units a
      ON a.container_id = CASE WHEN a.type IN (1, 3) THEN p.hobt_id ELSE p.partition_id END
    WHERE t.is_ms_shipped = 0]]
  local args = {}
  if schema then
    q = q .. " AND s.name = ?"
    args[#args + 1] = schema
  end
  if name then
    q = q .. " AND t.name = ?"
    args[#args + 1] = name
  end
  q = q .. " GROUP BY t.object_id, s.name, t.name ORDER BY SUM(a.used_pages) DESC, s.name, t.name"
  return collect(on:query(q, unpack(args)), schema == nil)
end

local by_dialect = { mysql = mysql_rows, mariadb = mysql_rows, postgres = postgres_rows, mssql = mssql_rows }
local storage_kinds = { table = true, ["materialized view"] = true }

dbcc.command{
  name  = "du",
  usage = "du [table]",
  help  = "Data and index size and row estimate of a table, or of all tables of the current database (or schema)",
  args  = { "object?" },
  run = function(ctx, args)
    local on, db, schema, name, where
    if args[1] then
      local obj = ctx:resolve(args[1])
      if not storage_kinds[obj.kind] then
        error(obj.name .. " is a " .. obj.kind .. "; du reports the storage of tables", 0)
      end
      on, db, schema, name, where = ctx:on(obj), obj.parts.database, obj.parts.schema, obj.name, obj.sql
    else
      db = ctx.path.database
      if not db then error("du without a table needs a current database: cd into one", 0) end
      on, schema, where = ctx, ctx.path.schema, ctx.path.str
    end
    local d = on.dialect
    local rows_of = by_dialect[d]
    if not rows_of then error("du is not supported for " .. tostring(d), 0) end
    if d == "postgres" and on.conn == nil and ctx.path.database ~= db then
      error("du on PostgreSQL reads the catalog of the current database; cd /" ..
        ctx.path.conn .. "/" .. db .. " first", 0)
    end
    local list = rows_of(on, db, schema, name)
    local rows = {}
    local tot = { rows = 0, data = 0, index = 0, free = 0 }
    for _, t in ipairs(list) do
      rows[#rows + 1] = { t.name, t.engine, t.rows, dbcc.fmt.size(t.data), dbcc.fmt.size(t.index),
        dbcc.fmt.size(t.data + t.index), dbcc.fmt.size(t.free), t.data + t.index }
      for k in pairs(tot) do tot[k] = tot[k] + t[k] end
    end
    if not name and #list > 1 then
      rows[#rows + 1] = { "(total " .. #list .. " tables)", "", tot.rows, dbcc.fmt.size(tot.data),
        dbcc.fmt.size(tot.index), dbcc.fmt.size(tot.data + tot.index), dbcc.fmt.size(tot.free), tot.data + tot.index }
    end
    ctx:rows({ "table", "engine", "rows (estimate)", "data", "index", "total", "free", "total bytes" }, rows,
      { title = "du " .. where })
  end,
}
