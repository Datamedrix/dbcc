-- help: list the commands, or show the usage of one; "help about" shows
-- the About page.
dbcc.command{
  name  = "help",
  usage = "help [command|about]",
  help  = "List the commands, or show usage and description of one; help about shows the About page",
  args  = { "string?" },
  run = function(ctx, args)
    local name = args[1]
    local cmds = dbcc.commands()
    if name == nil then
      local rows = {}
      for _, c in ipairs(cmds) do
        rows[#rows + 1] = { c.name, c.usage, c.help }
      end
      ctx:rows({ "command", "usage", "description" }, rows, { title = "help" })
      return
    end
    if name == "about" then
      ctx:about()
      return
    end
    for _, c in ipairs(cmds) do
      if c.name == name then
        local out = { "usage: " .. c.usage, "", c.help ~= "" and c.help or "(no description)" }
        local flags = {}
        for f, typ in pairs(c.flags) do
          flags[#flags + 1] = "  -" .. f .. (typ ~= "bool" and (" <" .. typ .. ">") or "")
        end
        table.sort(flags)
        if #flags > 0 then
          out[#out + 1] = ""
          out[#out + 1] = "flags:"
          for _, f in ipairs(flags) do out[#out + 1] = f end
        end
        if #c.args > 0 then
          out[#out + 1] = ""
          out[#out + 1] = "arguments: " .. table.concat(c.args, " ")
        end
        out[#out + 1] = ""
        out[#out + 1] = (c.write and "writes (asks for confirmation, blocked in read-only profiles); " or "") ..
          "source: " .. c.source
        ctx:text(table.concat(out, "\n"), nil, { title = "help " .. c.name })
        return
      end
    end
    error("no command " .. name .. "; help lists all commands", 0)
  end,
}
