package script

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
)

// ErrNotConnected is raised by primitives that need a connection at "/".
var ErrNotConnected = errors.New("not connected: cd /<profile> first")

func (rt *Runtime) ctxMethods() *lua.LTable {
	m := map[string]func(*lua.LState, *lua.LTable) int{
		"chdir":       rt.apiChdir,
		"abspath":     rt.apiAbspath,
		"objects":     rt.apiObjects,
		"resolve":     rt.apiResolve,
		"profiles":    rt.apiProfiles,
		"on":          rt.apiOn,
		"quote":       rt.apiQuote,
		"limit":       rt.apiLimit,
		"query":       rt.apiQuery,
		"exec":        rt.apiExec,
		"grid":        rt.apiGrid,
		"rows":        rt.apiRows,
		"text":        rt.apiText,
		"message":     rt.apiMessage,
		"confirm":     rt.apiConfirm,
		"pick":        rt.apiPick,
		"editor_set":  rt.apiEditorSet,
		"editor_open": rt.apiEditorOpen,
		"about":       rt.apiAbout,
	}
	m["meta"] = rt.apiMeta
	m["source"] = rt.apiSource
	m["copy_sql"] = rt.apiCopySQL
	m["replace_script"] = rt.apiReplaceScript
	m["export"] = rt.apiExport
	m["history"] = rt.apiHistory
	t := rt.L.NewTable()
	for name, f := range m {
		name, f := name, f
		t.RawSetString(name, rt.L.NewFunction(func(L *lua.LState) int {
			self, ok := L.Get(1).(*lua.LTable)
			if !ok || self.Metatable != lua.LValue(rt.ctxMeta) {
				L.RaiseError("ctx:%s: call it with a colon, ctx:%s(...)", name, name)
			}
			if rt.inv == nil {
				L.RaiseError("ctx:%s: ctx is only valid while its command runs", name)
			}
			return f(L, self)
		}))
	}
	return t
}

// newCtx builds the ctx table. conn binds it to another open connection
// (ctx:on); "" means the connection of the current path.
func (rt *Runtime) newCtx(conn string) *lua.LTable {
	t := rt.L.NewTable()
	rt.L.SetMetatable(t, rt.ctxMeta)
	if conn != "" {
		t.RawSetString("conn", lua.LString(conn))
	}
	rt.refreshCtx(t)
	return t
}

// refreshCtx sets the data fields of ctx: command, path, dialect,
// has_schemas, profile.
func (rt *Runtime) refreshCtx(t *lua.LTable) {
	ws := rt.env.Workspace
	if rt.inv != nil {
		t.RawSetString("command", lua.LString(rt.inv.cmd.Name))
	}
	t.RawSetString("path", rt.pathTable(ws.Path()))
	var s *session.Session
	if conn := luaFieldString(t, "conn"); conn != "" {
		s = ws.Sessions()[conn]
	} else {
		s = ws.Current()
	}
	if s == nil {
		t.RawSetString("dialect", lua.LNil)
		t.RawSetString("has_schemas", lua.LFalse)
		t.RawSetString("profile", lua.LNil)
		return
	}
	t.RawSetString("dialect", lua.LString(s.Dialect.Name()))
	t.RawSetString("has_schemas", lua.LBool(s.Dialect.HasSchemas()))
	p := rt.L.NewTable()
	p.RawSetString("name", lua.LString(s.Profile.Name))
	p.RawSetString("driver", lua.LString(s.Profile.Driver))
	if s.Profile.Database != "" {
		p.RawSetString("database", lua.LString(s.Profile.Database))
	}
	p.RawSetString("read_only", lua.LBool(s.Profile.ReadOnly))
	p.RawSetString("autocommit", lua.LBool(s.Profile.IsAutocommit()))
	p.RawSetString("confirm_writes", lua.LBool(s.Profile.ConfirmWrites))
	t.RawSetString("profile", p)
}

func (rt *Runtime) gctx() context.Context {
	if rt.inv != nil && rt.inv.ctx != nil {
		return rt.inv.ctx
	}
	return context.Background()
}

// session returns the session a ctx is bound to.
func (rt *Runtime) session(self *lua.LTable) (*session.Session, error) {
	if conn := luaFieldString(self, "conn"); conn != "" {
		return rt.env.Workspace.Open(rt.gctx(), conn)
	}
	if s := rt.env.Workspace.Current(); s != nil {
		return s, nil
	}
	return nil, ErrNotConnected
}

