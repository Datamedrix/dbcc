//go:build integration

package form

// Integration tests of the statements the form generates against real
// MySQL and MariaDB servers:
//
//	DBCC_IT_MYSQL=127.0.0.1:13306 DBCC_IT_MARIADB=127.0.0.1:13307 DBCC_IT_PW=<root password>
//	go test -tags integration -count=1 ./internal/ui/form/

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	driver "github.com/go-sql-driver/mysql"

	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

const itDB = "dbcc_it_form"

var itSchema = []string{
	`CREATE TABLE parent (
	   a INT NOT NULL, b VARCHAR(20) NOT NULL, label VARCHAR(40) NULL,
	   PRIMARY KEY (a, b))`,
	`CREATE TABLE items (
	   id INT UNSIGNED NOT NULL AUTO_INCREMENT,
	   code VARCHAR(20) NOT NULL,
	   region CHAR(2) NOT NULL DEFAULT 'EU',
	   note VARCHAR(40) NULL DEFAULT 'dflt',
	   qty INT NOT NULL DEFAULT 1,
	   small TINYINT(1) NOT NULL DEFAULT 0,
	   flag BIT(1) NULL,
	   status ENUM('new','active') NOT NULL DEFAULT 'new',
	   tags SET('a','b','c') NULL,
	   total INT GENERATED ALWAYS AS (qty * 2) STORED,
	   parent_a INT NULL, parent_b VARCHAR(20) NULL,
	   PRIMARY KEY (id),
	   UNIQUE KEY uq_code_region (code, region),
	   CONSTRAINT fk_items_parent FOREIGN KEY (parent_a, parent_b) REFERENCES parent (a, b),
	   CONSTRAINT chk_qty CHECK (qty >= 0))`,
	`INSERT INTO parent VALUES (1, 'a', 'one'), (2, 'b', 'two')`,
}

type itServer struct{ name, addr string }

func itServers(t *testing.T) []itServer {
	var out []itServer
	for _, s := range []itServer{{"mysql", os.Getenv("DBCC_IT_MYSQL")}, {"mariadb", os.Getenv("DBCC_IT_MARIADB")}} {
		if s.addr != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Skip("no DBCC_IT_* servers configured")
	}
	return out
}

func openDB(t *testing.T, s itServer, db string) *sql.DB {
	t.Helper()
	cfg := driver.NewConfig()
	cfg.User, cfg.Passwd = "root", os.Getenv("DBCC_IT_PW")
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", s.addr, db
	cfg.Timeout = 10 * time.Second
	conn, err := driver.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d := sql.OpenDB(conn)
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	return d
}

// sqlBackend is a Backend directly over a *sql.DB.
type sqlBackend struct {
	db *sql.DB
	d  dialect.Dialect
}

func (b *sqlBackend) Exec(q string, args ...any) (int64, error) {
	r, err := b.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

func (b *sqlBackend) query(q string, args ...any) (*exec.Result, error) {
	rows, err := b.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, _ := rows.Columns()
	var cols []exec.ColumnInfo
	for _, n := range names {
		cols = append(cols, exec.ColumnInfo{Name: n})
	}
	var out []exec.Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(exec.Row, len(cols))
		for i, v := range vals {
			if bs, ok := v.([]byte); ok {
				row[i] = string(bs)
			} else if v != nil {
				row[i] = exec.CellText(v)
			}
		}
		out = append(out, row)
	}
	return exec.NewResult("", cols, out), rows.Err()
}

func (b *sqlBackend) Query(q string, args ...any) (*exec.Result, error) { return b.query(q, args...) }
func (b *sqlBackend) Grid(q string, args ...any) (*exec.Result, error)  { return b.query(q, args...) }
func (b *sqlBackend) Dialect() dialect.Dialect                          { return b.d }
func (b *sqlBackend) Describe(p meta.Path) (*meta.Table, error) {
	return b.d.DescribeTable(context.Background(), b.db, p)
}

