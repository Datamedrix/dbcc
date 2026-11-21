package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

// DescribeTable reads the metadata of a table, partitioned table, foreign
// table, view or materialized view from pg_catalog (spec 6.2, 8.1). c must
// be connected to obj.Database (empty = the connection's database); an
// empty obj.Schema resolves the name through search_path.
//
// Representation of PostgreSQL specifics in the shared model:
//
//   - DataType is format_type() ("character varying(20)", "numeric(10,2)",
//     "timestamp with time zone", "text[]").
//   - Default: literal value (unquoted, casts removed) or, with DefaultExpr,
//     the expression text; nil for no default and DEFAULT NULL.
//   - Serial columns (nextval default on a sequence OWNED BY the column):
//     AutoIncrement with the nextval(…) default expression.
//     Identity columns: AutoIncrement, no default, and
//     Options["identity.<column>"] = "ALWAYS" | "BY DEFAULT".
//   - Generated columns: Generated{Expression, Stored}.
//   - Collation only when it differs from the type's default collation.
//   - UniqueKeys: UNIQUE constraints plus unique indexes on plain columns
//     without predicate (both identify rows for the form).
//   - Checks: CHECK constraints (expression; " NOT VALID" / " NO INHERIT"
//     appended when set) and EXCLUDE constraints (full definition).
//   - Index columns: column name or expression; a non-default operator
//     class, collation or NULLS order is appended to the name
//     ("code varchar_pattern_ops", "created NULLS LAST"). Index.Type is the
//     access method plus NULLS NOT DISTINCT and TABLESPACE; Include holds
//     the INCLUDE columns, Filter the predicate of a partial index. Invisible marks invalid indexes.
//   - Triggers: Timing BEFORE/AFTER/INSTEAD OF; Event like
//     "INSERT OR UPDATE OF a FOR EACH ROW WHEN (…)", "(disabled)" appended.
//   - Options: owner, rows (reltuples estimate, omitted if never analyzed),
//     access_method, tablespace, persistence, options (reloptions),
//     row_security, partitioning ("RANGE (created) 3 partitions: …"),
//     partition_of ("public.logs FOR VALUES FROM … TO …"), inherits,
//     foreign_server, foreign_options, populated (materialized views),
//     identity.<column>.
//   - Kind is KindTable, KindView or KindMaterializedView; views and
//     materialized views get columns, ViewDefinition and options (plus
//     indexes for materialized views).
func (p *Postgres) DescribeTable(ctx context.Context, c dialect.Conn, obj meta.Path) (*meta.Table, error) {
	db, err := p.checkDatabase(ctx, c, obj.Database)
	if err != nil {
		return nil, err
	}
	obj.Database = db
	var (
		oid                         int64
		relkind, schema, comment    string
		reltuples                   float64
		spc, am, persistence, owner string
		relopts                     string
		isPart, rls, rlsForce, pop  bool
	)
	err = c.QueryRowContext(ctx, `SELECT c.oid::int8, c.relkind::text, n.nspname, COALESCE(obj_description(c.oid, 'pg_class'), ''),
			c.reltuples::float8, COALESCE(ts.spcname, ''), COALESCE(am.amname, ''), c.relpersistence::text,
			pg_get_userbyid(c.relowner), COALESCE(array_to_string(c.reloptions, ', '), ''),
			c.relispartition, c.relrowsecurity, c.relforcerowsecurity, c.relispopulated
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
		LEFT JOIN pg_am am ON am.oid = c.relam
		WHERE c.oid = to_regclass($1) AND c.relkind IN ('r', 'p', 'f', 'v', 'm')`, p.Qualify(obj)).
		Scan(&oid, &relkind, &schema, &comment, &reltuples, &spc, &am, &persistence, &owner, &relopts, &isPart, &rls, &rlsForce, &pop)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", meta.ErrNotFound, p.Qualify(obj))
	}
	if err != nil {
		return nil, err
	}
	obj.Schema = schema
	t := &meta.Table{Path: obj, Comment: comment, Options: map[string]string{}}
	set := func(k, v string) {
		if v != "" {
			t.Options[k] = v
		}
	}
	set("owner", owner)
	set("options", relopts)
	if err := p.loadColumns(ctx, c, t, oid); err != nil {
		return nil, err
	}
	switch relkind {
	case "v", "m":
		t.Kind = meta.KindView
		if relkind == "m" {
			t.Kind = meta.KindMaterializedView
			set("access_method", am)
			set("tablespace", spc)
			t.Options["populated"] = map[bool]string{true: "yes", false: "no"}[pop]
		}
		def, err := p.viewDef(ctx, c, oid)
		if err != nil {
			return nil, err
		}
		t.ViewDefinition = def
		if relkind == "m" {
			return t, p.loadIndexes(ctx, c, t, oid)
		}
		return t, nil
	}
	t.Kind = meta.KindTable
	if reltuples >= 0 {
		t.Options["rows"] = strconv.FormatInt(int64(reltuples), 10)
	}
	set("access_method", am)
	set("tablespace", spc)
	switch persistence {
	case "u":
		t.Options["persistence"] = "unlogged"
	case "t":
		t.Options["persistence"] = "temporary"
	}
	switch {
	case rlsForce:
		t.Options["row_security"] = "enabled, forced"
	case rls:
		t.Options["row_security"] = "enabled"
	}
	steps := []func(context.Context, dialect.Conn, *meta.Table, int64) error{
		p.loadKeys, p.loadForeignKeys, p.loadChecks, p.loadIndexes, p.loadTriggers,
	}
	for _, f := range steps {
		if err := f(ctx, c, t, oid); err != nil {
			return nil, err
		}
	}
	if err := p.loadInheritance(ctx, c, t, oid, relkind == "p", isPart); err != nil {
		return nil, err
	}
	if relkind == "f" {
		var srv, opts string
		err := c.QueryRowContext(ctx, `SELECT s.srvname, COALESCE(array_to_string(f.ftoptions, ', '), '')
			FROM pg_foreign_table f JOIN pg_foreign_server s ON s.oid = f.ftserver WHERE f.ftrelid = $1::int8::oid`, oid).Scan(&srv, &opts)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		set("foreign_server", srv)
		set("foreign_options", opts)
	}
	return t, nil
}