func (rt *Runtime) mustSession(self *lua.LTable) *session.Session {
	s, err := rt.session(self)
	if err != nil {
		rt.raise(err)
	}
	return s
}

func (rt *Runtime) runner(s *session.Session) *exec.Runner {
	if rt.env.Runner == nil {
		rt.raise(errors.New("no runner configured"))
	}
	return rt.env.Runner(s)
}

// ---- paths ----

func level(p meta.Path) string {
	switch {
	case p.Conn == "":
		return "root"
	case p.Database == "":
		return "connection"
	case p.Schema == "":
		return "database"
	}
	return "schema"
}

// pathTable: {conn=, database=, schema=, str="/c/d/s", level="root|connection|database|schema"};
// absent parts are nil; tostring(path) == path.str.
func (rt *Runtime) pathTable(p meta.Path) *lua.LTable {
	t := rt.L.NewTable()
	set := func(k, v string) {
		if v != "" {
			t.RawSetString(k, lua.LString(v))
		}
	}
	set("conn", p.Conn)
	set("database", p.Database)
	set("schema", p.Schema)
	t.RawSetString("str", lua.LString(p.String()))
	t.RawSetString("level", lua.LString(level(p)))
	rt.L.SetMetatable(t, rt.pathMeta)
	return t
}

// pathArg accepts a string or a path table (its str field) at argument n.
func pathArg(L *lua.LState, n int, def string) string {
	switch v := L.Get(n).(type) {
	case lua.LString:
		return string(v)
	case *lua.LTable:
		if s := luaFieldString(v, "str"); s != "" {
			return s
		}
		L.ArgError(n, "path table without str field")
	case *lua.LNilType:
		return def
	default:
		L.ArgError(n, "path must be a string or a path table")
	}
	return def
}

// ctx:chdir(path) -> path. Updates ctx.path, ctx.dialect, ctx.profile.
func (rt *Runtime) apiChdir(L *lua.LState, self *lua.LTable) int {
	p, err := rt.env.Workspace.Chdir(rt.gctx(), pathArg(L, 2, ""))
	if err != nil {
		return rt.raise(err)
	}
	rt.refreshCtx(self)
	L.Push(rt.pathTable(p))
	return 1
}

// ctx:abspath(path) -> path. Resolves without side effects ("." = current).
func (rt *Runtime) apiAbspath(L *lua.LState, self *lua.LTable) int {
	p, err := rt.env.Workspace.Resolve(pathArg(L, 2, "."))
	if err != nil {
		return rt.raise(err)
	}
	L.Push(rt.pathTable(p))
	return 1
}

var kindNames = map[string]meta.ObjectKind{}

func init() {
	for _, k := range []meta.ObjectKind{meta.KindDatabase, meta.KindSchema, meta.KindTable, meta.KindView,
		meta.KindMaterializedView, meta.KindProcedure, meta.KindFunction, meta.KindTrigger, meta.KindEvent, meta.KindSequence} {
		kindNames[string(k)] = k
	}
}

func kindsArg(L *lua.LState, n int) dialect.ObjectKinds {
	var out dialect.ObjectKinds
	switch v := L.Get(n).(type) {
	case *lua.LNilType:
	case lua.LString:
		k, ok := kindNames[string(v)]
		if !ok {
			L.ArgError(n, fmt.Sprintf("unknown object kind %q", string(v)))
		}
		out = append(out, k)
	case *lua.LTable:
		for i := 1; i <= v.Len(); i++ {
			s := v.RawGetInt(i).String()
			k, ok := kindNames[s]
			if !ok {
				L.ArgError(n, fmt.Sprintf("unknown object kind %q", s))
			}
			out = append(out, k)
		}
	default:
		L.ArgError(n, "kinds must be a string or a list of strings")
	}
	return out
}

func (rt *Runtime) objectList(objs []meta.Object) *lua.LTable {
	out := rt.L.CreateTable(len(objs), 0)
	for _, o := range objs {
		e := rt.L.NewTable()
		e.RawSetString("name", lua.LString(o.Name))
		e.RawSetString("kind", lua.LString(string(o.Kind)))
		e.RawSetString("comment", lua.LString(o.Comment))
		e.RawSetString("system", lua.LBool(o.System))
		out.Append(e)
	}
	return out
}

