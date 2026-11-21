package script

// Primitives of M4/M5: metadata, sources, copy and replace scripts,
// export and history (spec 13).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/export"
	"dbcc/internal/meta"
)

// objectArg reads argument n as an object: a name (resolved against the
// ctx like ctx:resolve, restricted to kinds) or an object table from
// ctx:resolve.
func (rt *Runtime) objectArg(L *lua.LState, self *lua.LTable, n int, kinds dialect.ObjectKinds) *Resolved {
	switch v := L.Get(n).(type) {
	case lua.LString:
		r, err := rt.ResolveObject(rt.gctx(), luaFieldString(self, "conn"), string(v), kinds)
		if err != nil {
			rt.raise(err)
		}
		return r
	case *lua.LTable:
		p, ok := objectPath(v)
		if !ok {
			L.ArgError(n, "an object name or an object from ctx:resolve")
		}
		s, err := rt.env.Workspace.Open(rt.gctx(), p.Conn)
		if err != nil {
			rt.raise(err)
		}
		r := &Resolved{Object: meta.Object{Name: p.Object, Kind: meta.ObjectKind(luaFieldString(v, "kind")),
			Comment: luaFieldString(v, "comment")}, Path: p, SQL: luaFieldString(v, "sql"), Session: s}
		if r.SQL == "" {
			r.SQL = s.Dialect.Qualify(p)
		}
		if len(kinds) > 0 && !kinds.Has(r.Object.Kind) {
			rt.raisef("%s is a %s", p.Object, r.Object.Kind)
		}
		return r
	}
	L.ArgError(n, "an object name or an object from ctx:resolve")
	return nil
}

// kindOpt reads an optional object kind at argument n ("" when absent).
func kindOpt(L *lua.LState, n int) meta.ObjectKind {
	v := L.Get(n)
	if v == lua.LNil {
		return ""
	}
	s, ok := v.(lua.LString)
	if !ok {
		L.ArgError(n, "kind must be a string")
	}
	k, ok := kindNames[string(s)]
	if !ok {
		L.ArgError(n, fmt.Sprintf("unknown object kind %q", string(s)))
	}
	return k
}

func strList(L *lua.LState, xs []string) *lua.LTable {
	t := L.CreateTable(len(xs), 0)
	for _, x := range xs {
		t.Append(lua.LString(x))
	}
	return t
}

func setStr(t *lua.LTable, k, v string) {
	if v != "" {
		t.RawSetString(k, lua.LString(v))
	}
}

func keyTable(L *lua.LState, k meta.Key) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("name", lua.LString(k.Name))
	t.RawSetString("columns", strList(L, k.Columns))
	return t
}