func (p *Postgres) loadColumns(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64) error {
	rows, err := c.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
			pg_get_expr(ad.adbin, ad.adrelid), a.attidentity::text, a.attgenerated::text,
			COALESCE(CASE WHEN a.attcollation <> 0 AND a.attcollation <> ty.typcollation THEN co.collname END, ''),
			COALESCE(col_description(a.attrelid, a.attnum), ''),
			(SELECT json_agg(e.enumlabel ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = a.atttypid),
			EXISTS (SELECT 1 FROM pg_depend d JOIN pg_class sq ON sq.oid = d.objid AND sq.relkind = 'S'
				WHERE d.classid = 'pg_class'::regclass AND d.refclassid = 'pg_class'::regclass
				  AND d.refobjid = a.attrelid AND d.refobjsubid = a.attnum AND d.deptype = 'a')
		FROM pg_attribute a
		JOIN pg_type ty ON ty.oid = a.atttypid
		LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		LEFT JOIN pg_collation co ON co.oid = a.attcollation
		WHERE a.attrelid = $1::int8::oid AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			col                 meta.Column
			def                 sql.NullString
			identity, generated string
			enums               []byte
			ownsSeq             bool
		)
		if err := rows.Scan(&col.Name, &col.DataType, &col.Nullable, &def, &identity, &generated, &col.Collation, &col.Comment, &enums, &ownsSeq); err != nil {
			return err
		}
		col.Position = len(t.Columns) + 1
		switch generated {
		case "s", "v":
			col.Generated = &meta.Generated{Expression: def.String, Stored: generated == "s"}
		default:
			col.Default, col.DefaultExpr = normalizeDefault(def)
			// serial: a nextval default on a sequence owned by the column
			// (a default on a shared sequence is an ordinary expression).
			if ownsSeq && col.DefaultExpr && strings.HasPrefix(*col.Default, "nextval(") {
				col.AutoIncrement = true
			}
		}
		switch identity {
		case "a":
			col.AutoIncrement = true
			t.Options["identity."+col.Name] = "ALWAYS"
		case "d":
			col.AutoIncrement = true
			t.Options["identity."+col.Name] = "BY DEFAULT"
		}
		if len(enums) > 0 {
			if err := json.Unmarshal(enums, &col.EnumValues); err != nil {
				return fmt.Errorf("enum labels of %s: %w", col.Name, err)
			}
		}
		t.Columns = append(t.Columns, col)
	}
	return rows.Err()
}

