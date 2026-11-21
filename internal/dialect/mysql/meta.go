package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

// DescribeTable reads the full metadata of a table or view from
// information_schema (spec 6.2, 8.1). Views get columns, the view
// definition and view options only.
//
// Column defaults are normalised across servers: Default holds the literal
// value (unquoted, unescaped) or, with DefaultExpr set, the expression text;
// nil means no default or DEFAULT NULL. MySQL reports literals bare and
// marks expressions with DEFAULT_GENERATED in EXTRA; MariaDB quotes string
// literals, reports bare numbers and expressions, and uses the text NULL for
// DEFAULT NULL.
func (m *MySQL) DescribeTable(ctx context.Context, c dialect.Conn, obj meta.Path) (*meta.Table, error) {
	db, err := m.dbOf(ctx, c, obj)
	if err != nil {
		return nil, err
	}
	obj.Database = db
	t := &meta.Table{Path: obj, Options: map[string]string{}}

	var (
		tableType                                        string
		engine, coll, rowFormat, createOpts, comment, cs sql.NullString
		rows, autoInc                                    sql.NullInt64
	)
	err = c.QueryRowContext(ctx, `SELECT t.TABLE_TYPE, t.ENGINE, t.TABLE_COLLATION, t.ROW_FORMAT,
			t.TABLE_ROWS, t.AUTO_INCREMENT, t.CREATE_OPTIONS, t.TABLE_COMMENT, co.CHARACTER_SET_NAME
		FROM information_schema.TABLES t
		LEFT JOIN information_schema.COLLATIONS co ON co.COLLATION_NAME = t.TABLE_COLLATION
		WHERE t.TABLE_SCHEMA = ? AND t.TABLE_NAME = ?`, db, obj.Object).
		Scan(&tableType, &engine, &coll, &rowFormat, &rows, &autoInc, &createOpts, &comment, &cs)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", meta.ErrNotFound, m.Qualify(obj))
	}
	if err != nil {
		return nil, err
	}

	if err := m.loadColumns(ctx, c, t); err != nil {
		return nil, err
	}

	if tableType == "VIEW" || tableType == "SYSTEM VIEW" {
		t.Kind = meta.KindView
		return t, m.loadView(ctx, c, t)
	}
	t.Kind = meta.KindTable
	t.Comment = comment.String
	setOpt := func(k string, v sql.NullString) {
		if v.Valid && v.String != "" {
			t.Options[k] = v.String
		}
	}
	setOpt("engine", engine)
	if !cs.Valid && coll.Valid {
		cs = sql.NullString{String: charsetOf(coll.String), Valid: true}
	}
	setOpt("charset", cs)
	setOpt("collation", coll)
	setOpt("row_format", rowFormat)
	setOpt("create_options", createOpts)
	if rows.Valid {
		t.Options["rows"] = strconv.FormatInt(rows.Int64, 10)
	}
	if autoInc.Valid {
		t.Options["auto_increment"] = strconv.FormatInt(autoInc.Int64, 10)
	}

	steps := []func(context.Context, dialect.Conn, *meta.Table) error{
		m.loadKeys, m.loadForeignKeys, m.loadChecks, m.loadIndexes, m.loadTriggers, m.loadPartitions,
	}
	for _, f := range steps {
		if err := f(ctx, c, t); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// dbOf returns the database of obj, defaulting to the session's current one.
func (m *MySQL) dbOf(ctx context.Context, c dialect.Conn, obj meta.Path) (string, error) {
	if obj.Database != "" {
		return obj.Database, nil
	}
	db, err := m.CurrentDatabase(ctx, c)
	if err != nil {
		return "", err
	}
	if db == "" {
		return "", fmt.Errorf("no database selected for %s", m.QuoteIdent(obj.Object))
	}
	return db, nil
}

func (m *MySQL) loadColumns(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT COLUMN_NAME, ORDINAL_POSITION, COLUMN_TYPE, IS_NULLABLE,
			COLUMN_DEFAULT, EXTRA, COALESCE(GENERATION_EXPRESSION, ''), COLLATION_NAME, COLUMN_COMMENT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			col             meta.Column
			nullable, extra string
			def, coll       sql.NullString
			genExpr         string
		)
		if err := rows.Scan(&col.Name, &col.Position, &col.DataType, &nullable, &def, &extra, &genExpr, &coll, &col.Comment); err != nil {
			return err
		}
		col.Nullable = nullable == "YES"
		col.Collation = coll.String
		lx := strings.ToLower(extra)
		col.AutoIncrement = strings.Contains(lx, "auto_increment")
		if genExpr != "" && (strings.Contains(lx, "generated") || strings.Contains(lx, "persistent")) {
			col.Generated = &meta.Generated{
				Expression: m.unescapeExpr(genExpr),
				Stored:     strings.Contains(lx, "stored") || strings.Contains(lx, "persistent"),
			}
		}
		if i := strings.Index(lx, "on update "); i >= 0 {
			col.OnUpdate = strings.TrimSpace(extra[i+len("on update "):])
		}
		if col.Generated == nil {
			col.Default, col.DefaultExpr = m.normalizeDefault(def, strings.Contains(lx, "default_generated"))
		}
		col.EnumValues = enumValues(col.DataType)
		t.Columns = append(t.Columns, col)
	}
	return rows.Err()
}

