package script

import (
	"fmt"
	"sort"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/exec"
	"dbcc/internal/export"
)

// installAPI creates the global dbcc table and the metatables for ctx,
// paths and resolved objects.
func (rt *Runtime) installAPI() {
	L := rt.L
	real := L.NewTable()
	real.RawSetString("command", L.NewFunction(rt.luaCommand))
	real.RawSetString("commands", L.NewFunction(rt.luaCommands))
	real.RawSetString("exporter", L.NewFunction(rt.luaExporter))
	real.RawSetString("exporters", L.NewFunction(rt.luaExporters))
	real.RawSetString("importer", L.NewFunction(rt.luaImporter))
	real.RawSetString("importers", L.NewFunction(rt.luaImporters))
	fmtT := L.NewTable()
	fmtT.RawSetString("table", L.NewFunction(rt.luaFmtTable))
	fmtT.RawSetString("size", L.NewFunction(luaFmtSize))
	fmtT.RawSetString("count", L.NewFunction(luaFmtCount))
	fmtT.RawSetString("csv", L.NewFunction(luaFmtCSV))
	fmtT.RawSetString("json", L.NewFunction(luaFmtJSON))
	fmtT.RawSetString("cell", L.NewFunction(luaFmtCell))
	fmtT.RawSetString("number", L.NewFunction(luaFmtNumber))
	real.RawSetString("fmt", readOnly(L, fmtT, "dbcc.fmt"))
	L.SetGlobal("dbcc", readOnly(L, real, "dbcc"))

	rt.ctxMeta = L.NewTable()
	rt.ctxMeta.RawSetString("__index", rt.ctxMethods())
	rt.ctxMeta.RawSetString("__metatable", lua.LFalse)

	rt.pathMeta = L.NewTable()
	rt.pathMeta.RawSetString("__tostring", L.NewFunction(func(L *lua.LState) int {
		L.Push(L.CheckTable(1).RawGetString("str"))
		return 1
	}))
	rt.fileMeta = L.NewTable()
	rt.fileMeta.RawSetString("__tostring", L.NewFunction(func(L *lua.LState) int {
		L.Push(L.CheckTable(1).RawGetString("path"))
		return 1
	}))
	rt.objMeta = L.NewTable()
	rt.objMeta.RawSetString("__tostring", L.NewFunction(func(L *lua.LState) int {
		L.Push(L.CheckTable(1).RawGetString("sql"))
		return 1
	}))
}

// readOnly returns a proxy table that reads from t and rejects writes, so
// one script cannot break the API for the others.
func readOnly(L *lua.LState, t *lua.LTable, name string) *lua.LTable {
	p := L.NewTable()
	mt := L.NewTable()
	mt.RawSetString("__index", t)
	mt.RawSetString("__newindex", L.NewFunction(func(L *lua.LState) int {
		L.RaiseError("%s is read-only", name)
		return 0
	}))
	mt.RawSetString("__metatable", lua.LFalse)
	L.SetMetatable(p, mt)
	return p
}

var commandFields = map[string]bool{"name": true, "usage": true, "help": true, "args": true, "flags": true, "flag_values": true, "write": true, "run": true}