// ctx:objects(path, kinds) -> { {name=, kind=, comment=, system=}, ... }
// At /conn: databases; at a database of a schema dialect: schemas; else
// the objects of the database (or schema), filtered by kinds.
func (rt *Runtime) apiObjects(L *lua.LState, self *lua.LTable) int {
	ws := rt.env.Workspace
	p, err := ws.Resolve(pathArg(L, 2, "."))
	if err != nil {
		return rt.raise(err)
	}
	kinds := kindsArg(L, 3)
	if p.Conn == "" {
		return rt.raise(errors.New("ctx:objects: \"/\" holds connection profiles; use ctx:profiles()"))
	}
	s, err := ws.Open(rt.gctx(), p.Conn)
	if err != nil {
		return rt.raise(err)
	}
	d := s.Dialect
	var objs []meta.Object
	if p.Database == "" {
		objs, err = d.ListDatabases(rt.gctx(), s.Aux())
	} else {
		if p.Schema != "" && !d.HasSchemas() {
			return rt.raisef("%s has no schema level: %s", d.Name(), p)
		}
		// PostgreSQL reads the catalog of another database only on a
		// connection to it.
		aux, aerr := s.AuxFor(rt.gctx(), p.Database)
		if aerr != nil {
			return rt.raise(aerr)
		}
		if d.HasSchemas() && p.Schema == "" {
			objs, err = d.ListSchemas(rt.gctx(), aux, p.Database)
		} else {
			objs, err = d.ListObjects(rt.gctx(), aux, p, kinds)
		}
	}
	if err != nil {
		return rt.raise(err)
	}
	L.Push(rt.objectList(objs))
	return 1
}

