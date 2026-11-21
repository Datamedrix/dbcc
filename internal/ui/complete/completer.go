package complete

import (
	"context"
	"sort"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/session"
	"dbcc/internal/sqltext"
)

// Item kinds besides the meta.ObjectKind values ("table", "view",
// "procedure", "database", "schema", …).
const (
	KindColumn   = "column"
	KindKeyword  = "keyword"
	KindFunction = "function"
	KindCommand  = "command"
	KindPath     = "path"
	KindFile     = "file"
	KindProfile  = "profile"
)

// Item is one completion candidate.
type Item struct {
	Text   string // inserted text, quoted if necessary
	Kind   string // KindColumn, KindKeyword, meta.ObjectKind values, …
	Detail string // shown dimmed: data type, table, comment
}

// Result is the outcome of Completer.Complete.
type Result struct {
	Items []Item
	// Start is the byte offset in the completed text where the
	// replacement begins (including an opening quote the user typed);
	// accepting an item replaces text[Start:cursor] with Item.Text.
	Start  int
	Prefix string // the typed prefix (unquoted)
	Kind   sqltext.CompletionKind
}

// Completer produces candidates from the metadata cache for the current
// path (spec 9.2). Build one per completion request with ForWorkspace, or
// fill the fields directly (tests).
type Completer struct {
	Cache   *meta.Cache
	Dialect dialect.Dialect // dialect of Path.Conn; nil at "/"
	Path    meta.Path       // current path (Object ignored)

	// DialectOf returns the dialect of another open connection (command
	// line paths across connections); nil means only Path.Conn is known.
	DialectOf func(conn string) dialect.Dialect
	// Profiles returns the configured profile names (command line).
	Profiles func() []string
	// Dir is the working directory for file arguments ("" = ".").
	Dir string
}

// New returns a Completer for path on dialect d.
func New(cache *meta.Cache, d dialect.Dialect, path meta.Path) *Completer {
	return &Completer{Cache: cache, Dialect: d, Path: path}
}

// ForWorkspace returns a Completer for the current path of ws.
func ForWorkspace(cache *meta.Cache, ws *session.Workspace) *Completer {
	c := &Completer{Cache: cache, Path: ws.Path()}
	if s := ws.Current(); s != nil {
		c.Dialect = s.Dialect
		if p := s.Path(); c.Path.Conn == p.Conn && c.Path.Schema == "" {
			c.Path.Schema = p.Schema
		}
	}
	c.DialectOf = func(conn string) dialect.Dialect {
		if s, ok := ws.Sessions()[conn]; ok && s != nil {
			return s.Dialect
		}
		return nil
	}
	c.Profiles = func() []string {
		if ws.Config == nil {
			return nil
		}
		names := make([]string, 0, len(ws.Config.Profiles))
		for n := range ws.Config.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		return names
	}
	return c
}

// Candidates returns the candidates for the statement text at byte offset
// cursor, filtered by the typed prefix. text may be the whole editor
// buffer; only the statement around the cursor is used.
func (c *Completer) Candidates(ctx context.Context, text string, cursor int) []Item {
	return c.Complete(ctx, text, cursor).Items
}

