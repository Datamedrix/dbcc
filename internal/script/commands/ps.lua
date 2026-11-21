-- ps: running sessions and statements. The catalog differs per dialect,
-- so the command branches on ctx.dialect.
local queries = {
  -- PostgreSQL: client backends of pg_stat_activity.
  postgres = {
    list = [[SELECT pid AS id, usename AS "user",
        COALESCE(host(client_addr) || ':' || client_port, '') AS host, datname AS db,
        backend_type AS command,
        EXTRACT(EPOCH FROM now() - COALESCE(query_start, backend_start))::bigint AS time,
        COALESCE(state, '') || COALESCE(' (' || wait_event_type || ': ' || wait_event || ')', '') AS state,
        query AS info
      FROM pg_stat_activity WHERE backend_type = 'client backend' ORDER BY pid]],
    me = "SELECT pg_backend_pid() AS id",
  },
  -- MSSQL: user sessions with their running request, if any.
  mssql = {
    list = [[SELECT s.session_id AS id, s.login_name AS [user], COALESCE(s.host_name, '') AS host,
        DB_NAME(s.database_id) AS db, COALESCE(r.command, '') AS command,
        DATEDIFF(SECOND, COALESCE(r.start_time, s.last_request_start_time), GETDATE()) AS time,
        s.status + COALESCE(' (' + r.wait_type + ')', '') AS state, t.text AS info
      FROM sys.dm_exec_sessions s
      LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
      OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
      WHERE s.is_user_process = 1 ORDER BY s.session_id]],
    me = "SELECT @@SPID AS id",
  },
}

local function mysql_list(ctx, d)
  local cols = "ID AS id, USER AS user, HOST AS host, DB AS db, COMMAND AS command, TIME AS time, " ..
               "STATE AS state, INFO AS info"
  if d == "mysql" then
    -- information_schema.PROCESSLIST is deprecated in MySQL 8.
    local ok, r = pcall(ctx.query, ctx, "SELECT " .. cols .. " FROM performance_schema.processlist ORDER BY ID")
    if ok then return r end
  end
  return ctx:query("SELECT " .. cols .. " FROM information_schema.PROCESSLIST ORDER BY ID")
end

dbcc.command{
  name  = "ps",
  usage = "ps",
  help  = "Running sessions and their statements; * marks this session",
  args  = {},
  run = function(ctx)
    local d = ctx.dialect
    if d == nil then error("not connected: cd /<profile> first", 0) end
    local res, me
    if d == "mysql" or d == "mariadb" then
      res = mysql_list(ctx, d)
      me = ctx:query("SELECT CONNECTION_ID() AS id")[1].id
    elseif queries[d] then
      res = ctx:query(queries[d].list)
      me = ctx:query(queries[d].me)[1].id
    else
      error("ps is not supported for " .. d, 0)
    end
    local rows = {}
    for _, r in ipairs(res) do
      rows[#rows + 1] = { tostring(r.id) == tostring(me) and "*" or "", tonumber(r.id), r.user, r.host, r.db,
        r.command, tonumber(r.time), r.state, r.info }
    end
    ctx:rows({ "me", "id", "user", "host", "db", "command", "time", "state", "info" }, rows, { title = "ps" })
  end,
}