// metaTable mirrors meta.Table (spec 6.2) as a Lua table:
//
//	{ name=, kind=, sql=, conn=, parts={conn=,database=,schema=,object=},
//	  comment=, view_definition=, options={k=v},
//	  columns={ {name=, position=, data_type=, nullable=, default=(nil: none),
//	             default_expr=, auto_increment=, generated={expression=, stored=}|nil,
//	             on_update=, enum_values={...}, collation=, comment=}, ... },
//	  primary_key={name=, columns={...}}|nil, unique_keys={ {name=, columns=}, ... },
//	  foreign_keys={ {name=, columns=, ref_table={conn=,database=,schema=,object=,sql=},
//	                  ref_columns=, on_update=, on_delete=}, ... },
//	  checks={ {name=, expression=, not_enforced=}, ... },
//	  indexes={ {name=, columns={ {name=, descending=, sub_part=, expression=}, ... },
//	             unique=, type=, invisible=, comment=, include={...}, filter=}, ... },
//	  triggers={ {name=, timing=, event=}, ... } }
func (rt *Runtime) metaTable(r *Resolved, mt *meta.Table) *lua.LTable {
	L := rt.L
	t := rt.objectTable(r)
	t.RawSetString("kind", lua.LString(string(mt.Kind)))
	t.RawSetString("comment", lua.LString(mt.Comment))
	t.RawSetString("view_definition", lua.LString(mt.ViewDefinition))
	opts := L.NewTable()
	for k, v := range mt.Options {
		opts.RawSetString(k, lua.LString(v))
	}
	t.RawSetString("options", opts)

	cols := L.CreateTable(len(mt.Columns), 0)
	for _, c := range mt.Columns {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(c.Name))
		e.RawSetString("position", lua.LNumber(c.Position))
		e.RawSetString("data_type", lua.LString(c.DataType))
		e.RawSetString("nullable", lua.LBool(c.Nullable))
		if c.Default != nil {
			e.RawSetString("default", lua.LString(*c.Default))
		}
		e.RawSetString("default_expr", lua.LBool(c.DefaultExpr))
		e.RawSetString("auto_increment", lua.LBool(c.AutoIncrement))
		if g := c.Generated; g != nil {
			gt := L.NewTable()
			gt.RawSetString("expression", lua.LString(g.Expression))
			gt.RawSetString("stored", lua.LBool(g.Stored))
			e.RawSetString("generated", gt)
		}
		e.RawSetString("on_update", lua.LString(c.OnUpdate))
		e.RawSetString("enum_values", strList(L, c.EnumValues))
		e.RawSetString("collation", lua.LString(c.Collation))
		e.RawSetString("comment", lua.LString(c.Comment))
		cols.Append(e)
	}
	t.RawSetString("columns", cols)

	if mt.PrimaryKey != nil {
		t.RawSetString("primary_key", keyTable(L, *mt.PrimaryKey))
	}
	uks := L.NewTable()
	for _, k := range mt.UniqueKeys {
		uks.Append(keyTable(L, k))
	}
	t.RawSetString("unique_keys", uks)

	fks := L.NewTable()
	for _, fk := range mt.ForeignKeys {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(fk.Name))
		e.RawSetString("columns", strList(L, fk.Columns))
		ref := L.NewTable()
		setStr(ref, "conn", fk.RefTable.Conn)
		setStr(ref, "database", fk.RefTable.Database)
		setStr(ref, "schema", fk.RefTable.Schema)
		setStr(ref, "object", fk.RefTable.Object)
		ref.RawSetString("sql", lua.LString(r.Session.Dialect.Qualify(fk.RefTable)))
		e.RawSetString("ref_table", ref)
		e.RawSetString("ref_columns", strList(L, fk.RefColumns))
		e.RawSetString("on_update", lua.LString(fk.OnUpdate))
		e.RawSetString("on_delete", lua.LString(fk.OnDelete))
		fks.Append(e)
	}
	t.RawSetString("foreign_keys", fks)

	cks := L.NewTable()
	for _, ck := range mt.Checks {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(ck.Name))
		e.RawSetString("expression", lua.LString(ck.Expression))
		e.RawSetString("not_enforced", lua.LBool(ck.NotEnforced))
		cks.Append(e)
	}
	t.RawSetString("checks", cks)

	ixs := L.NewTable()
	for _, ix := range mt.Indexes {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(ix.Name))
		ic := L.NewTable()
		for _, c := range ix.Columns {
			ce := L.NewTable()
			ce.RawSetString("name", lua.LString(c.Name))
			ce.RawSetString("descending", lua.LBool(c.Descending))
			ce.RawSetString("sub_part", lua.LNumber(c.SubPart))
			ce.RawSetString("expression", lua.LBool(c.Expression))
			ic.Append(ce)
		}
		e.RawSetString("columns", ic)
		e.RawSetString("unique", lua.LBool(ix.Unique))
		e.RawSetString("type", lua.LString(ix.Type))
		e.RawSetString("invisible", lua.LBool(ix.Invisible))
		e.RawSetString("comment", lua.LString(ix.Comment))
		e.RawSetString("include", strList(L, ix.Include))
		e.RawSetString("filter", lua.LString(ix.Filter))
		ixs.Append(e)
	}
	t.RawSetString("indexes", ixs)

	trs := L.NewTable()
	for _, tr := range mt.Triggers {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(tr.Name))
		e.RawSetString("timing", lua.LString(tr.Timing))
		e.RawSetString("event", lua.LString(tr.Event))
		trs.Append(e)
	}
	t.RawSetString("triggers", trs)
	return t
}

var dataKinds = dialect.ObjectKinds{meta.KindTable, meta.KindView, meta.KindMaterializedView}

// describe loads the metadata of a resolved table or view.
func (rt *Runtime) describe(r *Resolved) (*meta.Table, error) {
	aux, err := r.Session.AuxFor(rt.gctx(), r.Path.Database)
	if err != nil {
		return nil, err
	}
	mt, err := r.Session.Dialect.DescribeTable(rt.gctx(), aux, r.Path)
	if err != nil {
		return nil, err
	}
	if mt.Path.Conn == "" {
		mt.Path.Conn = r.Path.Conn
	}
	return mt, nil
}

// ctx:meta(name) -> metadata table of a table or view (see metaTable).
func (rt *Runtime) apiMeta(L *lua.LState, self *lua.LTable) int {
	r := rt.objectArg(L, self, 2, dataKinds)
	mt, err := rt.describe(r)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(rt.metaTable(r, mt))
	return 1
}

// sourceArgs resolves (name, kind): kind restricts the lookup and
// defaults to the kind of the resolved object.
func (rt *Runtime) sourceArgs(L *lua.LState, self *lua.LTable) (*Resolved, meta.ObjectKind) {
	kind := kindOpt(L, 3)
	var kinds dialect.ObjectKinds
	if kind != "" {
		kinds = dialect.ObjectKinds{kind}
	}
	r := rt.objectArg(L, self, 2, kinds)
	if kind == "" {
		kind = r.Object.Kind
	}
	return r, kind
}