// ctx:profiles() -> { {name=, driver=, host=, port=, socket=, user=, database=,
// read_only=, autocommit=, confirm_writes=, open=, current=}, ... } sorted by name.
func (rt *Runtime) apiProfiles(L *lua.LState, self *lua.LTable) int {
	ws := rt.env.Workspace
	open := ws.Sessions()
	cur := ws.Path().Conn
	names := make([]string, 0)
	if ws.Config != nil {
		for n := range ws.Config.Profiles {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := L.CreateTable(len(names), 0)
	for _, n := range names {
		p := ws.Config.Profiles[n]
		e := L.NewTable()
		e.RawSetString("name", lua.LString(n))
		e.RawSetString("driver", lua.LString(p.Driver))
		e.RawSetString("host", lua.LString(p.Host))
		e.RawSetString("port", lua.LNumber(p.Port))
		e.RawSetString("socket", lua.LString(p.Socket))
		e.RawSetString("user", lua.LString(p.User))
		e.RawSetString("database", lua.LString(p.Database))
		e.RawSetString("read_only", lua.LBool(p.ReadOnly))
		e.RawSetString("autocommit", lua.LBool(p.IsAutocommit()))
		e.RawSetString("confirm_writes", lua.LBool(p.ConfirmWrites))
		_, isOpen := open[n]
		e.RawSetString("open", lua.LBool(isOpen))
		e.RawSetString("current", lua.LBool(n == cur))
		out.Append(e)
	}
	L.Push(out)
	return 1
}

// ---- objects ----

// splitQualified splits db.obj / schema.obj / db.schema.obj, honouring
// "…", `…` and […] quoting (doubled quote characters escape themselves).
func splitQualified(name string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	quoted := false
	rs := []rune(name)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case '"', '`', '[':
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			closed := false
			for ; i < len(rs); i++ {
				if rs[i] == end {
					if i+1 < len(rs) && rs[i+1] == end {
						cur.WriteRune(end)
						i++
						continue
					}
					closed = true
					break
				}
				cur.WriteRune(rs[i])
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quote in %q", name)
			}
			quoted = true
		case '.':
			if cur.Len() == 0 && !quoted {
				return nil, fmt.Errorf("empty name part in %q", name)
			}
			parts = append(parts, cur.String())
			cur.Reset()
			quoted = false
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() == 0 && !quoted {
		return nil, fmt.Errorf("empty name part in %q", name)
	}
	return append(parts, cur.String()), nil
}

// Resolved is the result of resolving an object name.
type Resolved struct {
	Object  meta.Object
	Path    meta.Path // with Object set
	SQL     string    // qualified, quoted name
	Session *session.Session
}

// ResolveObject resolves name for the ctx bound to conn ("" = current):
//   - obj, db.obj (MySQL/MariaDB), schema.obj, db.schema.obj (schema dialects),
//     with SQL-style quoting of the parts;
//   - absolute or relative paths whose last segment is the object:
//     /conn/db/obj, /conn/db/schema/obj, ../otherdb/obj. Such a path may name
//     another profile; its session is opened if needed.
//
// The object is looked up with Dialect.ListObjects on the auxiliary
// connection to get its kind; kinds (nil = all) restricts the lookup.
func (rt *Runtime) ResolveObject(ctx context.Context, conn, name string, kinds dialect.ObjectKinds) (*Resolved, error) {
	ws := rt.env.Workspace
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("empty object name")
	}
	var s *session.Session
	var at meta.Path
	var err error
	if i := strings.LastIndex(name, "/"); i >= 0 {
		dir, base := name[:i], name[i+1:]
		if dir == "" {
			dir = "/"
		}
		if base == "" {
			return nil, fmt.Errorf("%q names a path, not an object", name)
		}
		dp, err := ws.Resolve(dir)
		if err != nil {
			return nil, err
		}
		if dp.Conn == "" || dp.Database == "" {
			return nil, fmt.Errorf("%q: an object path needs /connection/database/object", name)
		}
		if s, err = ws.Open(ctx, dp.Conn); err != nil {
			return nil, err
		}
		if s.Dialect.HasSchemas() && dp.Schema == "" {
			return nil, fmt.Errorf("%q: %s needs /connection/database/schema/object", name, s.Dialect.Name())
		}
		if !s.Dialect.HasSchemas() && dp.Schema != "" {
			return nil, fmt.Errorf("%q: %s has no schema level", name, s.Dialect.Name())
		}
		parts, err := splitQualified(base)
		if err != nil {
			return nil, err
		}
		if len(parts) != 1 {
			return nil, fmt.Errorf("%q: use either a path or a qualified name", name)
		}
		at = dp
		at.Object = parts[0]
	} else {
		if conn != "" {
			s, err = ws.Open(ctx, conn)
		} else if s = ws.Current(); s == nil {
			err = ErrNotConnected
		}
		if err != nil {
			return nil, err
		}
		parts, err := splitQualified(name)
		if err != nil {
			return nil, err
		}
		cur := s.Path()
		at = meta.Path{Conn: s.Profile.Name, Database: cur.Database, Schema: cur.Schema}
		if s.Dialect.HasSchemas() {
			switch len(parts) {
			case 1:
				at.Object = parts[0]
			case 2:
				at.Schema, at.Object = parts[0], parts[1]
			case 3:
				at.Database, at.Schema, at.Object = parts[0], parts[1], parts[2]
			default:
				return nil, fmt.Errorf("%q: too many name parts", name)
			}
		} else {
			switch len(parts) {
			case 1:
				at.Object = parts[0]
			case 2:
				at.Database, at.Object = parts[0], parts[1]
			default:
				return nil, fmt.Errorf("%q: too many name parts (%s has no schema level)", name, s.Dialect.Name())
			}
		}
		if at.Database == "" {
			return nil, fmt.Errorf("%q: no database selected; cd into a database or qualify the name", name)
		}
	}
	list := at
	list.Object = ""
	aux, err := s.AuxFor(ctx, at.Database)
	if err != nil {
		return nil, err
	}
	objs, err := s.Dialect.ListObjects(ctx, aux, list, kinds)
	if err != nil {
		return nil, err
	}
	var found *meta.Object
	for i := range objs {
		if objs[i].Name == at.Object {
			found = &objs[i]
			break
		}
	}
	if found == nil {
		var folded []int
		for i := range objs {
			if strings.EqualFold(objs[i].Name, at.Object) {
				folded = append(folded, i)
			}
		}
		if len(folded) == 1 {
			found = &objs[folded[0]]
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no such object: %s in %s", at.Object, list)
	}
	at.Object = found.Name
	return &Resolved{Object: *found, Path: at, SQL: s.Dialect.Qualify(at), Session: s}, nil
}

// object table: {name=, kind=, comment=, conn=, parts={conn=,database=,schema=,object=}, sql=};
// tostring(obj) == obj.sql.
func (rt *Runtime) objectTable(r *Resolved) *lua.LTable {
	L := rt.L
	t := L.NewTable()
	t.RawSetString("name", lua.LString(r.Object.Name))
	t.RawSetString("kind", lua.LString(string(r.Object.Kind)))
	t.RawSetString("comment", lua.LString(r.Object.Comment))
	t.RawSetString("conn", lua.LString(r.Path.Conn))
	t.RawSetString("sql", lua.LString(r.SQL))
	parts := L.NewTable()
	for k, v := range map[string]string{"conn": r.Path.Conn, "database": r.Path.Database, "schema": r.Path.Schema, "object": r.Path.Object} {
		if v != "" {
			parts.RawSetString(k, lua.LString(v))
		}
	}
	t.RawSetString("parts", parts)
	L.SetMetatable(t, rt.objMeta)
	return t
}

func objectPath(t *lua.LTable) (meta.Path, bool) {
	parts, ok := t.RawGetString("parts").(*lua.LTable)
	if !ok {
		return meta.Path{}, false
	}
	p := meta.Path{Conn: luaFieldString(parts, "conn"), Database: luaFieldString(parts, "database"),
		Schema: luaFieldString(parts, "schema"), Object: luaFieldString(parts, "object")}
	return p, p.Object != ""
}

// ctx:resolve(name, kinds) -> object
func (rt *Runtime) apiResolve(L *lua.LState, self *lua.LTable) int {
	name := L.CheckString(2)
	r, err := rt.ResolveObject(rt.gctx(), luaFieldString(self, "conn"), name, kindsArg(L, 3))
	if err != nil {
		return rt.raise(err)
	}
	L.Push(rt.objectTable(r))
	return 1
}

// ctx:on(target) -> ctx bound to the connection of target (a profile name,
// a resolved object or a path table). Its SQL helpers and execution
// primitives run on that connection; ctx.path stays the current path.
func (rt *Runtime) apiOn(L *lua.LState, self *lua.LTable) int {
	var conn string
	switch v := L.Get(2).(type) {
	case lua.LString:
		conn = string(v)
	case *lua.LTable:
		conn = luaFieldString(v, "conn")
	}
	if conn == "" {
		L.ArgError(2, "a profile name, an object from ctx:resolve or a path with a connection")
	}
	if _, err := rt.env.Workspace.Open(rt.gctx(), conn); err != nil {
		return rt.raise(err)
	}
	if conn == rt.env.Workspace.Path().Conn {
		conn = "" // the current connection: follow it like ctx does
	}
	L.Push(rt.newCtx(conn))
	return 1
}

// ---- SQL helpers ----

func (rt *Runtime) apiQuote(L *lua.LState, self *lua.LTable) int {
	ident := L.CheckString(2)
	L.Push(lua.LString(rt.mustSession(self).Dialect.QuoteIdent(ident)))
	return 1
}

func (rt *Runtime) apiLimit(L *lua.LState, self *lua.LTable) int {
	q := L.CheckString(2)
	n := L.CheckInt(3)
	if n < 0 {
		L.ArgError(3, "limit must not be negative")
	}
	L.Push(lua.LString(rt.mustSession(self).Dialect.LimitQuery(q, n)))
	return 1
}

// ---- execution ----

func luaToGo(L *lua.LState, n int) any {
	switch v := L.Get(n).(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(v)
	case lua.LNumber:
		f := float64(v)
		if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f)
		}
		return f
	case lua.LString:
		return string(v)
	default:
		L.ArgError(n, "parameters must be nil, booleans, numbers or strings, got "+v.Type().String())
	}
	return nil
}