// Complete is Candidates plus the replacement range. It may query the
// database on a cold cache; call it off the UI goroutine or with a short
// timeout in ctx.
func (c *Completer) Complete(ctx context.Context, text string, cursor int) Result {
	if c.Dialect == nil {
		return Result{Start: cursor}
	}
	cc := sqltext.CompletionAt(c.Dialect.Tokenizer(), text, cursor)
	r := Result{Start: cc.Start, Prefix: cc.Prefix, Kind: cc.Kind}
	var b builder
	b.c, b.prefix, b.quoted = c, cc.Prefix, cc.Quote != 0
	switch cc.Kind {
	case sqltext.CompleteNone:
		return r
	case sqltext.CompleteTables:
		b.objects(c.objects(ctx, c.Path), isRelation)
		if c.hasSchemas() {
			b.objects(c.schemas(ctx, c.Path.Database), nil)
		}
		b.objects(c.databases(ctx, c.Path.Conn), nil)
	case sqltext.CompleteObjects:
		b.qualifiedObjects(ctx, cc.Qualifier, isRelation)
	case sqltext.CompleteProcedures:
		if len(cc.Qualifier) == 0 {
			b.objects(c.objects(ctx, c.Path), isProcedure)
		} else {
			b.qualifiedObjects(ctx, cc.Qualifier, isProcedure)
		}
	case sqltext.CompleteColumns:
		if cc.Table == nil || len(cc.Table.Parts) == 0 {
			break // derived table: columns unknown
		}
		t := c.table(ctx, cc.Table.Parts)
		if t != nil {
			b.columns(t, "")
		} else if !cc.TableFound {
			// "x." where x is no table of the statement and no table of
			// the current path: maybe a database (MySQL) or schema.
			b.qualifiedObjects(ctx, cc.Qualifier, nil)
		}
	case sqltext.CompleteAny:
		seen := map[string]bool{}
		for _, ref := range cc.Tables {
			if len(ref.Parts) == 0 {
				continue
			}
			key := strings.ToLower(strings.Join(ref.Parts, "."))
			if seen[key] {
				continue
			}
			seen[key] = true
			if t := c.table(ctx, ref.Parts); t != nil {
				b.columns(t, ref.Name())
			}
		}
		if !b.quoted {
			b.words(c.Dialect.Keywords(), KindKeyword)
			b.words(c.Dialect.Functions(), KindFunction)
		}
	}
	r.Items = b.items
	return r
}

// builder collects filtered, de-duplicated items.
type builder struct {
	c      *Completer
	prefix string
	quoted bool
	items  []Item
	seen   map[string]bool
}

