package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

// Option keys DescribeTable uses for facts the metadata model has no field
// for. CopyTable reads them back. <name> is the index name.
//
//	identity               IDENTITY(seed,increment) of the identity column
//	index <name> constraint  "UNIQUE" when the index backs a UNIQUE constraint
//	index <name> fillfactor  fill factor when not the server default
//	trigger <name>         "disabled" for a disabled trigger
//	foreign key <name>     "disabled" / "not trusted"
const (
	optIdentity = "identity"
)

func optIndex(name, what string) string { return "index " + name + " " + what }

// resolve fills in database and schema of obj from the connection when
// they are empty.
func (m *MSSQL) resolve(ctx context.Context, c dialect.Conn, obj meta.Path) (meta.Path, error) {
	if obj.Database == "" {
		db, err := m.CurrentDatabase(ctx, c)
		if err != nil {
			return obj, err
		}
		obj.Database = db
	}
	if obj.Schema == "" {
		s, err := m.CurrentSchema(ctx, c)
		if err != nil {
			return obj, err
		}
		if s == "" {
			s = "dbo"
		}
		obj.Schema = s
	}
	return obj, nil
}

// DescribeTable reads the metadata of a table or view from [db].sys.*
// (spec 6.2, 8.1).
//
// Normalisation: DataType is the declared type with length, precision or
// scale (nvarchar(40), nvarchar(max), decimal(10,2), datetime2(3)); alias
// types are [schema].[name]. Default holds the literal of a default
// constraint with the outer parentheses removed — strings unquoted,
// numbers as written — or, with DefaultExpr, the expression (getdate(),
// NEXT VALUE FOR …); DEFAULT NULL is nil. Computed columns have Generated
// with the expression as stored by the server ("([qty]*[price])") and
// Stored = PERSISTED. System-versioning period columns have Generated with
// Expression "GENERATED ALWAYS AS ROW START|END". Comments come from the
// MS_Description extended property. UniqueKeys holds unique constraints
// and unfiltered unique indexes; Indexes holds every index including the
// one backing the primary key.
func (m *MSSQL) DescribeTable(ctx context.Context, c dialect.Conn, obj meta.Path) (*meta.Table, error) {
	obj, err := m.resolve(ctx, c, obj)
	if err != nil {
		return nil, err
	}
	x := m.sysPrefix(obj.Database)
	var (
		id      int64
		typ     string
		comment sql.NullString
	)
	err = c.QueryRowContext(ctx, `SELECT o.object_id, o.type, CAST(ep.value AS nvarchar(max))
		FROM `+x+`objects o
		JOIN `+x+`schemas s ON s.schema_id = o.schema_id
		LEFT JOIN `+x+`extended_properties ep ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
		WHERE s.name = @p1 AND o.name = @p2 AND o.type IN ('U', 'V')`, obj.Schema, obj.Object).Scan(&id, &typ, &comment)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", meta.ErrNotFound, m.Qualify(obj))
	}
	if err != nil {
		return nil, err
	}
	t := &meta.Table{Path: obj, Comment: comment.String, Options: map[string]string{}}
	if err := m.loadColumns(ctx, c, x, id, t); err != nil {
		return nil, err
	}
	steps := []func(context.Context, dialect.Conn, string, int64, *meta.Table) error{m.loadIndexes, m.loadTriggers}
	if strings.TrimSpace(typ) == "V" {
		t.Kind = meta.KindView
		steps = append([]func(context.Context, dialect.Conn, string, int64, *meta.Table) error{m.loadView}, steps...)
	} else {
		t.Kind = meta.KindTable
		steps = append(steps, m.loadForeignKeys, m.loadChecks, m.loadTableOptions)
	}
	for _, f := range steps {
		if err := f(ctx, c, x, id, t); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (m *MSSQL) loadColumns(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	genAlways := "0"
	if m.atLeast(13) {
		genAlways = "c.generated_always_type"
	}
	rows, err := c.QueryContext(ctx, `SELECT c.column_id, c.name, ty.name, ts.name, ty.is_user_defined,
			c.max_length, c.precision, c.scale, c.is_nullable, c.is_identity, c.collation_name,
			dc.definition, cc.definition, COALESCE(cc.is_persisted, 0),
			CAST(ic.seed_value AS nvarchar(64)), CAST(ic.increment_value AS nvarchar(64)),
			CAST(ep.value AS nvarchar(max)), `+genAlways+`
		FROM `+x+`columns c
		JOIN `+x+`types ty ON ty.user_type_id = c.user_type_id
		JOIN `+x+`schemas ts ON ts.schema_id = ty.schema_id
		LEFT JOIN `+x+`default_constraints dc ON dc.object_id = c.default_object_id
		LEFT JOIN `+x+`computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		LEFT JOIN `+x+`identity_columns ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
		LEFT JOIN `+x+`extended_properties ep ON ep.class = 1 AND ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.name = 'MS_Description'
		WHERE c.object_id = @p1
		ORDER BY c.column_id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	pos := 0
	for rows.Next() {
		var (
			colID                          int
			col                            meta.Column
			typName, typSchema             string
			userDefined, persisted         bool
			maxLen, prec, scale            int
			identity                       bool
			coll, def, computed, seed, inc sql.NullString
			comment                        sql.NullString
			genType                        int
		)
		if err := rows.Scan(&colID, &col.Name, &typName, &typSchema, &userDefined, &maxLen, &prec, &scale,
			&col.Nullable, &identity, &coll, &def, &computed, &persisted, &seed, &inc, &comment, &genType); err != nil {
			return err
		}
		pos++
		col.Position = pos
		if userDefined {
			col.DataType = m.QuoteIdent(typSchema) + "." + m.QuoteIdent(typName)
		} else {
			col.DataType = typeDecl(typName, maxLen, prec, scale)
		}
		col.Collation = coll.String
		col.Comment = comment.String
		col.AutoIncrement = identity
		if identity && seed.Valid && inc.Valid {
			t.Options[optIdentity] = "IDENTITY(" + seed.String + "," + inc.String + ")"
		}
		switch {
		case computed.Valid:
			col.Generated = &meta.Generated{Expression: computed.String, Stored: persisted}
		case genType == 1 || genType == 2:
			what := "START"
			if genType == 2 {
				what = "END"
			}
			col.Generated = &meta.Generated{Expression: "GENERATED ALWAYS AS ROW " + what, Stored: true}
		case genType > 2: // ledger / transaction-id columns (2022)
			col.Generated = &meta.Generated{Expression: "GENERATED ALWAYS", Stored: true}
		}
		if def.Valid {
			col.Default, col.DefaultExpr = parseDefault(def.String)
		}
		t.Columns = append(t.Columns, col)
	}
	return rows.Err()
}

// typeDecl renders a system type as declared.
func typeDecl(name string, maxLen, prec, scale int) string {
	switch strings.ToLower(name) {
	case "char", "varchar", "binary", "varbinary":
		if maxLen < 0 {
			return name + "(max)"
		}
		return fmt.Sprintf("%s(%d)", name, maxLen)
	case "nchar", "nvarchar":
		if maxLen < 0 {
			return name + "(max)"
		}
		return fmt.Sprintf("%s(%d)", name, maxLen/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", name, prec, scale)
	case "datetime2", "datetimeoffset", "time":
		return fmt.Sprintf("%s(%d)", name, scale)
	case "float":
		if prec != 53 {
			return fmt.Sprintf("float(%d)", prec)
		}
	case "vector": // SQL Server 2025: max_length = 8 + 4 * dimensions
		if maxLen > 8 {
			return fmt.Sprintf("vector(%d)", (maxLen-8)/4)
		}
	}
	return name
}

var numericLiteral = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

// stripParens removes parentheses that enclose the whole expression.
func stripParens(s string) string {
	for {
		s = strings.TrimSpace(s)
		if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
			return s
		}
		depth := 0
		closesAtEnd := true
		for _, t := range sqltext.NewMSSQL().Tokenize(s) {
			if t.Kind != sqltext.TokPunct {
				continue
			}
			switch t.Text {
			case "(":
				depth++
			case ")":
				depth--
				if depth == 0 && t.End != len(s) {
					closesAtEnd = false
				}
			}
			if !closesAtEnd {
				break
			}
		}
		if !closesAtEnd {
			return s
		}
		s = s[1 : len(s)-1]
	}
}

// parseDefault turns a default constraint definition into (value,
// isExpression): "((0))" → 0, "('a”b')" → a'b, "(N'x')" → x, "(NULL)" →
// nil, "(getdate())" → getdate() as expression.
func parseDefault(def string) (*string, bool) {
	v := stripParens(def)
	switch {
	case strings.EqualFold(v, "NULL"):
		return nil, false
	case numericLiteral.MatchString(v):
		return &v, false
	}
	if s, ok := unquoteLiteral(v); ok {
		return &s, false
	}
	return &v, true
}

// unquoteLiteral decodes '…' or N'…' if v is exactly one string literal.
func unquoteLiteral(v string) (string, bool) {
	toks := sqltext.NewMSSQL().Tokenize(v)
	if len(toks) != 1 || toks[0].Kind != sqltext.TokString {
		return "", false
	}
	s := v
	if s[0] == 'N' || s[0] == 'n' {
		s = s[1:]
	}
	if len(s) < 2 || s[len(s)-1] != '\'' {
		return "", false
	}
	return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), true
}

func (m *MSSQL) loadView(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	var (
		def                sql.NullString
		check, schemaBound bool
	)
	err := c.QueryRowContext(ctx, `SELECT sm.definition, v.with_check_option, sm.is_schema_bound
		FROM `+x+`views v JOIN `+x+`sql_modules sm ON sm.object_id = v.object_id
		WHERE v.object_id = @p1`, id).Scan(&def, &check, &schemaBound)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if !def.Valid {
		t.Options["definition"] = "encrypted or not visible"
	} else {
		t.ViewDefinition = viewSelect(def.String)
	}
	if check {
		t.Options["check_option"] = "CASCADED"
	}
	if schemaBound {
		t.Options["schemabinding"] = "YES"
	}
	return nil
}

// viewSelect returns the text after the AS that follows the view header
// (CREATE VIEW name [(cols)] [WITH options] AS …).
func viewSelect(def string) string {
	depth := 0
	seenView := false
	for _, t := range sqltext.NewMSSQL().Tokenize(def) {
		switch {
		case t.Kind == sqltext.TokPunct && t.Text == "(":
			depth++
		case t.Kind == sqltext.TokPunct && t.Text == ")":
			depth--
		case t.Kind == sqltext.TokIdent && depth == 0:
			w := strings.ToUpper(t.Text)
			if w == "VIEW" {
				seenView = true
			} else if w == "AS" && seenView {
				return strings.TrimRight(strings.TrimSpace(def[t.End:]), ";")
			}
		}
	}
	return def
}

func (m *MSSQL) loadIndexes(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT i.name, i.type_desc, i.is_unique, i.is_primary_key, i.is_unique_constraint,
			i.has_filter, i.filter_definition, i.is_disabled, i.fill_factor,
			col.name, ic.key_ordinal, ic.is_descending_key, ic.is_included_column
		FROM `+x+`indexes i
		JOIN `+x+`index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN `+x+`columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		WHERE i.object_id = @p1 AND i.type > 0 AND i.is_hypothetical = 0
		ORDER BY i.is_primary_key DESC, i.name, ic.is_included_column,
			CASE WHEN ic.key_ordinal = 0 THEN 1000 + ic.index_column_id ELSE ic.key_ordinal END`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name, typ, col                      string
			unique, pk, uc, hasFilter, disabled bool
			filter                              sql.NullString
			fill, keyOrd                        int
			desc, included                      bool
		)
		if err := rows.Scan(&name, &typ, &unique, &pk, &uc, &hasFilter, &filter, &disabled, &fill,
			&col, &keyOrd, &desc, &included); err != nil {
			return err
		}
		if n := len(t.Indexes); n == 0 || t.Indexes[n-1].Name != name {
			ix := meta.Index{Name: name, Unique: unique, Type: typ, Invisible: disabled}
			if hasFilter && filter.Valid {
				ix.Filter = filter.String
			}
			t.Indexes = append(t.Indexes, ix)
			if uc {
				t.Options[optIndex(name, "constraint")] = "UNIQUE"
			}
			if fill != 0 {
				t.Options[optIndex(name, "fillfactor")] = strconv.Itoa(fill)
			}
			switch {
			case pk:
				t.PrimaryKey = &meta.Key{Name: name}
			case uc || (unique && !hasFilter):
				t.UniqueKeys = append(t.UniqueKeys, meta.Key{Name: name})
			}
		}
		ix := &t.Indexes[len(t.Indexes)-1]
		columnstore := strings.Contains(typ, "COLUMNSTORE")
		switch {
		case included && !columnstore:
			ix.Include = append(ix.Include, col)
			continue
		case keyOrd == 0 && !columnstore:
			continue // partitioning column not in the key
		}
		ix.Columns = append(ix.Columns, meta.IndexColumn{Name: col, Descending: desc})
		var k *meta.Key
		switch {
		case t.PrimaryKey != nil && t.PrimaryKey.Name == name:
			k = t.PrimaryKey
		case len(t.UniqueKeys) > 0 && t.UniqueKeys[len(t.UniqueKeys)-1].Name == name:
			k = &t.UniqueKeys[len(t.UniqueKeys)-1]
		}
		if k != nil {
			k.Columns = append(k.Columns, col)
		}
	}
	return rows.Err()
}