// ctx:source(name, kind) -> source text of a view, procedure, function,
// trigger or event (kind optional: the kind of the object).
func (rt *Runtime) apiSource(L *lua.LState, self *lua.LTable) int {
	r, kind := rt.sourceArgs(L, self)
	aux, err := r.Session.AuxFor(rt.gctx(), r.Path.Database)
	if err != nil {
		return rt.raise(err)
	}
	src, err := r.Session.Dialect.ObjectSource(rt.gctx(), aux, r.Path, kind)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(lua.LString(src))
	return 1
}

// ctx:replace_script(name, kind) -> runnable script that re-creates the
// object from its current source (spec 9.1).
func (rt *Runtime) apiReplaceScript(L *lua.LState, self *lua.LTable) int {
	r, kind := rt.sourceArgs(L, self)
	aux, err := r.Session.AuxFor(rt.gctx(), r.Path.Database)
	if err != nil {
		return rt.raise(err)
	}
	src, err := r.Session.Dialect.ReplaceScript(rt.gctx(), aux, r.Path, kind)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(lua.LString(src))
	return 1
}

// targetPath parses the name of a new object next to src: obj,
// db.obj (no schema level), schema.obj or db.schema.obj, or a path
// /conn/db[/schema]/obj or ../db/obj on the same connection.
func (rt *Runtime) targetPath(src *Resolved, name string) (meta.Path, error) {
	name = strings.TrimSpace(name)
	d := src.Session.Dialect
	at := meta.Path{Conn: src.Path.Conn, Database: src.Path.Database, Schema: src.Path.Schema}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		dir, base := name[:i], name[i+1:]
		if dir == "" {
			dir = "/"
		}
		dp, err := rt.env.Workspace.Resolve(dir)
		if err != nil {
			return meta.Path{}, err
		}
		if dp.Conn != src.Path.Conn {
			return meta.Path{}, fmt.Errorf("%q: the target must be on the connection of the source (%s)", name, src.Path.Conn)
		}
		if dp.Database == "" || (d.HasSchemas() && dp.Schema == "") {
			return meta.Path{}, fmt.Errorf("%q: the target path needs a database%s", name, map[bool]string{true: " and a schema"}[d.HasSchemas()])
		}
		at.Database, at.Schema = dp.Database, dp.Schema
		name = base
		parts, err := splitQualified(name)
		if err != nil {
			return meta.Path{}, err
		}
		if len(parts) != 1 {
			return meta.Path{}, fmt.Errorf("%q: use either a path or a qualified name", name)
		}
		at.Object = parts[0]
		return at, nil
	}
	parts, err := splitQualified(name)
	if err != nil {
		return meta.Path{}, err
	}
	switch {
	case len(parts) == 1:
		at.Object = parts[0]
	case len(parts) == 2 && d.HasSchemas():
		at.Schema, at.Object = parts[0], parts[1]
	case len(parts) == 2:
		at.Database, at.Object = parts[0], parts[1]
	case len(parts) == 3 && d.HasSchemas():
		at.Database, at.Schema, at.Object = parts[0], parts[1], parts[2]
	default:
		return meta.Path{}, fmt.Errorf("%q: too many name parts", name)
	}
	return at, nil
}

// ctx:copy_sql(src, dst, opts) -> { stmt, ... }, target. src is a name or
// an object from ctx:resolve (table or view), dst the name of the new
// table; opts.structure selects the cp -a variant (spec 8.2). target is an
// object-like table {name=, conn=, sql=, parts=} for the new table.
func (rt *Runtime) apiCopySQL(L *lua.LState, self *lua.LTable) int {
	src := rt.objectArg(L, self, 2, dataKinds)
	dstName := L.CheckString(3)
	opts := ensureTable(L, 4, "opts")
	structure := false
	if opts != nil {
		structure = lua.LVAsBool(opts.RawGetString("structure"))
	}
	dst, err := rt.targetPath(src, dstName)
	if err != nil {
		return rt.raise(err)
	}
	var mt *meta.Table
	if structure {
		if mt, err = rt.describe(src); err != nil {
			return rt.raise(err)
		}
	}
	stmts, err := src.Session.Dialect.CopyTable(src.Path, dst, mt, structure)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(strList(L, stmts))
	kind := meta.KindTable
	if structure && mt != nil && (mt.Kind == meta.KindView || mt.Kind == meta.KindMaterializedView) {
		kind = mt.Kind
	}
	L.Push(rt.objectTable(&Resolved{Object: meta.Object{Name: dst.Object, Kind: kind}, Path: dst,
		SQL: src.Session.Dialect.Qualify(dst), Session: src.Session}))
	return 2
}