var (
	reStringDefault  = regexp.MustCompile(`^(E?)'((?:[^']|'')*)'(?:::[^':]+)?$`)
	reNumericDefault = regexp.MustCompile(`^\(?(-?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?)\)?(?:::[^':]+)?$`)
	reNullDefault    = regexp.MustCompile(`(?i)^NULL(?:::[^':]+)?$`)
)

// normalizeDefault turns pg_get_expr(adbin) into (value, isExpression):
// 'EU'::bpchar → EU; 'it”s'::character varying → it's; 0.00 → 0.00;
// '-1.5'::numeric → -1.5; true → true; NULL::text → nil; anything else
// (now(), CURRENT_TIMESTAMP, nextval(…), 'a'::text || 'b'::text) is an
// expression.
func normalizeDefault(def sql.NullString) (*string, bool) {
	if !def.Valid {
		return nil, false
	}
	v := strings.TrimSpace(def.String)
	switch {
	case reNullDefault.MatchString(v):
		return nil, false
	case v == "true" || v == "false":
		return &v, false
	}
	if sm := reStringDefault.FindStringSubmatch(v); sm != nil {
		s := strings.ReplaceAll(sm[2], "''", "'")
		if sm[1] == "E" {
			s = unescapeE(s)
		}
		return &s, false
	}
	if sm := reNumericDefault.FindStringSubmatch(v); sm != nil {
		return &sm[1], false
	}
	return &v, true
}