func params(L *lua.LState, from int) []any {
	var out []any
	for i := from; i <= L.GetTop(); i++ {
		out = append(out, luaToGo(L, i))
	}
	return out
}

// bind prepares a statement of ctx:query, ctx:exec or ctx:grid: with
// arguments, the portable "?" placeholders a script writes are rewritten
// into the dialect's own ($1 on PostgreSQL, @p1 on MSSQL; dialect.Rebind).
// Without arguments q is left alone ("?" is also an operator on
// PostgreSQL). Scripts may also write the native placeholders directly.
func bind(s *session.Session, q string, args []any) string {
	if len(args) == 0 {
		return q
	}
	return dialect.Rebind(s.Dialect, q)
}

func cellToLua(v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(x)
	case exec.Binary:
		return lua.LString(string(x))
	default:
		return lua.LString(exec.CellText(x))
	}
}

// resultTable: an array of rows plus columns (list of names) and capped.
// Each row is an array of values (string, nil for NULL); row.colname also
// works through the row metatable.
func (rt *Runtime) resultTable(res *exec.Result) *lua.LTable {
	L := rt.L
	rows := res.Rows()
	t := L.CreateTable(len(rows), 2)
	cols := L.CreateTable(len(res.Columns), 0)
	index := map[string]int{}
	for i, c := range res.Columns {
		cols.Append(lua.LString(c.Name))
		if _, dup := index[c.Name]; !dup {
			index[c.Name] = i + 1
		}
	}
	rowMeta := L.NewTable()
	rowMeta.RawSetString("__index", L.NewFunction(func(L *lua.LState) int {
		row := L.CheckTable(1)
		if k, ok := L.Get(2).(lua.LString); ok {
			if i, ok := index[string(k)]; ok {
				L.Push(row.RawGetInt(i))
				return 1
			}
		}
		L.Push(lua.LNil)
		return 1
	}))
	for _, r := range rows {
		rowT := L.CreateTable(len(r), 0)
		for i, v := range r {
			rowT.RawSetInt(i+1, cellToLua(v))
		}
		L.SetMetatable(rowT, rowMeta)
		t.Append(rowT)
	}
	t.RawSetString("columns", cols)
	t.RawSetString("capped", lua.LBool(res.Capped))
	return t
}