// ExportProgressEvery is the default row interval of export progress
// messages.
var ExportProgressEvery = 10000

// ErrNoResult is raised by ctx:export without an active result.
var ErrNoResult = errors.New("no active result to export")

func expandHome(p string) string {
	if p == "~" {
		return config.HomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(config.HomeDir(), p[2:])
	}
	return p
}

// ctx:export(file, opts) -> rows written. Writes the active result to a
// .csv or .json file (format from the extension). The open cursor is
// continued to the end, ignoring max_rows; an active local filter is
// applied while streaming; an active local sort loads all rows first
// (spec 10.7). opts.force overwrites an existing file; opts.progress sets
// the row interval of progress messages (0: none). Cancellable.
func (rt *Runtime) apiExport(L *lua.LState, self *lua.LTable) int {
	file := expandHome(L.CheckString(2))
	opts := ensureTable(L, 3, "opts")
	force := false
	every := ExportProgressEvery
	if opts != nil {
		force = lua.LVAsBool(opts.RawGetString("force"))
		if n, ok := opts.RawGetString("progress").(lua.LNumber); ok {
			every = int(n)
		}
	}
	format, err := export.FormatFromFile(file)
	if err != nil {
		return rt.raise(err)
	}
	if format != export.CSV && format != export.JSON {
		return rt.raise(fmt.Errorf("unknown export format for %q: use .csv or .json", file))
	}
	res, view := rt.env.Host.ActiveResult()
	if res == nil {
		return rt.raise(ErrNoResult)
	}
	n, err := rt.exportResult(file, format, force, every, res, view)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(lua.LNumber(n))
	return 1
}

func (rt *Runtime) exportResult(file, format string, force bool, every int, res *exec.Result, view exec.View) (n int, err error) {
	flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(file, flag, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("%s already exists; use -f to overwrite", file)
		}
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			os.Remove(file) // no half-written export
		}
	}()
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	w, err := export.New(format, f, nil, exec.CellText)
	if err != nil {
		return 0, err
	}
	if err := w.Header(names); err != nil {
		return 0, err
	}
	ctx := rt.gctx()
	progress := func() {
		if every > 0 && n > 0 && n%every == 0 {
			rt.message(fmt.Sprintf("export %s: %s rows…", filepath.Base(file), exec.FormatCount(n)))
		}
	}
	write := func(rows []exec.Row) error {
		for _, r := range rows {
			if view != nil && !view.Keep(r) {
				continue
			}
			if err := w.Row(r); err != nil {
				return err
			}
			n++
			progress()
		}
		return ctx.Err()
	}
	sorted := view != nil && view.Sorted()
	if sorted {
		all := append([]exec.Row(nil), res.Rows()...)
		if err := res.Drain(ctx, 1000, func(rows []exec.Row) error {
			all = append(all, rows...)
			return nil
		}); err != nil {
			return 0, err
		}
		kept := all[:0:0]
		for _, r := range all {
			if view.Keep(r) {
				kept = append(kept, r)
			}
		}
		for _, r := range view.Order(kept) {
			if err := w.Row(r); err != nil {
				return n, err
			}
			n++
			progress()
		}
	} else {
		if err := write(res.Rows()); err != nil {
			return n, err
		}
		if err := res.Drain(ctx, 1000, write); err != nil {
			return n, err
		}
	}
	if err := w.Close(); err != nil {
		return n, err
	}
	if res.Capped {
		rt.message(fmt.Sprintf("export %s: the result was capped at %s rows before the export; re-run the query to export all rows",
			filepath.Base(file), exec.FormatCount(res.Len())))
	}
	return n, nil
}

// ctx:history(n) -> { {time=, profile=, path=, duration_ms=, ok=, outcome=, stmt=}, ... }
// The last n entries (default 100), oldest first.
func (rt *Runtime) apiHistory(L *lua.LState, self *lua.LTable) int {
	n := L.OptInt(2, 100)
	if rt.env.History == nil {
		return rt.raise(errors.New("no statement history available"))
	}
	entries := rt.env.History.Last(n)
	out := L.CreateTable(len(entries), 0)
	for _, e := range entries {
		t := L.NewTable()
		t.RawSetString("time", lua.LString(e.Time.Local().Format("2006-01-02 15:04:05")))
		t.RawSetString("profile", lua.LString(e.Profile))
		t.RawSetString("path", lua.LString(e.Path))
		t.RawSetString("duration_ms", lua.LNumber(e.Duration))
		t.RawSetString("ok", lua.LBool(e.OK()))
		t.RawSetString("outcome", lua.LString(e.Outcome))
		t.RawSetString("stmt", lua.LString(e.Stmt))
		out.Append(t)
	}
	L.Push(out)
	return 1
}