// unescapeE decodes the backslash escapes of an E'…' string body.
func unescapeE(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '\\' || i+1 == len(s) {
			b.WriteByte(ch)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func (p *Postgres) loadKeys(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64) error {
	rows, err := c.QueryContext(ctx, `SELECT con.conname, con.contype::text, a.attname
		FROM pg_constraint con
		CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		WHERE con.conrelid = $1::int8::oid AND con.contype IN ('p', 'u')
		ORDER BY con.contype <> 'p', con.conname, k.ord`, oid)
	if err != nil {
		return err
	}
	if err := scanKeys(rows, t, true); err != nil {
		return err
	}
	// Unique indexes that are not constraints, on plain columns, not partial.
	rows, err = c.QueryContext(ctx, `SELECT ic.relname, 'u', a.attname
		FROM pg_index ix
		JOIN pg_class ic ON ic.oid = ix.indexrelid
		CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum
		WHERE ix.indrelid = $1::int8::oid AND ix.indisunique AND NOT ix.indisprimary
		  AND ix.indpred IS NULL AND ix.indexprs IS NULL AND k.ord <= ix.indnkeyatts
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = ix.indexrelid AND con.conrelid = ix.indrelid)
		ORDER BY ic.relname, k.ord`, oid)
	if err != nil {
		return err
	}
	return scanKeys(rows, t, false)
}

func scanKeys(rows *sql.Rows, t *meta.Table, withPK bool) error {
	defer rows.Close()
	for rows.Next() {
		var name, typ, col string
		if err := rows.Scan(&name, &typ, &col); err != nil {
			return err
		}
		if typ == "p" {
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

var fkActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func (p *Postgres) loadForeignKeys(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64) error {
	// Constraints cloned onto this table for each partition of a
	// partitioned referenced table (conparentid on the same table) are
	// skipped; constraints inherited by a partition from its parent stay.
	rows, err := c.QueryContext(ctx, `SELECT con.conname, a.attname, rn.nspname, rc.relname, ra.attname,
			con.confupdtype::text, con.confdeltype::text
		FROM pg_constraint con
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(attnum, refnum, ord)
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		JOIN pg_class rc ON rc.oid = con.confrelid
		JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		JOIN pg_attribute ra ON ra.attrelid = con.confrelid AND ra.attnum = k.refnum
		WHERE con.conrelid = $1::int8::oid AND con.contype = 'f'
		  AND (con.conparentid = 0 OR (SELECT pc.conrelid FROM pg_constraint pc WHERE pc.oid = con.conparentid) <> con.conrelid)
		ORDER BY con.conname, k.ord`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, col, refSchema, refTable, refCol, upd, del string
		if err := rows.Scan(&name, &col, &refSchema, &refTable, &refCol, &upd, &del); err != nil {
			return err
		}
		if n := len(t.ForeignKeys); n == 0 || t.ForeignKeys[n-1].Name != name {
			t.ForeignKeys = append(t.ForeignKeys, meta.ForeignKey{
				Name:     name,
				RefTable: meta.Path{Conn: t.Path.Conn, Database: t.Path.Database, Schema: refSchema, Object: refTable},
				OnUpdate: fkActions[upd],
				OnDelete: fkActions[del],
			})
		}
		fk := &t.ForeignKeys[len(t.ForeignKeys)-1]
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, refCol)
	}
	return rows.Err()
}

func (p *Postgres) loadChecks(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64) error {
	enforced := "true"
	if p.atLeast(18) {
		enforced = "con.conenforced"
	}
	rows, err := c.QueryContext(ctx, `SELECT con.conname, con.contype::text,
			CASE WHEN con.contype = 'c' THEN pg_get_expr(con.conbin, con.conrelid, true) ELSE pg_get_constraintdef(con.oid, true) END,
			con.convalidated, con.connoinherit, `+enforced+`
		FROM pg_constraint con
		WHERE con.conrelid = $1::int8::oid AND con.contype IN ('c', 'x')
		ORDER BY con.conname`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ck meta.Check
		var typ string
		var valid, noInherit, isEnforced bool
		if err := rows.Scan(&ck.Name, &typ, &ck.Expression, &valid, &noInherit, &isEnforced); err != nil {
			return err
		}
		if typ == "c" {
			if noInherit {
				ck.Expression += " NO INHERIT"
			}
			if !valid {
				ck.Expression += " NOT VALID"
			}
		}
		ck.NotEnforced = !isEnforced
		t.Checks = append(t.Checks, ck)
	}
	return rows.Err()
}

func (p *Postgres) loadIndexes(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64) error {
	nnd := "false"
	if p.atLeast(15) {
		nnd = "ix.indnullsnotdistinct"
	}
	rows, err := c.QueryContext(ctx, `SELECT ic.relname, ix.indisunique, am.amname, pg_indexam_has_property(am.oid, 'can_order'),
			COALESCE(pg_get_expr(ix.indpred, ix.indrelid, true), ''), ix.indisvalid, `+nnd+`,
			COALESCE(obj_description(ix.indexrelid, 'pg_class'), ''), COALESCE(ts.spcname, ''),
			k.ord <= ix.indnkeyatts AS keycol,
			CASE WHEN ix.indkey[k.ord - 1] = 0 THEN pg_get_indexdef(ix.indexrelid, k.ord::int, true) ELSE a.attname END,
			ix.indkey[k.ord - 1] = 0 AS isexpr,
			COALESCE(ix.indoption[k.ord - 1], 0)::int,
			COALESCE(CASE WHEN NOT opc.opcdefault THEN opc.opcname END, ''),
			COALESCE(CASE WHEN ix.indkey[k.ord - 1] <> 0 AND ix.indcollation[k.ord - 1] NOT IN (0, a.attcollation) THEN co.collname END, '')
		FROM pg_index ix
		JOIN pg_class ic ON ic.oid = ix.indexrelid
		JOIN pg_am am ON am.oid = ic.relam
		LEFT JOIN pg_tablespace ts ON ts.oid = ic.reltablespace
		CROSS JOIN LATERAL generate_series(1, ix.indnatts) AS k(ord)
		LEFT JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = ix.indkey[k.ord - 1] AND ix.indkey[k.ord - 1] <> 0
		LEFT JOIN pg_opclass opc ON opc.oid = ix.indclass[k.ord - 1]
		LEFT JOIN pg_collation co ON co.oid = ix.indcollation[k.ord - 1]
		WHERE ix.indrelid = $1::int8::oid
		ORDER BY NOT ix.indisprimary, ic.relname, k.ord`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	type extra struct {
		pred, spc string
		nnd       bool
		include   []string
	}
	var extras []extra
	for rows.Next() {
		var (
			name, method, pred, comment, spc, col, opclass, coll string
			unique, canOrder, valid, nullsNotDistinct, key, expr bool
			option                                               int
		)
		if err := rows.Scan(&name, &unique, &method, &canOrder, &pred, &valid, &nullsNotDistinct, &comment, &spc,
			&key, &col, &expr, &option, &opclass, &coll); err != nil {
			return err
		}
		if n := len(t.Indexes); n == 0 || t.Indexes[n-1].Name != name {
			t.Indexes = append(t.Indexes, meta.Index{Name: name, Unique: unique, Type: method, Invisible: !valid, Comment: comment})
			extras = append(extras, extra{pred: pred, spc: spc, nnd: nullsNotDistinct})
		}
		ix, ex := &t.Indexes[len(t.Indexes)-1], &extras[len(extras)-1]
		if !key {
			ex.include = append(ex.include, col)
			continue
		}
		ic := meta.IndexColumn{Name: col, Expression: expr, Descending: option&1 != 0}
		if coll != "" {
			ic.Name += " COLLATE " + p.QuoteIdent(coll)
		}
		if opclass != "" {
			ic.Name += " " + opclass
		}
		if canOrder {
			nullsFirst := option&2 != 0
			switch {
			case !ic.Descending && nullsFirst:
				ic.Name += " NULLS FIRST"
			case ic.Descending && !nullsFirst:
				ic.Name += " NULLS LAST"
			}
		}
		ix.Columns = append(ix.Columns, ic)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, ex := range extras {
		ix := &t.Indexes[i]
		ix.Include = ex.include
		ix.Filter = ex.pred
		if ex.nnd {
			ix.Type += " NULLS NOT DISTINCT"
		}
		if ex.spc != "" {
			ix.Type += " TABLESPACE " + ex.spc
		}
	}
	return nil
}

func (p *Postgres) loadTriggers(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64) error {
	rows, err := c.QueryContext(ctx, `SELECT t.tgname, t.tgtype::int, t.tgenabled::text,
			COALESCE((SELECT string_agg(a.attname, ', ' ORDER BY a.attnum) FROM pg_attribute a
				WHERE a.attrelid = t.tgrelid AND a.attnum = ANY (t.tgattr::int2[])), ''),
			COALESCE(CASE WHEN t.tgqual IS NOT NULL THEN substring(pg_get_triggerdef(t.oid, true) FROM ' WHEN (.*) EXECUTE ') END, '')
		FROM pg_trigger t
		WHERE t.tgrelid = $1::int8::oid AND NOT t.tgisinternal
		ORDER BY t.tgname`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, enabled, cols, when string
		var typ int
		if err := rows.Scan(&name, &typ, &enabled, &cols, &when); err != nil {
			return err
		}
		t.Triggers = append(t.Triggers, meta.Trigger{Name: name, Timing: triggerTiming(typ), Event: triggerEvent(typ, cols, when, enabled)})
	}
	return rows.Err()
}

// pg_trigger.tgtype bits.
const (
	tgRow      = 1 << 0
	tgBefore   = 1 << 1
	tgInsert   = 1 << 2
	tgDelete   = 1 << 3
	tgUpdate   = 1 << 4
	tgTruncate = 1 << 5
	tgInstead  = 1 << 6
)

func triggerTiming(typ int) string {
	switch {
	case typ&tgInstead != 0:
		return "INSTEAD OF"
	case typ&tgBefore != 0:
		return "BEFORE"
	}
	return "AFTER"
}

func triggerEvent(typ int, cols, when, enabled string) string {
	var ev []string
	if typ&tgInsert != 0 {
		ev = append(ev, "INSERT")
	}
	if typ&tgUpdate != 0 {
		u := "UPDATE"
		if cols != "" {
			u += " OF " + cols
		}
		ev = append(ev, u)
	}
	if typ&tgDelete != 0 {
		ev = append(ev, "DELETE")
	}
	if typ&tgTruncate != 0 {
		ev = append(ev, "TRUNCATE")
	}
	s := strings.Join(ev, " OR ")
	if typ&tgRow != 0 {
		s += " FOR EACH ROW"
	} else {
		s += " FOR EACH STATEMENT"
	}
	if when != "" {
		s += " WHEN " + when
	}
	if enabled == "D" {
		s += " (disabled)"
	}
	return s
}

// loadInheritance fills partitioning, partition_of and inherits.
func (p *Postgres) loadInheritance(ctx context.Context, c dialect.Conn, t *meta.Table, oid int64, partitioned, isPartition bool) error {
	if partitioned {
		var key string
		if err := c.QueryRowContext(ctx, "SELECT pg_get_partkeydef($1::int8::oid)", oid).Scan(&key); err != nil {
			return err
		}
		rows, err := c.QueryContext(ctx, `SELECT CASE WHEN n.nspname = $2 THEN c.relname ELSE n.nspname || '.' || c.relname END
			FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE i.inhparent = $1::int8::oid ORDER BY 1`, oid, t.Path.Schema)
		if err != nil {
			return err
		}
		parts, err := scanStrings(rows)
		if err != nil {
			return err
		}
		s := fmt.Sprintf("%s %d partitions", key, len(parts))
		if len(parts) > 0 {
			s += ": " + strings.Join(parts, ", ")
		}
		t.Options["partitioning"] = s
	}
	rows, err := c.QueryContext(ctx, `SELECT n.nspname || '.' || c.relname ||
			CASE WHEN ch.relispartition THEN ' ' || pg_get_expr(ch.relpartbound, ch.oid) ELSE '' END
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhparent JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_class ch ON ch.oid = i.inhrelid
		WHERE i.inhrelid = $1::int8::oid ORDER BY i.inhseqno`, oid)
	if err != nil {
		return err
	}
	parents, err := scanStrings(rows)
	if err != nil {
		return err
	}
	if len(parents) > 0 {
		if isPartition {
			t.Options["partition_of"] = parents[0]
		} else {
			t.Options["inherits"] = strings.Join(parents, ", ")
		}
	}
	return nil
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// qualifiedCTE returns a CTE "dbcc_sp(v)" that, when it is safe, sets
// search_path to pg_catalog for the current statement only, so that
// pg_get_viewdef, pg_get_triggerdef and pg_get_indexdef schema-qualify
// every name and the text resolves the same in any session. Queries use it
// as "CASE WHEN dbcc_sp.v IS NOT NULL THEN pg_get_…(…) END" so that the
// setting is in place before the function runs. Inside an open
// transaction the setting would outlive the statement, so the trick is
// skipped there and names are qualified only when they are not visible in
// the connection's search_path.
func (p *Postgres) qualifiedCTE(c dialect.Conn) string {
	safe := false
	switch c.(type) {
	case *sql.DB:
		safe = true
	case *sql.Conn:
		safe = p.TxStatus(c) == 'I'
	}
	if safe {
		return "WITH dbcc_sp AS MATERIALIZED (SELECT set_config('search_path', 'pg_catalog', true) AS v) "
	}
	return "WITH dbcc_sp AS MATERIALIZED (SELECT ''::text AS v) "
}

// viewDef returns the SELECT of a view or materialized view without the
// trailing semicolon, with schema-qualified names (qualifiedCTE).
func (p *Postgres) viewDef(ctx context.Context, c dialect.Conn, oid int64) (string, error) {
	var def string
	err := c.QueryRowContext(ctx, p.qualifiedCTE(c)+
		"SELECT CASE WHEN dbcc_sp.v IS NOT NULL THEN pg_get_viewdef($1::int8::oid, true) END FROM dbcc_sp", oid).Scan(&def)
	return strings.TrimRight(strings.TrimSpace(def), ";"), err
}

// relInfo locates a relation by schema and name.
type relInfo struct {
	oid              int64
	relkind          string
	qualified        string // "schema"."name"
	relopts, spc, am string
	populated        bool
	comment          string
}

func (p *Postgres) findRel(ctx context.Context, c dialect.Conn, schema, name string, kinds string) (*relInfo, error) {
	r := &relInfo{qualified: p.Qualify(meta.Path{Schema: schema, Object: name})}
	err := c.QueryRowContext(ctx, `SELECT c.oid::int8, c.relkind::text, COALESCE(array_to_string(c.reloptions, ', '), ''),
			COALESCE(ts.spcname, ''), COALESCE(am.amname, ''), c.relispopulated, COALESCE(obj_description(c.oid, 'pg_class'), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
		LEFT JOIN pg_am am ON am.oid = c.relam
		WHERE n.nspname = $1 AND c.relname = $2 AND strpos($3, c.relkind::text) > 0`, schema, name, kinds).
		Scan(&r.oid, &r.relkind, &r.relopts, &r.spc, &r.am, &r.populated, &r.comment)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", meta.ErrNotFound, r.qualified)
	}
	return r, err
}

// ObjectSource returns the source of an object as a CREATE statement
// without a trailing semicolon:
//
//   - view: CREATE VIEW s.v [WITH (options)] AS <pg_get_viewdef>
//   - materialized view: CREATE MATERIALIZED VIEW s.v [USING am] [WITH (…)]
//     [TABLESPACE t] AS <def> WITH [NO] DATA
//   - function, procedure: pg_get_functiondef (CREATE OR REPLACE …); all
//     overloads of the name, separated by ";\n\n"
//   - trigger: pg_get_triggerdef; triggers of that name on several tables
//     are separated by ";\n"
//   - sequence: CREATE SEQUENCE … reconstructed from pg_sequence
//
// Names in view and trigger definitions are schema-qualified
// (qualifiedCTE). Tables have no source (spec 4: no DDL reconstruction).
func (p *Postgres) ObjectSource(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (string, error) {
	if _, err := p.checkDatabase(ctx, c, obj.Database); err != nil {
		return "", err
	}
	schema, err := p.schemaOf(ctx, c, obj)
	if err != nil {
		return "", err
	}
	q := p.Qualify(meta.Path{Schema: schema, Object: obj.Object})
	switch kind {
	case meta.KindView, meta.KindMaterializedView:
		kinds := "v"
		if kind == meta.KindMaterializedView {
			kinds = "m"
		}
		r, err := p.findRel(ctx, c, schema, obj.Object, kinds)
		if err != nil {
			return "", err
		}
		def, err := p.viewDef(ctx, c, r.oid)
		if err != nil {
			return "", err
		}
		with := ""
		if r.relopts != "" {
			with = " WITH (" + r.relopts + ")"
		}
		if kind == meta.KindView {
			return "CREATE VIEW " + q + with + " AS\n" + def, nil
		}
		var b strings.Builder
		b.WriteString("CREATE MATERIALIZED VIEW " + q)
		if r.am != "" && r.am != "heap" {
			b.WriteString(" USING " + p.QuoteIdent(r.am))
		}
		b.WriteString(with)
		if r.spc != "" {
			b.WriteString(" TABLESPACE " + p.QuoteIdent(r.spc))
		}
		b.WriteString(" AS\n" + def + "\nWITH ")
		if !r.populated {
			b.WriteString("NO ")
		}
		b.WriteString("DATA")
		return b.String(), nil
	case meta.KindFunction, meta.KindProcedure:
		defs, err := p.routineDefs(ctx, c, schema, obj.Object, kind)
		if err != nil {
			return "", err
		}
		return strings.Join(defs, ";\n\n"), nil
	case meta.KindTrigger:
		trs, err := p.triggerDefs(ctx, c, schema, obj.Object)
		if err != nil {
			return "", err
		}
		var defs []string
		for _, tr := range trs {
			defs = append(defs, tr.def)
		}
		return strings.Join(defs, ";\n"), nil
	case meta.KindSequence:
		return p.sequenceSource(ctx, c, schema, obj.Object)
	case meta.KindTable, "":
		return "", fmt.Errorf("postgres: tables have no source text; use the table description")
	}
	return "", fmt.Errorf("postgres has no source for kind %q", kind)
}

func (p *Postgres) routineDefs(ctx context.Context, c dialect.Conn, schema, name string, kind meta.ObjectKind) ([]string, error) {
	prokind := "f"
	if kind == meta.KindProcedure {
		prokind = "p"
	}
	rows, err := c.QueryContext(ctx, `SELECT pg_get_functiondef(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname = $2 AND p.prokind = $3
		ORDER BY pg_get_function_identity_arguments(p.oid)`, schema, name, prokind)
	if err != nil {
		return nil, err
	}
	defs, err := scanStrings(rows)
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("%w: %s %s", meta.ErrNotFound, kind, p.Qualify(meta.Path{Schema: schema, Object: name}))
	}
	for i := range defs {
		defs[i] = strings.TrimSpace(defs[i])
	}
	return defs, nil
}

type triggerDef struct {
	def, table string // table: qualified, quoted
	constraint bool
}

func (p *Postgres) triggerDefs(ctx context.Context, c dialect.Conn, schema, name string) ([]triggerDef, error) {
	rows, err := c.QueryContext(ctx, p.qualifiedCTE(c)+`SELECT CASE WHEN dbcc_sp.v IS NOT NULL THEN pg_get_triggerdef(t.oid, true) END,
			quote_ident(n.nspname) || '.' || quote_ident(c.relname), t.tgconstraint <> 0
		FROM dbcc_sp, pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND t.tgname = $2 AND NOT t.tgisinternal
		ORDER BY c.relname`, schema, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []triggerDef
	for rows.Next() {
		var d triggerDef
		if err := rows.Scan(&d.def, &d.table, &d.constraint); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: trigger %s", meta.ErrNotFound, p.Qualify(meta.Path{Schema: schema, Object: name}))
	}
	return out, nil
}

func (p *Postgres) sequenceSource(ctx context.Context, c dialect.Conn, schema, name string) (string, error) {
	var typ, owned string
	var start, inc, min, max, cache int64
	var cycle bool
	err := c.QueryRowContext(ctx, `SELECT format_type(s.seqtypid, NULL), s.seqstart, s.seqincrement, s.seqmin, s.seqmax, s.seqcache, s.seqcycle,
			COALESCE((SELECT quote_ident(tn.nspname) || '.' || quote_ident(tc.relname) || '.' || quote_ident(a.attname)
				FROM pg_depend d JOIN pg_class tc ON tc.oid = d.refobjid JOIN pg_namespace tn ON tn.oid = tc.relnamespace
				JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
				WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.refclassid = 'pg_class'::regclass
				  AND d.deptype IN ('a', 'i') LIMIT 1), '')
		FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&typ, &start, &inc, &min, &max, &cache, &cycle, &owned)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%w: sequence %s", meta.ErrNotFound, p.Qualify(meta.Path{Schema: schema, Object: name}))
	}
	if err != nil {
		return "", err
	}
	s := fmt.Sprintf("CREATE SEQUENCE %s AS %s INCREMENT BY %d MINVALUE %d MAXVALUE %d START WITH %d CACHE %d",
		p.Qualify(meta.Path{Schema: schema, Object: name}), typ, inc, min, max, start, cache)
	if cycle {
		s += " CYCLE"
	}
	if owned != "" {
		s += " OWNED BY " + owned
	}
	return s, nil
}