func TestIntegrationStatements(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			admin := openDB(t, s, "")
			for _, q := range []string{"DROP DATABASE IF EXISTS " + itDB, "CREATE DATABASE " + itDB} {
				if _, err := admin.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { admin.Exec("DROP DATABASE IF EXISTS " + itDB) })
			db := openDB(t, s, itDB)
			for _, q := range itSchema {
				if _, err := db.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			dd, err := (&mysql.MySQL{}).Detect(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			d := dd.(*mysql.MySQL)
			be := &sqlBackend{db: db, d: d}
			items, err := be.Describe(meta.Path{Database: itDB, Object: "items"})
			if err != nil {
				t.Fatal(err)
			}
			run := func(q string, args []any, err error) error {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				_, xerr := db.Exec(q, args...)
				return xerr
			}
			ins := func(fs ...FieldState) error {
				t.Helper()
				q, args, err := BuildInsert(d, items, fs)
				return run(q, args, err)
			}
			v := func(col, val string) FieldState { return FieldState{Column: col, State: Value, Value: val} }
			n := func(col string) FieldState { return FieldState{Column: col, State: Null} }

			// Touched fields only; NULL, empty string, booleans, SET, ENUM.
			if err := ins(v("code", "c1"), v("small", "1"), v("flag", "1"), v("tags", "a,c"), v("status", "active"),
				v("parent_a", "1"), v("parent_b", "a"), n("note")); err != nil {
				t.Fatal(err)
			}
			if err := ins(v("code", ""), v("note", "")); err != nil { // empty strings are values
				t.Fatal(err)
			}
			res, err := be.query("SELECT code, region, note, qty, small, flag + 0, status, tags, total, parent_a, parent_b FROM items ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			want := []exec.Row{
				{"c1", "EU", nil, "1", "1", "1", "active", "a,c", "2", "1", "a"},
				{"", "EU", "", "1", "0", nil, "new", nil, "2", nil, nil},
			}
			if got := res.Rows(); !reflect.DeepEqual(got, want) {
				t.Fatalf("rows\n%v\nwant\n%v", got, want)
			}

			// Mapped server errors (check constraints differ: MySQL 3819, MariaDB 4025).
			chk := uint16(3819)
			if d.MariaDB {
				chk = 4025
			}
			for _, c := range []struct {
				name string
				err  error
				num  uint16
				cols []string
			}{
				{"not null", ins(n("code")), 1048, []string{"code"}},
				{"all defaults, no default for code", ins(), 1364, []string{"code"}},
				{"duplicate", ins(v("code", "c1")), 1062, []string{"code", "region"}},
				{"fk", ins(v("code", "c9"), v("parent_a", "9"), v("parent_b", "z")), 1452, []string{"parent_a", "parent_b"}},
				{"check", ins(v("code", "c8"), v("qty", "-1")), chk, []string{"qty"}},
			} {
				var me *driver.MySQLError
				if c.err == nil || !errors.As(c.err, &me) || me.Number != c.num {
					t.Errorf("%s: %v, want error %d", c.name, c.err, c.num)
					continue
				}
				fe := d.MapError(c.err, items)
				if fe == nil {
					t.Errorf("%s: not mapped", c.name)
					continue
				}
				cols := items.ConstraintColumns(fe.Constraint)
				if len(cols) == 0 {
					cols = []string{fe.Column}
				}
				if !reflect.DeepEqual(cols, c.cols) {
					t.Errorf("%s: %+v → %v, want %v", c.name, fe, cols, c.cols)
				}
			}

			// UPDATE on the original key, only changed fields.
			q, args, err := BuildUpdate(d, items, []FieldState{
				{Column: "code", State: Value, Value: "c2", HasOrig: true, Orig: "c1"},
				{Column: "qty", State: Value, Value: "1", HasOrig: true, Orig: "1"},
				{Column: "note", State: Value, Value: "x", HasOrig: true, Orig: nil},
				{Column: "flag", State: Value, Value: "0", HasOrig: true, Orig: "\x01"},
			}, []KeyValue{{Column: "code", Value: "c1"}, {Column: "region", Value: "EU"}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(q, "UPDATE `"+itDB+"`.`items` SET `code` = ?, `note` = ?, `flag` = ? WHERE `code` = ? AND `region` = ?") {
				t.Fatalf("update %s", q)
			}
			r, err := db.Exec(q, args...)
			if err != nil {
				t.Fatal(err)
			}
			if a, _ := r.RowsAffected(); a != 1 {
				t.Fatalf("affected %d", a)
			}
			// The same UPDATE again matches no row (key changed).
			r, err = db.Exec(q, args...)
			if a, _ := r.RowsAffected(); err != nil || a != 0 {
				t.Fatalf("second update: %d %v", a, err)
			}
			// Update to a duplicate key.
			q, args, _ = BuildUpdate(d, items, []FieldState{{Column: "code", State: Value, Value: "", HasOrig: true, Orig: "c2"}}, []KeyValue{{Column: "id", Value: "1"}})
			if _, err := db.Exec(q, args...); err == nil {
				t.Fatal("duplicate update must fail")
			} else if fe := d.MapError(err, items); fe == nil || fe.Constraint != "uq_code_region" {
				t.Fatalf("duplicate update: %v → %+v", err, fe)
			}

			// End to end through the form: lookup fills the composite FK,
			// an FK violation marks both FK fields.
			f := New(Options{Table: items, Backend: be})
			f.FocusField("code")
			for _, ch := range "e2e" {
				f.HandleKey(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone))
			}
			f.FocusField("parent_a")
			f.HandleKey(tcell.NewEventKey(tcell.KeyF4, 0, tcell.ModNone))
			if f.look == nil {
				t.Fatalf("lookup: %s", f.Message())
			}
			for _, ch := range "two" {
				f.HandleKey(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone))
			}
			if src := f.look.grid.Source(); src == nil || src.Len() != 1 {
				t.Fatalf("lookup filter: %v %s", src, f.look.status)
			}
			f.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			if a, b := f.fieldOf("parent_a"), f.fieldOf("parent_b"); a.value != "2" || b.value != "b" || a.desc != "two" {
				t.Fatalf("lookup selection %+v %+v", a, b)
			}
			// Remove the parent so the insert violates the FK.
			if _, err := db.Exec("DELETE FROM parent WHERE a = 2"); err != nil {
				t.Fatal(err)
			}
			f.Save(false)
			if f.Closed() || f.fieldOf("parent_a").err == "" || f.fieldOf("parent_b").err == "" {
				t.Fatalf("fk error not marked: %s", f.Message())
			}
			t.Logf("%s: %s", s.name, f.Message())
		})
	}
}
