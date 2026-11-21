package mysql

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

// CopyTable returns the statements of cp (spec 8.2).
//
//   - Data only: CREATE TABLE dst AS SELECT * FROM src (tables and views; a
//     view becomes a snapshot table, A2).
//   - With structure, table: CREATE TABLE dst LIKE src, then INSERT INTO dst
//     (cols) SELECT cols FROM src without generated columns. LIKE copies
//     columns, keys, indexes, check constraints and partitioning, but never
//     foreign keys or triggers (A1).
//   - With structure, view: a second view with the same definition (A2);
//     t must come from DescribeTable (ViewDefinition).
func (m *MySQL) CopyTable(src, dst meta.Path, t *meta.Table, withStructure bool) ([]string, error) {
	if src.Object == "" || dst.Object == "" {
		return nil, fmt.Errorf("copy needs a source and a target name")
	}
	if !withStructure {
		return []string{"CREATE TABLE " + m.Qualify(dst) + " AS SELECT * FROM " + m.Qualify(src)}, nil
	}
	if t == nil {
		return nil, fmt.Errorf("copy with structure needs the metadata of %s", m.Qualify(src))
	}
	if t.Kind == meta.KindView {
		if t.ViewDefinition == "" {
			return nil, fmt.Errorf("no view definition for %s", m.Qualify(src))
		}
		stmt := "CREATE VIEW " + m.Qualify(dst) + " AS " + t.ViewDefinition
		switch strings.ToUpper(t.Options["check_option"]) {
		case "CASCADED":
			stmt += " WITH CASCADED CHECK OPTION"
		case "LOCAL":
			stmt += " WITH LOCAL CHECK OPTION"
		}
		return []string{stmt}, nil
	}
	var cols []string
	for _, c := range t.Columns {
		if c.Generated == nil {
			cols = append(cols, m.QuoteIdent(c.Name))
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s has no insertable columns", m.Qualify(src))
	}
	list := strings.Join(cols, ", ")
	return []string{
		"CREATE TABLE " + m.Qualify(dst) + " LIKE " + m.Qualify(src),
		"INSERT INTO " + m.Qualify(dst) + " (" + list + ") SELECT " + list + " FROM " + m.Qualify(src),
	}, nil
}

// TextSearchPredicate returns the quick-search condition (spec 10.5): a
// case-insensitive "contains" across all searchable columns, combined with
// OR. param is the placeholder ("?" when empty); it appears exactly once per
// searchable column, so the caller binds the search text
// len(m.TextSearchColumns(columns)) times. The text is matched literally
// (LOCATE, no LIKE wildcards). Non-text columns are compared through
// CAST(col AS CHAR); binary, bit, geometry and vector columns are skipped.
// With no searchable column the predicate is "1 = 0" and takes no parameter.
func (m *MySQL) TextSearchPredicate(columns []meta.Column, param string) string {
	if param == "" {
		param = "?"
	}
	var parts []string
	for _, c := range m.TextSearchColumns(columns) {
		expr := m.QuoteIdent(c.Name)
		if !isTextType(c.DataType) {
			expr = "CAST(" + expr + " AS CHAR)"
		}
		parts = append(parts, "LOCATE(LOWER("+param+"), LOWER("+expr+")) > 0")
	}
	if len(parts) == 0 {
		return "1 = 0"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// TextSearchParamCount is len(TextSearchColumns(columns)): "?" binds one
// argument per occurrence.
func (m *MySQL) TextSearchParamCount(columns []meta.Column) int {
	return len(m.TextSearchColumns(columns))
}

// Placeholder is always "?" (go-sql-driver/mysql).
func (m *MySQL) Placeholder(n int) string { return "?" }

// InsertDefaults spells the all-defaults INSERT of MySQL and MariaDB.
func (m *MySQL) InsertDefaults(qualifiedTable string) string {
	return "INSERT INTO " + qualifiedTable + " () VALUES ()"
}

// TextSearchColumns returns the columns TextSearchPredicate searches, in
// order; one parameter is bound per column.
func (m *MySQL) TextSearchColumns(columns []meta.Column) []meta.Column {
	var out []meta.Column
	for _, c := range columns {
		if !isBinaryType(c.DataType) && !strings.EqualFold(c.Collation, "binary") {
			out = append(out, c)
		}
	}
	return out
}

func baseType(dataType string) string {
	t := strings.ToLower(strings.TrimSpace(dataType))
	if i := strings.IndexAny(t, "( "); i >= 0 {
		t = t[:i]
	}
	return t
}

func isTextType(dataType string) bool {
	switch baseType(dataType) {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		return true
	}
	return false
}

func isBinaryType(dataType string) bool {
	switch baseType(dataType) {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit",
		"geometry", "point", "linestring", "polygon", "multipoint", "multilinestring",
		"multipolygon", "geometrycollection", "geomcollection", "vector":
		return true
	}
	return false
}

// ReplaceScript returns a runnable script that re-creates the object from
// its current source (spec 9.1):
//
//   - View (both servers): CREATE OR REPLACE … VIEW.
//   - MariaDB routine, trigger, event: CREATE OR REPLACE ….
//   - MySQL routine, trigger, event: DROP … IF EXISTS, then CREATE ….
//     This is not atomic, and the DROP discards privileges granted on a
//     routine (A21, see the integration test).
//
// Routine, trigger and event bodies are wrapped in DELIMITER lines with a
// delimiter that does not occur in the source ($$, //, ;;, …), so the
// script runs unchanged through sqltext.NewMySQL or the mysql client.
//
// The object name (and a trigger's table) is qualified with the database,
// so the script works whatever the session's current database is.
//
// DEFINER: the source is kept as the server returns it, including its
// DEFINER clause, because dropping it would silently change the security
// context of SQL SECURITY DEFINER objects. The clause is removed only when
// it would make the CREATE fail: the definer is not the current account
// and the account has none of SUPER, SET_USER_ID, SET_ANY_DEFINER (MySQL)
// or SET USER (MariaDB). A comment line in the script then says so. On
// MySQL this also prevents the DROP succeeding and the CREATE failing.
func (m *MySQL) ReplaceScript(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (string, error) {
	db, err := m.dbOf(ctx, c, obj)
	if err != nil {
		return "", err
	}
	obj.Database = db
	var keyword string
	switch kind {
	case meta.KindView:
		keyword = "VIEW"
	case meta.KindProcedure:
		keyword = "PROCEDURE"
	case meta.KindFunction:
		keyword = "FUNCTION"
	case meta.KindTrigger:
		keyword = "TRIGGER"
	case meta.KindEvent:
		keyword = "EVENT"
	default:
		return "", fmt.Errorf("%s cannot replace objects of kind %q", m.Name(), kind)
	}
	src, err := m.ObjectSource(ctx, c, obj, kind)
	if err != nil {
		return "", err
	}
	src = strings.TrimRight(strings.TrimSpace(src), ";")
	if kind == meta.KindView {
		if src, err = m.qualifiedViewSource(ctx, c, obj, src); err != nil {
			return "", err
		}
	}

	var note string
	if definer, ok := findDefiner(src); ok {
		keep, why, err := m.mayUseDefiner(ctx, c, definer)
		if err != nil {
			return "", err
		}
		if !keep {
			src = removeDefiner(src)
			note = fmt.Sprintf("-- DEFINER=%s removed: %s; the object is re-created with the current account as definer.\n", definer, why)
		}
	}
	orReplace := kind == meta.KindView || m.MariaDB
	create, err := m.qualifyCreate(src, keyword, obj, orReplace)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(note)
	if kind == meta.KindView {
		b.WriteString(create + ";\n")
		return b.String(), nil
	}
	if !orReplace {
		fmt.Fprintf(&b, "DROP %s IF EXISTS %s;\n", keyword, m.Qualify(obj))
	}
	delim := chooseDelimiter(create)
	sep := ""
	if endsWithLineComment(create) {
		sep = "\n"
	}
	fmt.Fprintf(&b, "DELIMITER %s\n%s%s%s\nDELIMITER ;\n", delim, create, sep, delim)
	return b.String(), nil
}

// qualifiedViewSource replaces the SELECT of a SHOW CREATE VIEW text by
// information_schema.VIEWS.VIEW_DEFINITION. SHOW CREATE VIEW leaves table
// references in the session's current database unqualified, so its text
// would resolve differently in another session; VIEW_DEFINITION is always
// fully qualified. The header (ALGORITHM, DEFINER, SQL SECURITY, name,
// column list) and a trailing WITH … CHECK OPTION are kept.
func (m *MySQL) qualifiedViewSource(ctx context.Context, c dialect.Conn, obj meta.Path, src string) (string, error) {
	var def string
	err := c.QueryRowContext(ctx, `SELECT VIEW_DEFINITION FROM information_schema.VIEWS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, obj.Database, obj.Object).Scan(&def)
	if err != nil || def == "" {
		return src, nil // keep the server text if the definition is not visible
	}
	toks := significant(src)
	as := -1
	depth := 0
	for i, t := range toks {
		switch {
		case t.Text == "(":
			depth++
		case t.Text == ")":
			depth--
		case depth == 0 && t.Kind == sqltext.TokIdent && strings.EqualFold(t.Text, "VIEW"):
			as = -2 // start looking for AS after the view keyword
		case as == -2 && depth == 0 && t.Kind == sqltext.TokIdent && strings.EqualFold(t.Text, "AS"):
			as = i
		}
		if as >= 0 {
			break
		}
	}
	if as < 0 {
		return src, nil
	}
	tail := ""
	if n := len(toks); n >= 3 && strings.EqualFold(toks[n-1].Text, "OPTION") && strings.EqualFold(toks[n-2].Text, "CHECK") {
		w := n - 3
		if w >= 0 && !strings.EqualFold(toks[w].Text, "WITH") {
			w-- // WITH CASCADED|LOCAL CHECK OPTION
		}
		if w >= 0 && strings.EqualFold(toks[w].Text, "WITH") {
			tail = " " + src[toks[w].Start:]
		}
	}
	return src[:toks[as].End] + " " + def + tail, nil
}

// chooseDelimiter returns the first candidate that does not occur in body.
func chooseDelimiter(body string) string {
	for _, d := range []string{"$$", "//", ";;", "$$$", "///", "@@@@"} {
		if !strings.Contains(body, d) {
			return d
		}
	}
	for i := 1; ; i++ {
		d := fmt.Sprintf("$$%d$$", i)
		if !strings.Contains(body, d) {
			return d
		}
	}
}

func endsWithLineComment(src string) bool {
	toks := sqltext.NewMySQL().Tokenize(src)
	for i := len(toks) - 1; i >= 0; i-- {
		switch toks[i].Kind {
		case sqltext.TokWhitespace:
			continue
		case sqltext.TokComment:
			return !strings.HasPrefix(toks[i].Text, "/*")
		}
		return false
	}
	return false
}

// significant returns the code tokens of src (no whitespace or comments).
func significant(src string) []sqltext.Token {
	var out []sqltext.Token
	for _, t := range sqltext.NewMySQL().Tokenize(src) {
		if t.Kind != sqltext.TokWhitespace && t.Kind != sqltext.TokComment {
			out = append(out, t)
		}
	}
	return out
}

// qualifyCreate makes sure the object name after keyword (and, for
// triggers, the table after ON) carries the database name. With orReplace
// it turns CREATE into CREATE OR REPLACE. An IF NOT EXISTS clause is
// removed, since the script is meant to replace the object.
func (m *MySQL) qualifyCreate(src, keyword string, obj meta.Path, orReplace bool) (string, error) {
	toks := significant(src)
	if len(toks) == 0 || !strings.EqualFold(toks[0].Text, "CREATE") {
		return "", fmt.Errorf("unexpected source for %s: does not start with CREATE", m.Qualify(obj))
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	hasOrReplace := len(toks) > 2 && strings.EqualFold(toks[1].Text, "OR") && strings.EqualFold(toks[2].Text, "REPLACE")
	if orReplace && !hasOrReplace {
		edits = append(edits, edit{toks[0].End, toks[0].End, " OR REPLACE"})
	}
	// qualifyAt rewrites an unqualified name at toks[i] to db.name.
	qualifyAt := func(i int, db string) int {
		if i >= len(toks) {
			return i
		}
		if i+1 < len(toks) && toks[i+1].Text == "." {
			return i + 2 // already qualified
		}
		name := toks[i].Text
		if toks[i].Kind == sqltext.TokQuotedIdent {
			name = strings.ReplaceAll(name[1:len(name)-1], "``", "`")
		}
		edits = append(edits, edit{toks[i].Start, toks[i].End, m.QuoteIdent(db) + "." + m.QuoteIdent(name)})
		return i
	}
	nameAt := -1
	for i := 1; i < len(toks); i++ {
		if toks[i].Kind == sqltext.TokIdent && strings.EqualFold(toks[i].Text, keyword) {
			nameAt = i + 1
			break
		}
		if toks[i].Kind == sqltext.TokIdent && strings.EqualFold(toks[i].Text, "AS") {
			break
		}
	}
	if nameAt < 0 || nameAt >= len(toks) {
		return "", fmt.Errorf("unexpected source for %s: no %s name", m.Qualify(obj), keyword)
	}
	// IF NOT EXISTS (kept in trigger sources as originally typed).
	if nameAt+2 < len(toks) && toks[nameAt].Kind == sqltext.TokIdent && strings.EqualFold(toks[nameAt].Text, "IF") &&
		strings.EqualFold(toks[nameAt+1].Text, "NOT") && strings.EqualFold(toks[nameAt+2].Text, "EXISTS") {
		edits = append(edits, edit{toks[nameAt].Start, toks[nameAt+3-1].End, ""})
		nameAt += 3
	}
	next := qualifyAt(nameAt, obj.Database)
	if keyword == "TRIGGER" {
		for i := next + 1; i < len(toks); i++ {
			if toks[i].Kind == sqltext.TokIdent && strings.EqualFold(toks[i].Text, "ON") {
				qualifyAt(i+1, obj.Database)
				break
			}
		}
	}
	out := src
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		out = out[:e.start] + e.text + out[e.end:]
	}
	return out, nil
}

// reDefiner matches the DEFINER clause of a CREATE header.
var reDefiner = regexp.MustCompile("(?i)^CREATE\\s+(?:OR\\s+REPLACE\\s+)?(?:ALGORITHM\\s*=\\s*\\w+\\s+)?(DEFINER\\s*=\\s*((?:`(?:[^`]|``)*`|'(?:[^']|'')*'|[\\w.%-]+)(?:\\s*@\\s*(?:`(?:[^`]|``)*`|'(?:[^']|'')*'|[\\w.%:-]+))?|CURRENT_USER(?:\\(\\))?)\\s+)")

// findDefiner returns the definer account text (e.g. `root`@`%`).
func findDefiner(src string) (string, bool) {
	sm := reDefiner.FindStringSubmatch(src)
	if sm == nil {
		return "", false
	}
	return sm[2], sm[2] != ""
}

// removeDefiner drops the DEFINER clause from a CREATE header.
func removeDefiner(src string) string {
	loc := reDefiner.FindStringSubmatchIndex(src)
	if loc == nil {
		return src
	}
	return src[:loc[2]] + src[loc[3]:]
}

// accountKey normalises `u`@`h`, 'u'@'h' or u@h to u@h.
func accountKey(acct string) string {
	user, host, _ := strings.Cut(acct, "@")
	unq := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) >= 2 && (s[0] == '`' || s[0] == '\'') && s[len(s)-1] == s[0] {
			q := string(s[0])
			s = strings.ReplaceAll(s[1:len(s)-1], q+q, q)
		}
		return s
	}
	return unq(user) + "@" + unq(host)
}

