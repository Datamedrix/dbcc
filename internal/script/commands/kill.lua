-- kill: terminate a session, or with -q only its running statement. The
-- statements differ per dialect, so the command branches on ctx.dialect.
dbcc.command{
  name  = "kill",
  usage = "kill [-q] <id>",
  help  = "Terminate the session with the given id (see ps); -q cancels only its running statement " ..
          "(not on MSSQL)",
  args  = { "int" },
  flags = { q = "bool" },
  write = true,
  run = function(ctx, args, flags)
    local d = ctx.dialect
    if d == nil then error("not connected: cd /<profile> first", 0) end
    if not tostring(args[1]):match("^%d+$") then error("the session id must be a positive integer", 0) end
    local id = tonumber(args[1])
    local lit = string.format("%d", id)
    local function not_me(sql)
      if tonumber(ctx:query(sql)[1].id) == id then error("session " .. lit .. " is this session", 0) end
    end
    if d == "mysql" or d == "mariadb" then
      not_me("SELECT CONNECTION_ID() AS id")
      ctx:exec((flags.q and "KILL QUERY " or "KILL ") .. lit)
    elseif d == "postgres" then
      not_me("SELECT pg_backend_pid() AS id")
      if #ctx:query("SELECT 1 AS x FROM pg_stat_activity WHERE pid = ?", id) == 0 then
        error("no session " .. lit, 0)
      end
      ctx:exec("SELECT " .. (flags.q and "pg_cancel_backend" or "pg_terminate_backend") .. "(" .. lit .. ")")
    elseif d == "mssql" then
      if flags.q then
        error("kill -q: SQL Server cannot cancel the statement of another session without ending it; use kill " .. lit, 0)
      end
      not_me("SELECT @@SPID AS id")
      -- KILL takes no parameter: the id is a validated integer literal.
      ctx:exec("KILL " .. lit)
    else
      error("kill is not supported for " .. d, 0)
    end
    ctx:message("kill: " .. (flags.q and "statement of session " or "session ") .. lit .. " terminated")
  end,
}