var numericLiteral = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

// normalizeDefault turns COLUMN_DEFAULT into (value, isExpression).
func (m *MySQL) normalizeDefault(def sql.NullString, defaultGenerated bool) (*string, bool) {
	if !def.Valid {
		return nil, false
	}
	v := def.String
	if !m.MariaDB {
		if defaultGenerated {
			e := m.unescapeExpr(v)
			return &e, true
		}
		return &v, false
	}
	// MariaDB (10.2.7+): string literals are quoted, NULL means DEFAULT NULL,
	// numbers and b'…'/0x… are bare literals, anything else is an expression.
	switch {
	case v == "NULL":
		return nil, false
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		u := unquoteSQL(v)
		return &u, false
	case numericLiteral.MatchString(v),
		strings.HasPrefix(v, "b'"), strings.HasPrefix(v, "x'"), strings.HasPrefix(v, "0x"):
		return &v, false
	}
	return &v, true
}

// unescapeExpr undoes MySQL 8's extra level of backslash escaping in
// expression columns of information_schema (GENERATION_EXPRESSION,
// expression COLUMN_DEFAULT, CHECK_CLAUSE): `\'a\\\'b\'` → `'a\'b'`, which
// is how SHOW CREATE TABLE prints the expression. MariaDB reports these
// expressions unescaped.
func (m *MySQL) unescapeExpr(s string) string {
	if m.MariaDB || !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unquoteSQL decodes a single-quoted SQL string literal (” and backslash
// escapes). Input without surrounding quotes is returned unchanged.
func unquoteSQL(s string) string {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return s
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case ch == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				ch = '\n'
			case 't':
				ch = '\t'
			case 'r':
				ch = '\r'
			case '0':
				ch = 0
			case 'Z':
				ch = 26
			case 'b':
				ch = '\b'
			default:
				ch = s[i]
			}
			b.WriteByte(ch)
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// enumValues parses the values of enum('a','b') and set('a','b') types.
func enumValues(colType string) []string {
	lt := strings.ToLower(colType)
	var rest string
	switch {
	case strings.HasPrefix(lt, "enum("):
		rest = colType[5:]
	case strings.HasPrefix(lt, "set("):
		rest = colType[4:]
	default:
		return nil
	}
	var out []string
	for i := 0; i < len(rest); i++ {
		if rest[i] != '\'' {
			if rest[i] == ')' {
				break
			}
			continue
		}
		var b strings.Builder
		j := i + 1
		for ; j < len(rest); j++ {
			if rest[j] == '\'' {
				if j+1 < len(rest) && rest[j+1] == '\'' {
					b.WriteByte('\'')
					j++
					continue
				}
				break
			}
			if rest[j] == '\\' && j+1 < len(rest) {
				j++
			}
			b.WriteByte(rest[j])
		}
		out = append(out, b.String())
		i = j
	}
	return out
}

// charsetOf derives the character set from a collation name.
func charsetOf(collation string) string {
	if i := strings.IndexByte(collation, '_'); i > 0 {
		return collation[:i]
	}
	return collation
}

func (m *MySQL) loadView(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	q := `SELECT VIEW_DEFINITION, CHECK_OPTION, IS_UPDATABLE, DEFINER, SECURITY_TYPE, '' FROM information_schema.VIEWS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
	if m.MariaDB {
		q = strings.Replace(q, "SECURITY_TYPE, ''", "SECURITY_TYPE, ALGORITHM", 1)
	}
	var def, check, upd, definer, sec, alg sql.NullString
	err := c.QueryRowContext(ctx, q, t.Path.Database, t.Path.Object).Scan(&def, &check, &upd, &definer, &sec, &alg)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	t.ViewDefinition = def.String
	for k, v := range map[string]sql.NullString{"check_option": check, "updatable": upd, "definer": definer, "security_type": sec, "algorithm": alg} {
		if v.Valid && v.String != "" {
			t.Options[k] = v.String
		}
	}
	return nil
}

func (m *MySQL) loadKeys(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE, k.COLUMN_NAME
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.KEY_COLUMN_USAGE k
		  ON k.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND k.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		 AND k.TABLE_SCHEMA = tc.TABLE_SCHEMA AND k.TABLE_NAME = tc.TABLE_NAME
		WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE IN ('PRIMARY KEY', 'UNIQUE')
		ORDER BY tc.CONSTRAINT_TYPE = 'UNIQUE', tc.CONSTRAINT_NAME, k.ORDINAL_POSITION`, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		var colName sql.NullString // NULL for functional key parts
		if err := rows.Scan(&name, &typ, &colName); err != nil {
			return err
		}
		if !colName.Valid {
			continue
		}
		col := colName.String
		if typ == "PRIMARY KEY" {
			if t.PrimaryKey == nil {
				t.PrimaryKey = &meta.Key{Name: name}
			}
			t.PrimaryKey.Columns = append(t.PrimaryKey.Columns, col)
			continue
		}
		if n := len(t.UniqueKeys); n == 0 || t.UniqueKeys[n-1].Name != name {
			t.UniqueKeys = append(t.UniqueKeys, meta.Key{Name: name})
		}
		k := &t.UniqueKeys[len(t.UniqueKeys)-1]
		k.Columns = append(k.Columns, col)
	}
	return rows.Err()
}

func (m *MySQL) loadForeignKeys(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA,
			k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, rc.UPDATE_RULE, rc.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
		  ON rc.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND rc.CONSTRAINT_NAME = k.CONSTRAINT_NAME
		 AND rc.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, col, refDB, refTable, refCol, upd, del string
		if err := rows.Scan(&name, &col, &refDB, &refTable, &refCol, &upd, &del); err != nil {
			return err
		}
		if n := len(t.ForeignKeys); n == 0 || t.ForeignKeys[n-1].Name != name {
			t.ForeignKeys = append(t.ForeignKeys, meta.ForeignKey{
				Name:     name,
				RefTable: meta.Path{Conn: t.Path.Conn, Database: refDB, Object: refTable},
				OnUpdate: upd,
				OnDelete: del,
			})
		}
		fk := &t.ForeignKeys[len(t.ForeignKeys)-1]
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, refCol)
	}
	return rows.Err()
}

