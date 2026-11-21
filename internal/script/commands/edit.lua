-- edit: open the source of a view, procedure, function, trigger or event
-- as a runnable replace script in an editor tab (spec 9.1).
local aliases = { proc = "procedure", func = "function", fn = "function", mview = "materialized view" }
local editable = { view = true, procedure = true, ["function"] = true, trigger = true, event = true,
  ["materialized view"] = true }

dbcc.command{
  name  = "edit",
  usage = "edit [-t kind] <object>",
  help  = "Open a view, procedure, function, trigger or event in an editor tab as a script that replaces it " ..
          "(Run all saves it). -t view|procedure|function|trigger|event. Without UI the script is printed",
  args  = { "object" },
  flags = { t = "string" },
  run = function(ctx, args, flags)
    local kind = flags.t and (aliases[flags.t] or flags.t)
    local obj
    if kind then
      obj = ctx:resolve(args[1], kind)
    else
      obj = ctx:resolve(args[1], { "view", "procedure", "function", "trigger", "event", "materialized view" })
    end
    if not editable[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. "; edit opens views, procedures, functions, triggers and events", 0)
    end
    local script = ctx:replace_script(obj, obj.kind)
    ctx:editor_open(obj.name, script, { object = obj })
  end,
}