// ctx:query(sql, ...) -> result (see resultTable). Buffered, capped at
// max_rows_buffered; the runner's read-only guard and confirm_writes apply.
func (rt *Runtime) apiQuery(L *lua.LState, self *lua.LTable) int {
	q := L.CheckString(2)
	s := rt.mustSession(self)
	args := params(L, 3)
	res, err := rt.runner(s).Query(rt.gctx(), bind(s, q, args), args...)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(rt.resultTable(res))
	return 1
}

// ctx:exec(sql, ...) -> affected rows. In a write command the statement is
// first shown through Host.Confirm; the runner guards apply as well.
func (rt *Runtime) apiExec(L *lua.LState, self *lua.LTable) int {
	s := rt.mustSession(self)
	args := params(L, 3)
	q := bind(s, L.CheckString(2), args)
	r := rt.runner(s)
	if rt.inv.cmd.Write {
		if s.Profile.ReadOnly {
			return rt.raise(fmt.Errorf("%w (profile %s)", exec.ErrReadOnly, s.Profile.Name))
		}
		// A "yes" to ctx:confirm earlier in this invocation (which showed
		// all statements at once, as cp does) covers this statement.
		if !rt.inv.confirmed {
			ok, err := rt.env.Host.Confirm(fmt.Sprintf("%s will run on %s:\n\n%s", rt.inv.cmd.Name, s.Profile.Name, q))
			if err != nil {
				return rt.raise(err)
			}
			if !ok {
				return rt.raise(exec.ErrDeclined)
			}
		}
		// Confirmed once: confirm_writes must not ask again for this statement.
		old := r.Confirm
		r.Confirm = func(stmt string) bool { return stmt == q || (old != nil && old(stmt)) }
		defer func() { r.Confirm = old }()
	}
	n, err := r.Exec(rt.gctx(), q, args...)
	if err != nil {
		return rt.raise(err)
	}
	L.Push(lua.LNumber(n))
	return 1
}