func (m *MSSQL) loadForeignKeys(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT fk.name, pc.name, rs.name, ro.name, rc.name,
			fk.update_referential_action_desc, fk.delete_referential_action_desc, fk.is_disabled, fk.is_not_trusted
		FROM `+x+`foreign_keys fk
		JOIN `+x+`foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN `+x+`columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN `+x+`objects ro ON ro.object_id = fk.referenced_object_id
		JOIN `+x+`schemas rs ON rs.schema_id = ro.schema_id
		JOIN `+x+`columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE fk.parent_object_id = @p1
		ORDER BY fk.name, fkc.constraint_column_id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, col, rs, rt, rcol, upd, del string
		var disabled, untrusted bool
		if err := rows.Scan(&name, &col, &rs, &rt, &rcol, &upd, &del, &disabled, &untrusted); err != nil {
			return err
		}
		if n := len(t.ForeignKeys); n == 0 || t.ForeignKeys[n-1].Name != name {
			t.ForeignKeys = append(t.ForeignKeys, meta.ForeignKey{
				Name:     name,
				RefTable: meta.Path{Conn: t.Path.Conn, Database: t.Path.Database, Schema: rs, Object: rt},
				OnUpdate: strings.ReplaceAll(upd, "_", " "),
				OnDelete: strings.ReplaceAll(del, "_", " "),
			})
			switch {
			case disabled:
				t.Options["foreign key "+name] = "disabled"
			case untrusted:
				t.Options["foreign key "+name] = "not trusted"
			}
		}
		fk := &t.ForeignKeys[len(t.ForeignKeys)-1]
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, rcol)
	}
	return rows.Err()
}

