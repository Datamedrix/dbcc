package meta

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLoader struct {
	calls  sync.Map // key string -> *int64
	delay  time.Duration
	failOn string
	objs   map[string][]Object // "db" or "db.schema"
}

func (f *fakeLoader) count(k string) int64 {
	v, _ := f.calls.LoadOrStore(k, new(int64))
	return atomic.LoadInt64(v.(*int64))
}

func (f *fakeLoader) hit(k string) error {
	v, _ := f.calls.LoadOrStore(k, new(int64))
	atomic.AddInt64(v.(*int64), 1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if k == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeLoader) Databases(ctx context.Context) ([]Object, error) {
	if err := f.hit("dbs"); err != nil {
		return nil, err
	}
	return []Object{{Name: "shop", Kind: KindDatabase}, {Name: "hr", Kind: KindDatabase}}, nil
}

func (f *fakeLoader) Schemas(ctx context.Context, db string) ([]Object, error) {
	if err := f.hit("schemas:" + db); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakeLoader) Objects(ctx context.Context, at Path) ([]Object, error) {
	k := "objs:" + at.Database + "." + at.Schema
	if err := f.hit(k); err != nil {
		return nil, err
	}
	return f.objs[at.Database], nil
}

func (f *fakeLoader) Table(ctx context.Context, obj Path) (*Table, error) {
	k := "table:" + obj.Database + "." + obj.Object
	if err := f.hit(k); err != nil {
		return nil, err
	}
	if obj.Object == "missing" {
		return nil, fmt.Errorf("%w: missing", ErrNotFound)
	}
	return &Table{Path: obj, Kind: KindTable, Columns: []Column{{Name: "id"}}}, nil
}

func newTestCache(f *fakeLoader) *Cache {
	return NewCache(func(conn string) (Loader, bool) {
		if conn == "dev" {
			return f, true
		}
		return nil, false
	})
}

func TestCacheLazyAndCached(t *testing.T) {
	f := &fakeLoader{objs: map[string][]Object{"shop": {{Name: "customers", Kind: KindTable}, {Name: "Orders", Kind: KindTable}}}}
	c := newTestCache(f)
	ctx := context.Background()
	if f.count("objs:shop.") != 0 {
		t.Fatal("loaded before use")
	}
	for i := 0; i < 3; i++ {
		objs, err := c.Objects(ctx, Path{Conn: "dev", Database: "shop", Object: "ignored"})
		if err != nil || len(objs) != 2 {
			t.Fatalf("objects: %v %v", objs, err)
		}
	}
	if n := f.count("objs:shop."); n != 1 {
		t.Fatalf("objects loaded %d times", n)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.Table(ctx, Path{Conn: "dev", Database: "shop", Object: "customers"}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Databases(ctx, "dev"); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("table:shop.customers") != 1 || f.count("dbs") != 1 {
		t.Fatal("table or databases loaded more than once")
	}
	if o, ok := c.FindObject(ctx, Path{Conn: "dev", Database: "shop"}, "orders"); !ok || o.Name != "Orders" {
		t.Fatalf("FindObject case-insensitive: %v %v", o, ok)
	}
	if _, ok := c.FindObject(ctx, Path{Conn: "dev", Database: "shop"}, "nope"); ok {
		t.Fatal("FindObject found a missing object")
	}
}

func TestCacheNoLoader(t *testing.T) {
	c := newTestCache(&fakeLoader{})
	if _, err := c.Databases(context.Background(), "prod"); !errors.Is(err, ErrNoLoader) {
		t.Fatalf("want ErrNoLoader, got %v", err)
	}
	if _, err := NewCache(nil).Databases(context.Background(), "x"); !errors.Is(err, ErrNoLoader) {
		t.Fatalf("nil LoaderFunc: %v", err)
	}
}

func TestCacheErrorsNotCached(t *testing.T) {
	f := &fakeLoader{failOn: "dbs"}
	c := newTestCache(f)
	ctx := context.Background()
	if _, err := c.Databases(ctx, "dev"); err == nil {
		t.Fatal("want error")
	}
	f.failOn = ""
	if dbs, err := c.Databases(ctx, "dev"); err != nil || len(dbs) != 2 {
		t.Fatalf("retry: %v %v", dbs, err)
	}
	if f.count("dbs") != 2 {
		t.Fatalf("calls %d", f.count("dbs"))
	}
	if _, err := c.Table(ctx, Path{Conn: "dev", Database: "shop", Object: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not found: %v", err)
	}
}

func TestCacheInvalidate(t *testing.T) {
	f := &fakeLoader{}
	c := newTestCache(f)
	ctx := context.Background()
	shop := Path{Conn: "dev", Database: "shop"}
	hr := Path{Conn: "dev", Database: "hr"}
	load := func() {
		c.Databases(ctx, "dev")
		c.Objects(ctx, shop)
		c.Objects(ctx, hr)
		c.Table(ctx, Path{Conn: "dev", Database: "shop", Object: "a"})
		c.Table(ctx, Path{Conn: "dev", Database: "shop", Object: "b"})
		c.Table(ctx, Path{Conn: "dev", Database: "hr", Object: "a"})
	}
	snapshot := func() map[string]int64 {
		out := map[string]int64{}
		for _, k := range []string{"dbs", "objs:shop.", "objs:hr.", "table:shop.a", "table:shop.b", "table:hr.a"} {
			out[k] = f.count(k)
		}
		return out
	}
	load()
	base := snapshot()

	// Object level: table a of shop and the shop object list only.
	c.Invalidate(Path{Conn: "dev", Database: "shop", Object: "A"})
	load()
	s := snapshot()
	want := map[string]int64{"objs:shop.": 1, "table:shop.a": 1}
	for k, v := range s {
		if v-base[k] != want[k] {
			t.Errorf("object invalidation: %s reloaded %d times, want %d", k, v-base[k], want[k])
		}
	}

	// Database level: everything of shop plus the database list.
	base = s
	c.Invalidate(shop)
	load()
	s = snapshot()
	want = map[string]int64{"dbs": 1, "objs:shop.": 1, "table:shop.a": 1, "table:shop.b": 1}
	for k, v := range s {
		if v-base[k] != want[k] {
			t.Errorf("database invalidation: %s reloaded %d times, want %d", k, v-base[k], want[k])
		}
	}

	// Connection level.
	base = s
	c.InvalidateAll("dev")
	load()
	s = snapshot()
	for k, v := range s {
		if v-base[k] != 1 {
			t.Errorf("InvalidateAll: %s reloaded %d times, want 1", k, v-base[k])
		}
	}

	// Other connections are unaffected; Clear drops everything.
	base = s
	c.InvalidateAll("prod")
	load()
	for k, v := range snapshot() {
		if v != base[k] {
			t.Errorf("InvalidateAll(prod) reloaded %s", k)
		}
	}
	c.Clear()
	load()
	for k, v := range snapshot() {
		if v-base[k] != 1 {
			t.Errorf("Clear: %s reloaded %d times", k, v-base[k])
		}
	}
}

func TestCacheSchemaInvalidate(t *testing.T) {
	f := &fakeLoader{}
	c := newTestCache(f)
	ctx := context.Background()
	pub := Path{Conn: "dev", Database: "app", Schema: "public"}
	c.Objects(ctx, pub)
	c.Objects(ctx, Path{Conn: "dev", Database: "app", Schema: "sales"})
	c.Schemas(ctx, "dev", "app")
	c.Invalidate(pub)
	c.Objects(ctx, pub)
	c.Objects(ctx, Path{Conn: "dev", Database: "app", Schema: "sales"})
	c.Schemas(ctx, "dev", "app")
	if f.count("objs:app.public") != 2 || f.count("objs:app.sales") != 1 || f.count("schemas:app") != 1 {
		t.Fatalf("schema invalidation: public %d sales %d schemas %d",
			f.count("objs:app.public"), f.count("objs:app.sales"), f.count("schemas:app"))
	}
}

func TestCacheConcurrentSingleLoad(t *testing.T) {
	f := &fakeLoader{delay: 20 * time.Millisecond, objs: map[string][]Object{"shop": {{Name: "t"}}}}
	c := newTestCache(f)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if objs, err := c.Objects(ctx, Path{Conn: "dev", Database: "shop"}); err != nil || len(objs) != 1 {
				t.Errorf("objects: %v %v", objs, err)
			}
			if _, err := c.Table(ctx, Path{Conn: "dev", Database: "shop", Object: fmt.Sprintf("t%d", i%5)}); err != nil {
				t.Error(err)
			}
			if i%10 == 0 {
				c.Invalidate(Path{Conn: "dev", Database: "hr"})
			}
		}(i)
	}
	wg.Wait()
	if n := f.count("objs:shop."); n != 1 {
		t.Fatalf("concurrent requests loaded %d times", n)
	}
	for i := 0; i < 5; i++ {
		if n := f.count(fmt.Sprintf("table:shop.t%d", i)); n != 1 {
			t.Fatalf("table t%d loaded %d times", i, n)
		}
	}
}

func TestCacheConcurrentInvalidateRace(t *testing.T) {
	f := &fakeLoader{delay: time.Millisecond}
	c := newTestCache(f)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.Table(ctx, Path{Conn: "dev", Database: "shop", Object: "x"})
				c.Databases(ctx, "dev")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.Invalidate(Path{Conn: "dev", Database: "shop", Object: "x"})
				c.InvalidateAll("dev")
			}
		}()
	}
	wg.Wait()
}

func TestCacheWaiterContextCancel(t *testing.T) {
	f := &fakeLoader{delay: 200 * time.Millisecond}
	c := newTestCache(f)
	go c.Databases(context.Background(), "dev")
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Databases(ctx, "dev"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter: %v", err)
	}
}

func TestIsDDLKeyword(t *testing.T) {
	for _, k := range []string{"CREATE", "alter", "DROP", "RENAME", "TRUNCATE", "COMMENT"} {
		if !IsDDLKeyword(k) {
			t.Errorf("%s should be DDL", k)
		}
	}
	for _, k := range []string{"SELECT", "INSERT", "UPDATE", "CALL", ""} {
		if IsDDLKeyword(k) {
			t.Errorf("%s should not be DDL", k)
		}
	}
}