// ctx:grid(sql, opts, ...) opens a lazily loaded result. opts.origin (an
// object from ctx:resolve) marks it table-origin and runs it on the
// object's connection; opts.title names the tab.
//
// opts.follow = {sql=, key=, interval=} makes it a following result (tail
// -f, spec A26): after the result's rows are loaded, follow.sql — a query
// with one "?" for the last seen value of column follow.key (a column of
// the result), returning the newer rows ordered by it ascending — is run
// every follow.interval seconds (default 2, at least 0.5) on the
// auxiliary connection of the session, and its rows are appended at the
// end of the result, which keeps at most page_size rows. The call returns
// at once; polling stops when the result is closed, cancelled or its
// session closes.
func (rt *Runtime) apiGrid(L *lua.LState, self *lua.LTable) int {
	q := L.CheckString(2)
	opts := ensureTable(L, 3, "opts")
	var origin *exec.Origin
	var s *session.Session
	var err error
	title := ""
	var follow *lua.LTable
	if opts != nil {
		title = luaFieldString(opts, "title")
		if o, ok := opts.RawGetString("origin").(*lua.LTable); ok {
			p, ok := objectPath(o)
			if !ok {
				L.ArgError(3, "opts.origin must be an object from ctx:resolve")
			}
			origin = &exec.Origin{Object: p, SQL: luaFieldString(o, "sql")}
			if s, err = rt.env.Workspace.Open(rt.gctx(), p.Conn); err != nil {
				return rt.raise(err)
			}
		}
		switch v := opts.RawGetString("follow").(type) {
		case *lua.LNilType:
		case *lua.LTable:
			follow = v
		default:
			L.ArgError(3, "opts.follow must be a table {sql=, key=, interval=}")
		}
	}
	if s == nil {
		s = rt.mustSession(self)
	}
	var spec exec.FollowSpec
	if follow != nil {
		spec = rt.followSpec(L, s, origin, follow)
	}
	args := params(L, 4)
	res, err := rt.runner(s).Grid(rt.gctx(), bind(s, q, args), origin, args...)
	if err != nil {
		return rt.raise(err)
	}
	if title != "" {
		res.Title = title
	}
	if follow != nil {
		if err := res.SetFollow(spec); err != nil {
			res.Release()
			return rt.raise(err)
		}
	}
	rt.env.Host.Result(res)
	res.StartFollow()
	return 0
}

// followSpec reads opts.follow of ctx:grid. Polls run on the auxiliary
// connection for the database of the origin (or the session's current one).
func (rt *Runtime) followSpec(L *lua.LState, s *session.Session, origin *exec.Origin, f *lua.LTable) exec.FollowSpec {
	fq := luaFieldString(f, "sql")
	key := luaFieldString(f, "key")
	if fq == "" || key == "" {
		L.ArgError(3, "opts.follow needs sql (with one ? for the last key) and key (a column of the result)")
	}
	interval := exec.DefaultFollowInterval
	switch v := f.RawGetString("interval").(type) {
	case *lua.LNilType:
	case lua.LNumber:
		interval = time.Duration(float64(v) * float64(time.Second))
		if interval < exec.MinFollowInterval {
			L.ArgError(3, fmt.Sprintf("opts.follow.interval must be at least %g seconds", exec.MinFollowInterval.Seconds()))
		}
	default:
		L.ArgError(3, "opts.follow.interval must be a number of seconds")
	}
	db := s.Database()
	if origin != nil && origin.Object.Database != "" {
		db = origin.Object.Database
	}
	return exec.FollowSpec{
		SQL:      dialect.Rebind(s.Dialect, fq),
		Key:      key,
		Interval: interval,
		Conn: func(ctx context.Context) (exec.Querier, error) {
			return s.AuxFor(ctx, db)
		},
		Gone: s.Done(),
	}
}

// ---- output ----

func (rt *Runtime) title(opts *lua.LTable) string {
	if opts != nil {
		if t := luaFieldString(opts, "title"); t != "" {
			return t
		}
	}
	return rt.inv.cmd.Name
}

// ctx:rows(headers, rows, opts) shows rows computed in Lua. nil is NULL;
// columns whose values are all numbers are numeric. opts.title.
func (rt *Runtime) apiRows(L *lua.LState, self *lua.LTable) int {
	headers := stringList(L, L.CheckTable(2), "headers")
	rows := L.CheckTable(3)
	opts := ensureTable(L, 4, "opts")
	n := len(headers)
	_, numeric := cellRows(L, rows, n)
	if len(numeric) > n {
		for i := n; i < len(numeric); i++ {
			headers = append(headers, fmt.Sprintf("col%d", i+1))
		}
		n = len(numeric)
	}
	cols := make([]exec.ColumnInfo, n)
	for i := range cols {
		cols[i] = exec.ColumnInfo{Name: headers[i], Numeric: numeric[i]}
	}
	data := make([]exec.Row, 0, rows.Len())
	for i := 1; i <= rows.Len(); i++ {
		r := rows.RawGetInt(i).(*lua.LTable)
		row := make(exec.Row, n)
		for j := 0; j < n; j++ {
			if v := r.RawGetInt(j + 1); v != lua.LNil {
				row[j] = L.ToStringMeta(v).String()
			}
		}
		data = append(data, row)
	}
	rt.env.Host.Result(exec.NewResult(rt.title(opts), cols, data))
	return 0
}