func (m *MySQL) loadChecks(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	var q string
	switch {
	case m.MariaDB:
		if !m.versionAtLeast(10, 2, 22) {
			return nil
		}
		// MariaDB has TABLE_NAME (and LEVEL Column/Table) in CHECK_CONSTRAINTS;
		// column-level checks are named after their column.
		q = `SELECT CONSTRAINT_NAME, CHECK_CLAUSE, 'YES' FROM information_schema.CHECK_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA = ? AND TABLE_NAME = ? ORDER BY CONSTRAINT_NAME`
	default:
		if !m.versionAtLeast(8, 0, 16) {
			return nil
		}
		q = `SELECT cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE, tc.ENFORCED
			FROM information_schema.TABLE_CONSTRAINTS tc
			JOIN information_schema.CHECK_CONSTRAINTS cc
			  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
			ORDER BY cc.CONSTRAINT_NAME`
	}
	rows, err := c.QueryContext(ctx, q, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ck meta.Check
		var enforced string
		if err := rows.Scan(&ck.Name, &ck.Expression, &enforced); err != nil {
			return err
		}
		ck.Expression = m.unescapeExpr(ck.Expression)
		ck.NotEnforced = enforced == "NO"
		t.Checks = append(t.Checks, ck)
	}
	return rows.Err()
}