// dbcc.command{name=, usage=, help=, args=, flags=, write=, run=}
// run is called as run(ctx, args, flags): args holds the positional
// arguments, flags the given flags (bool flags true, value flags strings,
// repeated flags declared as "type..." lists of strings).
func (rt *Runtime) luaCommand(L *lua.LState) int {
	t := L.CheckTable(1)
	if !rt.loading {
		L.RaiseError("dbcc.command may only be called while a command script loads")
	}
	var bad []string
	t.ForEach(func(k, _ lua.LValue) {
		if ks, ok := k.(lua.LString); !ok || !commandFields[string(ks)] {
			bad = append(bad, k.String())
		}
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		L.RaiseError("dbcc.command: unknown field(s) %v (name, usage, help, args, flags, flag_values, write, run)", bad)
	}
	str := func(field string, required bool) string {
		v := t.RawGetString(field)
		switch x := v.(type) {
		case lua.LString:
			return string(x)
		case *lua.LNilType:
			if required {
				L.RaiseError("dbcc.command: field %q is required", field)
			}
			return ""
		}
		L.RaiseError("dbcc.command: field %q must be a string, got %s", field, v.Type())
		return ""
	}
	c := &Command{Name: str("name", true), Usage: str("usage", false), Help: str("help", false),
		Source: rt.loadSrc, Default: rt.loadDef}
	if err := validName(c.Name); err != nil {
		L.RaiseError("dbcc.command: %v", err)
	}
	switch v := t.RawGetString("write").(type) {
	case lua.LBool:
		c.Write = bool(v)
	case *lua.LNilType:
	default:
		L.RaiseError("dbcc.command %s: field \"write\" must be a boolean", c.Name)
	}
	var decl []string
	switch v := t.RawGetString("args").(type) {
	case *lua.LTable:
		for i := 1; i <= v.Len(); i++ {
			s, ok := v.RawGetInt(i).(lua.LString)
			if !ok {
				L.RaiseError("dbcc.command %s: args must be a list of strings", c.Name)
			}
			decl = append(decl, string(s))
		}
	case *lua.LNilType:
	default:
		L.RaiseError("dbcc.command %s: field \"args\" must be a list", c.Name)
	}
	specs, err := ParseArgSpecs(decl)
	if err != nil {
		L.RaiseError("dbcc.command %s: %v", c.Name, err)
	}
	c.Args = specs
	fdecl := map[string]string{}
	switch v := t.RawGetString("flags").(type) {
	case *lua.LTable:
		var ferr string
		v.ForEach(func(k, val lua.LValue) {
			ks, ok1 := k.(lua.LString)
			vs, ok2 := val.(lua.LString)
			if !ok1 || !ok2 {
				ferr = "flags must map flag names to type strings, e.g. { a = \"bool\", t = \"string\" }"
				return
			}
			fdecl[string(ks)] = string(vs)
		})
		if ferr != "" {
			L.RaiseError("dbcc.command %s: %s", c.Name, ferr)
		}
	case *lua.LNilType:
	default:
		L.RaiseError("dbcc.command %s: field \"flags\" must be a table", c.Name)
	}
	fspecs, err := ParseFlagSpecs(fdecl)
	if err != nil {
		L.RaiseError("dbcc.command %s: %v", c.Name, err)
	}
	c.Flags = fspecs
	switch v := t.RawGetString("flag_values").(type) {
	case *lua.LTable:
		var ferr string
		v.ForEach(func(k, val lua.LValue) {
			if ferr != "" {
				return
			}
			ks, ok1 := k.(lua.LString)
			list, ok2 := val.(*lua.LTable)
			src, isSrc := val.(lua.LString)
			if !ok1 || (!ok2 && !isSrc) || (isSrc && !FlagValueSources[string(src)]) {
				// A string names a dynamic source: the registered export formats.
				ferr = "flag_values must map flag names to lists of strings, e.g. { t = { \"table\", \"view\" } }, or to \"exporters\" or \"importers\""
				return
			}
			idx := -1
			for i, f := range c.Flags {
				if f.Name == string(ks) {
					idx = i
				}
			}
			if idx < 0 || !c.Flags[idx].TakesValue() {
				ferr = fmt.Sprintf("flag_values.%s: no such flag with a value", ks)
				return
			}
			if isSrc {
				c.Flags[idx].ValuesFrom = string(src)
				return
			}
			var vals []string
			for i := 1; i <= list.Len(); i++ {
				s, ok := list.RawGetInt(i).(lua.LString)
				if !ok {
					ferr = fmt.Sprintf("flag_values.%s must be a list of strings", ks)
					return
				}
				vals = append(vals, string(s))
			}
			c.Flags[idx].Values = vals
		})
		if ferr != "" {
			L.RaiseError("dbcc.command %s: %s", c.Name, ferr)
		}
	case *lua.LNilType:
	default:
		L.RaiseError("dbcc.command %s: field \"flag_values\" must be a table", c.Name)
	}
	if c.Usage == "" {
		c.Usage = defaultUsage(c.Name, fspecs, specs)
	}
	fn, ok := t.RawGetString("run").(*lua.LFunction)
	if !ok {
		L.RaiseError("dbcc.command %s: field \"run\" must be a function", c.Name)
	}
	c.fn = fn
	rt.staged = append(rt.staged, c)
	return 0
}

// dbcc.commands() -> { {name=, usage=, help=, args={...}, flags={name=type}, write=, source=, default=}, ... }
func (rt *Runtime) luaCommands(L *lua.LState) int {
	out := L.NewTable()
	for _, c := range rt.Commands() {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(c.Name))
		e.RawSetString("usage", lua.LString(c.Usage))
		e.RawSetString("help", lua.LString(c.Help))
		args := L.NewTable()
		for _, a := range c.Args {
			args.Append(lua.LString(a.String()))
		}
		e.RawSetString("args", args)
		flags := L.NewTable()
		for _, f := range c.Flags {
			t := string(f.Type)
			if f.Repeated {
				t += "..."
			}
			flags.RawSetString(f.Name, lua.LString(t))
		}
		e.RawSetString("flags", flags)
		e.RawSetString("write", lua.LBool(c.Write))
		e.RawSetString("source", lua.LString(c.Source))
		e.RawSetString("default", lua.LBool(c.Default))
		out.Append(e)
	}
	L.Push(out)
	return 1
}