func (b *builder) add(name, kind, detail string) {
	if !hasPrefixFold(name, b.prefix) {
		return
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	key := kind + "\x00" + name
	if kind == KindColumn {
		key = kind + "\x00" + strings.ToLower(name)
	}
	if b.seen[key] {
		return
	}
	b.seen[key] = true
	text := name
	switch {
	case kind == KindKeyword || kind == KindFunction:
	case b.quoted:
		text = b.c.Dialect.QuoteIdent(name)
	default:
		text = QuoteIfNeeded(b.c.Dialect, name)
	}
	b.items = append(b.items, Item{Text: text, Kind: kind, Detail: detail})
}

func (b *builder) objects(objs []meta.Object, keep func(meta.ObjectKind) bool) {
	for _, o := range objs {
		if keep == nil || keep(o.Kind) {
			b.add(o.Name, string(o.Kind), o.Comment)
		}
	}
}

func (b *builder) columns(t *meta.Table, table string) {
	for _, col := range t.Columns {
		d := col.DataType
		if table != "" {
			d += "  " + table
		}
		b.add(col.Name, KindColumn, d)
	}
}

// words adds keywords or functions, in the letter case of the prefix
// (lower case when the prefix is lower case).
func (b *builder) words(ws []string, kind string) {
	lower := b.prefix != "" && strings.ToLower(b.prefix) == b.prefix
	for _, w := range ws {
		if lower {
			w = strings.ToLower(w)
		}
		b.add(w, kind, "")
	}
}

// qualifiedObjects adds the objects of the database or schema a qualifier
// names: MySQL "db." → objects of db; PostgreSQL "schema." → objects of
// the schema in the current database, or, if no such schema, the schemas
// of database "db."; "db.schema." → objects of that schema.
func (b *builder) qualifiedObjects(ctx context.Context, q []string, keep func(meta.ObjectKind) bool) {
	c := b.c
	p := meta.Path{Conn: c.Path.Conn}
	if !c.hasSchemas() {
		if len(q) != 1 {
			return
		}
		p.Database = c.canonical(c.databases(ctx, p.Conn), q[0])
		b.objects(c.objects(ctx, p), keep)
		return
	}
	switch len(q) {
	case 1:
		p.Database = c.Path.Database
		p.Schema = c.canonical(c.schemas(ctx, p.Database), q[0])
		objs := c.objects(ctx, p)
		if len(objs) == 0 {
			if db := c.canonical(c.databases(ctx, p.Conn), q[0]); c.hasName(c.databases(ctx, p.Conn), db) {
				b.objects(c.schemas(ctx, db), nil)
				return
			}
		}
		b.objects(objs, keep)
	case 2:
		p.Database = c.canonical(c.databases(ctx, p.Conn), q[0])
		p.Schema = c.canonical(c.schemas(ctx, p.Database), q[1])
		b.objects(c.objects(ctx, p), keep)
	}
}

func (c *Completer) hasSchemas() bool { return c.Dialect != nil && c.Dialect.HasSchemas() }

func (c *Completer) databases(ctx context.Context, conn string) []meta.Object {
	if c.Cache == nil || conn == "" {
		return nil
	}
	o, _ := c.Cache.Databases(ctx, conn)
	return o
}

func (c *Completer) schemas(ctx context.Context, db string) []meta.Object {
	if c.Cache == nil || db == "" || !c.hasSchemas() {
		return nil
	}
	o, _ := c.Cache.Schemas(ctx, c.Path.Conn, db)
	return o
}

func (c *Completer) objects(ctx context.Context, at meta.Path) []meta.Object {
	if c.Cache == nil || at.Conn == "" || at.Database == "" {
		return nil
	}
	if c.hasSchemas() && at.Schema == "" {
		return nil
	}
	o, _ := c.Cache.Objects(ctx, at)
	return o
}

// table resolves the qualified name parts of a statement table against
// the current path and returns its cached metadata, or nil.
func (c *Completer) table(ctx context.Context, parts []string) *meta.Table {
	if c.Cache == nil {
		return nil
	}
	p := meta.Path{Conn: c.Path.Conn, Database: c.Path.Database, Schema: c.Path.Schema}
	n := len(parts)
	if c.hasSchemas() {
		switch n {
		case 1:
		case 2:
			p.Schema = c.canonical(c.schemas(ctx, p.Database), parts[0])
		case 3:
			p.Database = c.canonical(c.databases(ctx, p.Conn), parts[0])
			p.Schema = c.canonical(c.schemas(ctx, p.Database), parts[1])
		default:
			return nil
		}
	} else {
		switch n {
		case 1:
			p.Schema = ""
		case 2:
			p.Database = c.canonical(c.databases(ctx, p.Conn), parts[0])
			p.Schema = ""
		default:
			return nil
		}
	}
	if p.Database == "" {
		return nil
	}
	o, ok := c.Cache.FindObject(ctx, p, parts[n-1])
	if !ok || !isRelation(o.Kind) {
		return nil
	}
	p.Object = o.Name
	t, err := c.Cache.Table(ctx, p)
	if err != nil {
		return nil
	}
	return t
}

// canonical returns the name in objs equal to name (exact, then
// case-insensitive), or name itself.
func (c *Completer) canonical(objs []meta.Object, name string) string {
	for _, o := range objs {
		if o.Name == name {
			return o.Name
		}
	}
	for _, o := range objs {
		if strings.EqualFold(o.Name, name) {
			return o.Name
		}
	}
	return name
}

func (c *Completer) hasName(objs []meta.Object, name string) bool {
	for _, o := range objs {
		if o.Name == name {
			return true
		}
	}
	return false
}

func isRelation(k meta.ObjectKind) bool {
	return k == meta.KindTable || k == meta.KindView || k == meta.KindMaterializedView
}

func isProcedure(k meta.ObjectKind) bool { return k == meta.KindProcedure }

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