// mayUseDefiner reports whether a CREATE with DEFINER=definer succeeds for
// the current account, with the reason when it does not.
func (m *MySQL) mayUseDefiner(ctx context.Context, c dialect.Conn, definer string) (bool, string, error) {
	var cur string
	if err := c.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&cur); err != nil {
		return false, "", err
	}
	if strings.EqualFold(accountKey(definer), accountKey(cur)) {
		return true, "", nil
	}
	rows, err := c.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return false, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return false, "", err
		}
		if hasDefinerPrivilege(g) {
			return true, "", rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return false, "", err
	}
	priv := "SUPER, SET_USER_ID or SET_ANY_DEFINER"
	if m.MariaDB {
		priv = "SUPER or SET USER"
	}
	return false, fmt.Sprintf("the current account %s is not the definer and lacks %s", cur, priv), nil
}

// hasDefinerPrivilege reports whether a global GRANT line allows setting
// another account as DEFINER.
func hasDefinerPrivilege(grant string) bool {
	g := strings.ToUpper(grant)
	on := strings.Index(g, " ON ")
	if !strings.HasPrefix(g, "GRANT ") || on < 0 || !strings.HasPrefix(strings.TrimSpace(g[on+4:]), "*.*") {
		return false
	}
	for _, p := range strings.Split(g[len("GRANT "):on], ",") {
		switch strings.TrimSpace(p) {
		case "ALL", "ALL PRIVILEGES", "SUPER", "SET_USER_ID", "SET_ANY_DEFINER", "SET USER":
			return true
		}
	}
	return false
}
