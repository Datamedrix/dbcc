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
        -- The formats are plugins (dbcc.exporter, dbcc.importer): list them with their options.
        local formats = (c.name == "export" and dbcc.exporters()) or (c.name == "open" and dbcc.importers()) or {}
        for _, e in ipairs(formats) do
          local exts = {}
          for _, x in ipairs(e.extensions) do exts[#exts + 1] = "." .. x end
          out[#out + 1] = ""
          out[#out + 1] = "type " .. e.name .. " — " .. e.title ..
            (#exts > 0 and (" (" .. table.concat(exts, ", ") .. ")") or "")
          for _, o in ipairs(e.options) do
            local def = o.default
            if o.type == "choice" then
              for _, ch in ipairs(o.choices) do
                if ch.value == o.default then def = ch.label end
              end
            end
            local line = "  -o " .. o.name .. "=<" .. o.type .. ">  " .. o.label .. " (default: " .. tostring(def) .. ")"
            if o.type == "choice" then
              local vals = {}
              for _, ch in ipairs(o.choices) do vals[#vals + 1] = (ch.label:match("^%S+") or ch.value) end
              line = line .. "; " .. table.concat(vals, ", ")
            end
            out[#out + 1] = line
          end
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
