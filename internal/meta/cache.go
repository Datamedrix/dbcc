package meta

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Loader loads metadata of one connection. The cache calls it lazily on
// first use; implementations typically query the auxiliary connection of a
// session through the dialect (see internal/ui/complete.SessionLoader).
// Loads may run concurrently.
type Loader interface {
	// Databases lists the databases of the connection.
	Databases(ctx context.Context) ([]Object, error)
	// Schemas lists the schemas of database db (nil on dialects without a
	// schema level).
	Schemas(ctx context.Context, db string) ([]Object, error)
	// Objects lists the objects of at (Conn, Database and, on dialects
	// with schemas, Schema; Object is ignored).
	Objects(ctx context.Context, at Path) ([]Object, error)
	// Table describes the table or view obj. It wraps ErrNotFound when the
	// object does not exist.
	Table(ctx context.Context, obj Path) (*Table, error)
}

// LoaderFunc resolves the loader of a connection (profile name). It
// returns false when the connection is not open; the cache never opens
// connections itself.
type LoaderFunc func(conn string) (Loader, bool)

// ErrNoLoader is returned when the connection of a path has no loader
// (not connected).
var ErrNoLoader = errors.New("connection not open")

// Cache is the lazy metadata cache per connection, database and schema
// (spec 6.2). Results are cached until invalidated; errors are not cached.
// Concurrent requests for the same key share one load. Cache is safe for
// concurrent use. The returned slices and tables are shared: callers must
// not modify them.
type Cache struct {
	loaders LoaderFunc

	mu      sync.Mutex
	entries map[cacheKey]*entry
}

type keyKind uint8

const (
	keyDatabases keyKind = iota
	keySchemas
	keyObjects
	keyTable
)

type cacheKey struct {
	kind                   keyKind
	conn, db, schema, name string
}

type entry struct {
	done    chan struct{}
	objects []Object
	table   *Table
	err     error
}

// NewCache returns an empty cache that obtains loaders from loaders.
func NewCache(loaders LoaderFunc) *Cache {
	return &Cache{loaders: loaders, entries: map[cacheKey]*entry{}}
}

// Databases returns the databases of connection conn.
func (c *Cache) Databases(ctx context.Context, conn string) ([]Object, error) {
	e, err := c.get(ctx, cacheKey{kind: keyDatabases, conn: conn}, func(l Loader) (*entry, error) {
		o, err := l.Databases(ctx)
		return &entry{objects: o}, err
	})
	if err != nil {
		return nil, err
	}
	return e.objects, nil
}

// Schemas returns the schemas of database db on connection conn.
func (c *Cache) Schemas(ctx context.Context, conn, db string) ([]Object, error) {
	e, err := c.get(ctx, cacheKey{kind: keySchemas, conn: conn, db: db}, func(l Loader) (*entry, error) {
		o, err := l.Schemas(ctx, db)
		return &entry{objects: o}, err
	})
	if err != nil {
		return nil, err
	}
	return e.objects, nil
}

// Objects returns the objects of the database (and schema) of at.
// at.Object is ignored.
func (c *Cache) Objects(ctx context.Context, at Path) ([]Object, error) {
	at.Object = ""
	e, err := c.get(ctx, cacheKey{kind: keyObjects, conn: at.Conn, db: at.Database, schema: at.Schema}, func(l Loader) (*entry, error) {
		o, err := l.Objects(ctx, at)
		return &entry{objects: o}, err
	})
	if err != nil {
		return nil, err
	}
	return e.objects, nil
}

// Table returns the metadata of the table or view obj.
func (c *Cache) Table(ctx context.Context, obj Path) (*Table, error) {
	e, err := c.get(ctx, cacheKey{kind: keyTable, conn: obj.Conn, db: obj.Database, schema: obj.Schema, name: obj.Object}, func(l Loader) (*entry, error) {
		t, err := l.Table(ctx, obj)
		return &entry{table: t}, err
	})
	if err != nil {
		return nil, err
	}
	return e.table, nil
}

// FindObject looks up name among the objects of at: an exact match wins,
// then a case-insensitive one. It returns false if there is none.
func (c *Cache) FindObject(ctx context.Context, at Path, name string) (Object, bool) {
	objs, err := c.Objects(ctx, at)
	if err != nil {
		return Object{}, false
	}
	for _, o := range objs {
		if o.Name == name {
			return o, true
		}
	}
	for _, o := range objs {
		if strings.EqualFold(o.Name, name) {
			return o, true
		}
	}
	return Object{}, false
}

func (c *Cache) get(ctx context.Context, k cacheKey, load func(Loader) (*entry, error)) (*entry, error) {
	c.mu.Lock()
	if e, ok := c.entries[k]; ok {
		c.mu.Unlock()
		select {
		case <-e.done:
			if e.err != nil {
				return nil, e.err
			}
			return e, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var l Loader
	ok := false
	if c.loaders != nil {
		l, ok = c.loaders(k.conn)
	}
	if !ok || l == nil {
		c.mu.Unlock()
		return nil, ErrNoLoader
	}
	e := &entry{done: make(chan struct{})}
	c.entries[k] = e
	c.mu.Unlock()

	res, err := load(l)
	if res != nil {
		e.objects, e.table = res.objects, res.table
	}
	e.err = err
	if err != nil {
		// Errors are not cached: the next request loads again.
		c.mu.Lock()
		if c.entries[k] == e {
			delete(c.entries, k)
		}
		c.mu.Unlock()
	}
	close(e.done)
	return e, err
}

// Invalidate drops cached metadata below p:
//
//   - p.Object set: that table's description and the object list of its
//     database/schema;
//   - p.Schema set: everything of that schema;
//   - p.Database set: everything of that database, including its schema
//     list, plus the database list of the connection;
//   - only p.Conn: everything of the connection (same as InvalidateAll).
func (c *Cache) Invalidate(p Path) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.conn != p.Conn {
			continue
		}
		drop := false
		switch {
		case p.Database == "":
			drop = true
		case p.Schema == "" && p.Object == "":
			drop = k.kind == keyDatabases || k.db == p.Database
		case p.Object == "":
			drop = k.db == p.Database && k.schema == p.Schema && k.kind != keySchemas
		default:
			drop = k.db == p.Database && k.schema == p.Schema &&
				(k.kind == keyObjects || (k.kind == keyTable && strings.EqualFold(k.name, p.Object)))
		}
		if drop {
			delete(c.entries, k)
		}
	}
}

// InvalidateAll drops all cached metadata of connection conn (refresh,
// reconnect, DDL).
func (c *Cache) InvalidateAll(conn string) { c.Invalidate(Path{Conn: conn}) }

// Clear drops everything.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.entries = map[cacheKey]*entry{}
	c.mu.Unlock()
}

// ddlFirstKeywords are first keywords of statements that change the
// catalog (used by IsDDL).
var ddlFirstKeywords = map[string]bool{
	"CREATE": true, "ALTER": true, "DROP": true, "RENAME": true, "TRUNCATE": true,
	"COMMENT": true, // PostgreSQL COMMENT ON
	"IMPORT":  true, // MySQL IMPORT TABLE
}

// IsDDLKeyword reports whether a statement whose first keyword is kw
// (upper case) changes the catalog: CREATE, ALTER, DROP, RENAME, TRUNCATE,
// COMMENT, IMPORT. CREATE TEMPORARY … counts too (it shadows names).
func IsDDLKeyword(kw string) bool { return ddlFirstKeywords[strings.ToUpper(kw)] }