func (m *MySQL) loadIndexes(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	// Visibility: MySQL IS_VISIBLE (8.0), MariaDB IGNORED (10.6); functional
	// key parts: MySQL EXPRESSION (8.0.13).
	vis, expr := "'YES'", "NULL"
	switch {
	case m.MariaDB && m.versionAtLeast(10, 6, 0):
		vis = "IF(IGNORED = 'YES', 'NO', 'YES')"
	case !m.MariaDB && m.versionAtLeast(8, 0, 0):
		vis = "IS_VISIBLE"
		if m.versionAtLeast(8, 0, 13) {
			expr = "EXPRESSION"
		}
	}
	rows, err := c.QueryContext(ctx, `SELECT INDEX_NAME, NON_UNIQUE, INDEX_TYPE, COLUMN_NAME, `+expr+`, SUB_PART,
			COLLATION, `+vis+`, INDEX_COMMENT
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY INDEX_NAME <> 'PRIMARY', INDEX_NAME, SEQ_IN_INDEX`, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name, typ, visible, comment string
			nonUnique                   int
			col, ex, coll               sql.NullString
			sub                         sql.NullInt64
		)
		if err := rows.Scan(&name, &nonUnique, &typ, &col, &ex, &sub, &coll, &visible, &comment); err != nil {
			return err
		}
		if n := len(t.Indexes); n == 0 || t.Indexes[n-1].Name != name {
			t.Indexes = append(t.Indexes, meta.Index{
				Name: name, Unique: nonUnique == 0, Type: typ,
				Invisible: visible == "NO", Comment: comment,
			})
		}
		ix := &t.Indexes[len(t.Indexes)-1]
		ic := meta.IndexColumn{Name: col.String, Descending: coll.String == "D", SubPart: int(sub.Int64)}
		if !col.Valid && ex.Valid {
			ic.Name, ic.Expression = m.unescapeExpr(ex.String), true
		}
		ix.Columns = append(ix.Columns, ic)
	}
	return rows.Err()
}