// ctx:text(str, lexer, opts) shows text. lexer "sql" means the SQL lexer
// of the ctx's dialect; "" or nil is plain text. opts.title.
func (rt *Runtime) apiText(L *lua.LState, self *lua.LTable) int {
	text := L.ToStringMeta(L.CheckAny(2)).String()
	lexer := L.OptString(3, "")
	opts := ensureTable(L, 4, "opts")
	if lexer == "sql" {
		if s, err := rt.session(self); err == nil {
			lexer = s.Dialect.LexerName()
		}
	}
	rt.env.Host.Text(rt.title(opts), text, lexer)
	return 0
}

func (rt *Runtime) apiMessage(L *lua.LState, self *lua.LTable) int {
	rt.message(L.ToStringMeta(L.CheckAny(2)).String())
	return 0
}

// ---- interaction ----

func (rt *Runtime) apiConfirm(L *lua.LState, self *lua.LTable) int {
	ok, err := rt.env.Host.Confirm(L.ToStringMeta(L.CheckAny(2)).String())
	if err != nil {
		return rt.raise(err)
	}
	if ok && rt.inv.cmd.Write {
		rt.inv.confirmed = true
	}
	L.Push(lua.LBool(ok))
	return 1
}

// ctx:pick(items, opts) -> item, index (nil when cancelled). Items are
// strings or tables with a label field; opts.title.
func (rt *Runtime) apiPick(L *lua.LState, self *lua.LTable) int {
	items := L.CheckTable(2)
	opts := ensureTable(L, 3, "opts")
	labels := make([]string, 0, items.Len())
	for i := 1; i <= items.Len(); i++ {
		v := items.RawGetInt(i)
		if t, ok := v.(*lua.LTable); ok && luaFieldString(t, "label") != "" {
			labels = append(labels, luaFieldString(t, "label"))
		} else {
			labels = append(labels, L.ToStringMeta(v).String())
		}
	}
	idx, err := rt.env.Host.Pick(rt.title(opts), labels)
	if err != nil {
		return rt.raise(fmt.Errorf("ctx:pick: %w", err))
	}
	if idx < 0 || idx >= len(labels) {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(items.RawGetInt(idx + 1))
	L.Push(lua.LNumber(idx + 1))
	return 2
}

func (rt *Runtime) apiEditorSet(L *lua.LState, self *lua.LTable) int {
	if err := rt.env.Host.EditorSet(L.CheckString(2)); err != nil {
		return rt.raise(fmt.Errorf("ctx:editor_set: %w", err))
	}
	return 0
}

// ctx:editor_open(title, text, opts); opts.object (from ctx:resolve) binds
// the tab to that object.
func (rt *Runtime) apiEditorOpen(L *lua.LState, self *lua.LTable) int {
	title := L.CheckString(2)
	text := L.CheckString(3)
	opts := ensureTable(L, 4, "opts")
	var ref *ObjectRef
	if opts != nil {
		if o, ok := opts.RawGetString("object").(*lua.LTable); ok {
			p, ok := objectPath(o)
			if !ok {
				L.ArgError(4, "opts.object must be an object from ctx:resolve")
			}
			ref = &ObjectRef{Path: p, Kind: meta.ObjectKind(luaFieldString(o, "kind"))}
		}
	}
	if err := rt.env.Host.EditorOpen(title, text, ref); err != nil {
		return rt.raise(fmt.Errorf("ctx:editor_open: %w", err))
	}
	return 0
}

func (rt *Runtime) apiAbout(L *lua.LState, self *lua.LTable) int {
	if err := rt.env.Host.About(); err != nil {
		return rt.raise(fmt.Errorf("ctx:about: %w", err))
	}
	return 0
}