// dbcc.fmt.table(headers, rows) -> string. Rows are lists; nil is NULL.
// Columns whose values are all numbers are right-aligned.
func (rt *Runtime) luaFmtTable(L *lua.LState) int {
	headers := stringList(L, L.CheckTable(1), "headers")
	cells, numeric := cellRows(L, L.CheckTable(2), len(headers))
	L.Push(lua.LString(export.FormatTable(headers, cells, numeric)))
	return 1
}

func stringList(L *lua.LState, t *lua.LTable, what string) []string {
	out := make([]string, 0, t.Len())
	for i := 1; i <= t.Len(); i++ {
		out = append(out, L.ToStringMeta(t.RawGetInt(i)).String())
	}
	return out
}

// cellRows converts a list of row lists into text cells. width is the
// minimum number of columns (holes from nil are rendered as NULL).
// numeric[i] is true when every non-nil value of column i is a number.
func cellRows(L *lua.LState, t *lua.LTable, width int) ([][]string, []bool) {
	n := t.Len()
	rows := make([][]string, 0, n)
	cols := width
	for i := 1; i <= n; i++ {
		if r, ok := t.RawGetInt(i).(*lua.LTable); ok && r.MaxN() > cols {
			cols = r.MaxN()
		}
	}
	numeric := make([]bool, cols)
	seen := make([]bool, cols)
	for i := range numeric {
		numeric[i] = true
	}
	for i := 1; i <= n; i++ {
		r, ok := t.RawGetInt(i).(*lua.LTable)
		if !ok {
			L.RaiseError("row %d is not a list", i)
		}
		cells := make([]string, cols)
		for j := 0; j < cols; j++ {
			v := r.RawGetInt(j + 1)
			if v == lua.LNil {
				cells[j] = "NULL"
				continue
			}
			seen[j] = true
			if _, isNum := v.(lua.LNumber); !isNum {
				numeric[j] = false
			}
			cells[j] = L.ToStringMeta(v).String()
		}
		rows = append(rows, cells)
	}
	for i := range numeric {
		numeric[i] = numeric[i] && seen[i]
	}
	return rows, numeric
}

func luaFieldString(t *lua.LTable, k string) string {
	if s, ok := t.RawGetString(k).(lua.LString); ok {
		return string(s)
	}
	return ""
}

func ensureTable(L *lua.LState, n int, what string) *lua.LTable {
	v := L.Get(n)
	if v == lua.LNil {
		return nil
	}
	t, ok := v.(*lua.LTable)
	if !ok {
		L.ArgError(n, fmt.Sprintf("%s must be a table, got %s", what, v.Type()))
	}
	return t
}

// dbcc.fmt.size(bytes) -> "512 B", "16.0 KiB", "1.5 MiB", … (nil -> "").
func luaFmtSize(L *lua.LState) int {
	v := L.Get(1)
	if v == lua.LNil {
		L.Push(lua.LString(""))
		return 1
	}
	L.Push(lua.LString(FormatSize(float64(L.CheckNumber(1)))))
	return 1
}

// FormatSize renders a byte count like dbcc.fmt.size: "512 B", "16.0 KiB",
// "1.5 MiB", … (also used by the file dialog).
func FormatSize(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", n)
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

// dbcc.fmt.count(n) -> "1,234,567".
func luaFmtCount(L *lua.LState) int {
	n := int64(L.CheckNumber(1))
	s := exec.FormatCount(int(n))
	L.Push(lua.LString(s))
	return 1
}