func (m *MSSQL) loadChecks(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT name, definition, is_disabled FROM `+x+`check_constraints
		WHERE parent_object_id = @p1 ORDER BY name`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ck meta.Check
		if err := rows.Scan(&ck.Name, &ck.Expression, &ck.NotEnforced); err != nil {
			return err
		}
		t.Checks = append(t.Checks, ck)
	}
	return rows.Err()
}

func (m *MSSQL) loadTriggers(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	rows, err := c.QueryContext(ctx, `SELECT tr.name, tr.is_instead_of_trigger, tr.is_disabled, te.type_desc
		FROM `+x+`triggers tr
		JOIN `+x+`trigger_events te ON te.object_id = tr.object_id
		WHERE tr.parent_id = @p1
		ORDER BY tr.name, te.type`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	order := map[string]int{"INSERT": 1, "UPDATE": 2, "DELETE": 3}
	events := map[string][]string{}
	for rows.Next() {
		var name, ev string
		var instead, disabled bool
		if err := rows.Scan(&name, &instead, &disabled, &ev); err != nil {
			return err
		}
		if n := len(t.Triggers); n == 0 || t.Triggers[n-1].Name != name {
			timing := "AFTER"
			if instead {
				timing = "INSTEAD OF"
			}
			t.Triggers = append(t.Triggers, meta.Trigger{Name: name, Timing: timing})
			if disabled {
				t.Options["trigger "+name] = "disabled"
			}
		}
		events[name] = append(events[name], ev)
	}
	for i := range t.Triggers {
		evs := events[t.Triggers[i].Name]
		sort.Slice(evs, func(a, b int) bool { return order[evs[a]] < order[evs[b]] })
		t.Triggers[i].Event = strings.Join(evs, ", ")
	}
	return rows.Err()
}

// loadTableOptions sets rows (estimate), filegroup or partitioning,
// data_compression, temporal, lock_escalation and memory_optimized.
func (m *MSSQL) loadTableOptions(ctx context.Context, c dialect.Conn, x string, id int64, t *meta.Table) error {
	temporal, history, memOpt := "''", "NULL", "0"
	if m.atLeast(13) {
		temporal = "tb.temporal_type_desc"
		history = "(SELECT QUOTENAME(hs.name) + '.' + QUOTENAME(ho.name) FROM " + x + "objects ho JOIN " + x +
			"schemas hs ON hs.schema_id = ho.schema_id WHERE ho.object_id = tb.history_table_id)"
	}
	if m.atLeast(12) {
		memOpt = "tb.is_memory_optimized"
	}
	var (
		rows                        sql.NullInt64
		temporalType, hist, lockEsc sql.NullString
		dsName, dsType, compression sql.NullString
		isMem                       bool
	)
	err := c.QueryRowContext(ctx, `SELECT
			(SELECT SUM(p.rows) FROM `+x+`partitions p WHERE p.object_id = tb.object_id AND p.index_id IN (0, 1)),
			`+temporal+`, `+history+`, tb.lock_escalation_desc, `+memOpt+`,
			ds.name, ds.type,
			(SELECT TOP (1) p.data_compression_desc FROM `+x+`partitions p WHERE p.object_id = tb.object_id AND p.index_id IN (0, 1) ORDER BY p.partition_number)
		FROM `+x+`tables tb
		LEFT JOIN `+x+`indexes i ON i.object_id = tb.object_id AND i.index_id IN (0, 1)
		LEFT JOIN `+x+`data_spaces ds ON ds.data_space_id = i.data_space_id
		WHERE tb.object_id = @p1`, id).Scan(&rows, &temporalType, &hist, &lockEsc, &isMem, &dsName, &dsType, &compression)
	if err != nil {
		return err
	}
	if rows.Valid {
		t.Options["rows"] = strconv.FormatInt(rows.Int64, 10)
	}
	if temporalType.Valid && temporalType.String != "" && temporalType.String != "NON_TEMPORAL_TABLE" {
		t.Options["temporal"] = temporalType.String
		if hist.Valid {
			t.Options["history_table"] = hist.String
		}
	}
	if lockEsc.Valid && lockEsc.String != "TABLE" {
		t.Options["lock_escalation"] = lockEsc.String
	}
	if isMem {
		t.Options["memory_optimized"] = "YES"
	}
	if compression.Valid && compression.String != "NONE" {
		t.Options["data_compression"] = compression.String
	}
	switch strings.TrimSpace(dsType.String) {
	case "FG":
		t.Options["filegroup"] = dsName.String
	case "PS":
		return m.loadPartitioning(ctx, c, x, id, dsName.String, t)
	}
	return nil
}

func (m *MSSQL) loadPartitioning(ctx context.Context, c dialect.Conn, x string, id int64, scheme string, t *meta.Table) error {
	var fn, col string
	var right bool
	var fanout int
	err := c.QueryRowContext(ctx, `SELECT pf.name, pf.boundary_value_on_right, pf.fanout, col.name
		FROM `+x+`indexes i
		JOIN `+x+`partition_schemes ps ON ps.data_space_id = i.data_space_id
		JOIN `+x+`partition_functions pf ON pf.function_id = ps.function_id
		JOIN `+x+`index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.partition_ordinal = 1
		JOIN `+x+`columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		WHERE i.object_id = @p1 AND i.index_id IN (0, 1)`, id).Scan(&fn, &right, &fanout, &col)
	if err != nil {
		return err
	}
	side := "LEFT"
	if right {
		side = "RIGHT"
	}
	t.Options["partitioning"] = fmt.Sprintf("RANGE %s (%s) %d partitions: function %s, scheme %s",
		side, m.QuoteIdent(col), fanout, m.QuoteIdent(fn), m.QuoteIdent(scheme))
	return nil
}

// module is a row of sys.sql_modules with its object.
type module struct {
	typ                    string
	definition             sql.NullString
	ansiNulls, quotedIdent bool
}

func kindMatches(kind meta.ObjectKind, typ string) bool {
	return kind == "" || objectKind(typ) == kind
}

func (m *MSSQL) loadModule(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (*module, error) {
	x := m.sysPrefix(obj.Database)
	mod := &module{}
	err := c.QueryRowContext(ctx, `SELECT o.type, sm.definition, sm.uses_ansi_nulls, sm.uses_quoted_identifier
		FROM `+x+`objects o
		JOIN `+x+`schemas s ON s.schema_id = o.schema_id
		JOIN `+x+`sql_modules sm ON sm.object_id = o.object_id
		WHERE s.name = @p1 AND o.name = @p2`, obj.Schema, obj.Object).
		Scan(&mod.typ, &mod.definition, &mod.ansiNulls, &mod.quotedIdent)
	if err == sql.ErrNoRows || (err == nil && !kindMatches(kind, mod.typ)) {
		k := string(kind)
		if k == "" {
			k = "object"
		}
		return nil, fmt.Errorf("%w: %s %s", meta.ErrNotFound, k, m.Qualify(obj))
	}
	if err != nil {
		return nil, err
	}
	if !mod.definition.Valid {
		return nil, fmt.Errorf("the source of %s is encrypted or not visible (VIEW DEFINITION)", m.Qualify(obj))
	}
	return mod, nil
}

// ObjectSource returns the module text of a view, procedure, function or
// trigger from sys.sql_modules unchanged (including comments before
// CREATE). Sequences have no module text; their source is generated from
// sys.sequences. Tables have no source on MSSQL (spec 4: no CREATE TABLE
// reconstruction); cat uses DescribeTable instead.
func (m *MSSQL) ObjectSource(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (string, error) {
	obj, err := m.resolve(ctx, c, obj)
	if err != nil {
		return "", err
	}
	switch kind {
	case meta.KindSequence:
		return m.sequenceSource(ctx, c, obj)
	case meta.KindTable:
		return "", fmt.Errorf("mssql has no source text for tables; use cat for %s", m.Qualify(obj))
	}
	mod, err := m.loadModule(ctx, c, obj, kind)
	if err != nil {
		return "", err
	}
	return mod.definition.String, nil
}

func (m *MSSQL) sequenceSource(ctx context.Context, c dialect.Conn, obj meta.Path) (string, error) {
	x := m.sysPrefix(obj.Database)
	var (
		typ                         string
		start, inc, min, max, cache sql.NullString
		cycle, cached               bool
	)
	err := c.QueryRowContext(ctx, `SELECT ty.name, CAST(sq.start_value AS nvarchar(64)), CAST(sq.increment AS nvarchar(64)),
			CAST(sq.minimum_value AS nvarchar(64)), CAST(sq.maximum_value AS nvarchar(64)), sq.is_cycling,
			sq.is_cached, CAST(sq.cache_size AS nvarchar(64))
		FROM `+x+`sequences sq
		JOIN `+x+`schemas s ON s.schema_id = sq.schema_id
		JOIN `+x+`types ty ON ty.user_type_id = sq.user_type_id
		WHERE s.name = @p1 AND sq.name = @p2`, obj.Schema, obj.Object).
		Scan(&typ, &start, &inc, &min, &max, &cycle, &cached, &cache)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%w: sequence %s", meta.ErrNotFound, m.Qualify(obj))
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE SEQUENCE %s.%s AS %s\n    START WITH %s\n    INCREMENT BY %s\n    MINVALUE %s\n    MAXVALUE %s\n",
		m.QuoteIdent(obj.Schema), m.QuoteIdent(obj.Object), typ, start.String, inc.String, min.String, max.String)
	if cycle {
		b.WriteString("    CYCLE\n")
	} else {
		b.WriteString("    NO CYCLE\n")
	}
	switch {
	case !cached:
		b.WriteString("    NO CACHE")
	case cache.Valid:
		b.WriteString("    CACHE " + cache.String)
	default:
		b.WriteString("    CACHE")
	}
	return b.String(), nil
}