func (m *MySQL) loadTriggers(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION
		FROM information_schema.TRIGGERS
		WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?
		ORDER BY FIELD(EVENT_MANIPULATION, 'INSERT', 'UPDATE', 'DELETE'), ACTION_TIMING = 'AFTER',
			ACTION_ORDER, TRIGGER_NAME`, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tr meta.Trigger
		if err := rows.Scan(&tr.Name, &tr.Timing, &tr.Event); err != nil {
			return err
		}
		t.Triggers = append(t.Triggers, tr)
	}
	return rows.Err()
}

// loadPartitions sets Options["partitioning"] to a summary such as
// "RANGE (year(`created`)) 3 partitions: p2023, p2024, pmax".
func (m *MySQL) loadPartitions(ctx context.Context, c dialect.Conn, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT PARTITION_NAME, PARTITION_METHOD, PARTITION_EXPRESSION,
			SUBPARTITION_NAME, SUBPARTITION_METHOD, SUBPARTITION_EXPRESSION
		FROM information_schema.PARTITIONS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND PARTITION_NAME IS NOT NULL
		ORDER BY PARTITION_ORDINAL_POSITION, SUBPARTITION_ORDINAL_POSITION`, t.Path.Database, t.Path.Object)
	if err != nil {
		return err
	}
	defer rows.Close()
	var method, expr, subMethod, subExpr string
	var parts []string
	subs := map[string]bool{}
	for rows.Next() {
		var name, pm, pe, sn, sm, se sql.NullString
		if err := rows.Scan(&name, &pm, &pe, &sn, &sm, &se); err != nil {
			return err
		}
		method, expr = pm.String, m.unescapeExpr(pe.String)
		subMethod, subExpr = sm.String, m.unescapeExpr(se.String)
		if len(parts) == 0 || parts[len(parts)-1] != name.String {
			parts = append(parts, name.String)
		}
		if sn.Valid {
			subs[sn.String] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(parts) == 0 {
		return nil
	}
	s := fmt.Sprintf("%s (%s) %d partitions: %s", method, expr, len(parts), strings.Join(parts, ", "))
	if subMethod != "" {
		s += fmt.Sprintf("; subpartitioned by %s (%s), %d subpartitions", subMethod, subExpr, len(subs))
	}
	t.Options["partitioning"] = s
	return nil
}

// versionAtLeast compares the numeric prefix of Version with x.y.z. An
// unknown version counts as new.
func (m *MySQL) versionAtLeast(x, y, z int) bool {
	v := m.Version
	if v == "" {
		return true
	}
	if i := strings.IndexFunc(v, func(r rune) bool { return r != '.' && (r < '0' || r > '9') }); i >= 0 {
		v = v[:i]
	}
	var nums [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		nums[i], _ = strconv.Atoi(p)
	}
	want := [3]int{x, y, z}
	for i := range nums {
		if nums[i] != want[i] {
			return nums[i] > want[i]
		}
	}
	return true
}

// ObjectSource returns the CREATE statement of an object as the server
// reports it: SHOW CREATE VIEW, PROCEDURE, FUNCTION, TRIGGER, EVENT (and
// TABLE). The text is returned unchanged, including DEFINER.
func (m *MySQL) ObjectSource(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (string, error) {
	db, err := m.dbOf(ctx, c, obj)
	if err != nil {
		return "", err
	}
	obj.Database = db
	var what, col string
	switch kind {
	case meta.KindView:
		what, col = "VIEW", "Create View"
	case meta.KindProcedure:
		what, col = "PROCEDURE", "Create Procedure"
	case meta.KindFunction:
		what, col = "FUNCTION", "Create Function"
	case meta.KindTrigger:
		what, col = "TRIGGER", "SQL Original Statement"
	case meta.KindEvent:
		what, col = "EVENT", "Create Event"
	case meta.KindTable, "":
		what, col = "TABLE", "Create Table"
	default:
		return "", fmt.Errorf("%s has no source for kind %q", m.Name(), kind)
	}
	rows, err := c.QueryContext(ctx, "SHOW CREATE "+what+" "+m.Qualify(obj))
	if err != nil {
		if notFound(err) {
			return "", fmt.Errorf("%w: %s %s", meta.ErrNotFound, strings.ToLower(what), m.Qualify(obj))
		}
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	idx := -1
	for i, n := range cols {
		if strings.EqualFold(n, col) {
			idx = i
		}
	}
	if idx < 0 {
		return "", fmt.Errorf("SHOW CREATE %s: no column %q", what, col)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%w: %s %s", meta.ErrNotFound, strings.ToLower(what), m.Qualify(obj))
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	if !vals[idx].Valid {
		// Routine bodies are NULL without SHOW_ROUTINE / ownership.
		return "", fmt.Errorf("no privilege to read the source of %s %s", strings.ToLower(what), m.Qualify(obj))
	}
	return vals[idx].String, rows.Err()
}

// notFound reports server errors meaning "object does not exist".
func notFound(err error) bool {
	switch errNumber(err) {
	case 1049, 1146, 1305, 1360, 1539: // unknown db, no table, no routine, no trigger, unknown event
		return true
	}
	return false
}
