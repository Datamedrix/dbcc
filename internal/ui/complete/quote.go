package complete

import (
	"strings"

	"dbcc/internal/dialect"
)

// reservedWords is the union of the reserved words of MySQL/MariaDB,
// PostgreSQL and SQL Server that commonly collide with object names.
// Quoting a name that one of the other dialects reserves is harmless; not
// quoting a reserved one breaks the statement.
var reservedWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
ADD ALL ALTER ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC AUTHORIZATION BACKUP BEFORE BEGIN
BETWEEN BOTH BREAK BROWSE BULK BY CALL CASCADE CASE CAST CHANGE CHECK CHECKPOINT CLOSE CLUSTERED
COLLATE COLUMN COMMIT COMPUTE CONDITION CONSTRAINT CONTAINS CONTINUE CONVERT CREATE CROSS CURRENT
CURRENT_DATE CURRENT_ROLE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER CURSOR DATABASE DATABASES DBCC
DEALLOCATE DECLARE DEFAULT DEFERRABLE DELAYED DELETE DENY DESC DESCRIBE DISTINCT DISTINCTROW DIV DO
DROP DUAL DUMP EACH ELSE ELSEIF END ESCAPE EXCEPT EXEC EXECUTE EXISTS EXIT EXPLAIN FALSE FETCH FILE
FILLFACTOR FOR FORCE FOREIGN FREETEXT FROM FULL FULLTEXT FUNCTION GENERATED GOTO GRANT GROUP GROUPS
HAVING HOLDLOCK IDENTITY IF IGNORE IN INDEX INITIALLY INNER INOUT INSERT INTERSECT INTERVAL INTO IS
ITERATE JOIN KEY KEYS KILL LATERAL LEADING LEAVE LEFT LIKE LIMIT LINENO LINES LOAD LOCALTIME
LOCALTIMESTAMP LOCK LOOP MATCH MERGE MOD NATURAL NOCHECK NONCLUSTERED NOT NULL OF OFF OFFSET ON ONLY
OPEN OPTION OPTIONALLY OR ORDER OUT OUTER OUTFILE OVER PARTITION PERCENT PIVOT PLACING PLAN PRECISION
PRIMARY PRINT PROC PROCEDURE PURGE RAISERROR RANGE READ READS READTEXT RECONFIGURE RECURSIVE
REFERENCES REGEXP RELEASE RENAME REPEAT REPLACE REQUIRE RESIGNAL RESTORE RESTRICT RETURN RETURNING
REVOKE RIGHT RLIKE ROLLBACK ROW ROWCOUNT ROWS RULE SAVE SCHEMA SCHEMAS SELECT SESSION_USER SET SETUSER
SHOW SHUTDOWN SIGNAL SIMILAR SOME SPATIAL SQL STATISTICS SYMMETRIC SYSTEM_USER TABLE TABLESAMPLE
TEXTSIZE THEN TO TOP TRAILING TRAN TRANSACTION TRIGGER TRUE TRUNCATE UNDO UNION UNIQUE UNLOCK UNPIVOT
UPDATE UPDATETEXT USAGE USE USER USING VALUES VARIADIC VIEW WAITFOR WHEN WHERE WHILE WINDOW WITH
WRITE WRITETEXT XOR`) {
		m[w] = true
	}
	return m
}()

// IsReserved reports whether word is a reserved word in any supported
// dialect (case-insensitive).
func IsReserved(word string) bool { return reservedWords[strings.ToUpper(word)] }

// Optional dialect refinements, used when a dialect implements them.
type (
	needsQuoter interface{ NeedsQuote(name string) bool }
	reserver    interface{ IsReserved(word string) bool }
)

// NeedsQuote reports whether name must be quoted in SQL for dialect d:
// when it is not a plain identifier (ASCII letter or '_' first, then
// letters, digits, '_' or '$') or it is a reserved word. On dialects with
// ANSI double-quote quoting (standard case folding, PostgreSQL) names with
// upper-case letters are quoted too. A dialect can override the rule with
// a NeedsQuote(name string) bool method, or the reserved list with
// IsReserved(word string) bool.
func NeedsQuote(d dialect.Dialect, name string) bool {
	if d != nil {
		if q, ok := d.(needsQuoter); ok {
			return q.NeedsQuote(name)
		}
	}
	if name == "" {
		return true
	}
	upper := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z'):
		case c >= 'A' && c <= 'Z':
			upper = true
		case i > 0 && (c >= '0' && c <= '9' || c == '$'):
		default:
			return true
		}
	}
	if d != nil {
		if r, ok := d.(reserver); ok {
			if r.IsReserved(name) {
				return true
			}
		} else if IsReserved(name) {
			return true
		}
		if upper && strings.HasPrefix(d.QuoteIdent("x"), `"`) {
			return true
		}
		return false
	}
	return IsReserved(name)
}

// QuoteIfNeeded returns name, quoted with d.QuoteIdent only when
// NeedsQuote says so (spec 9.2: identifiers are quoted only when
// necessary).
func QuoteIfNeeded(d dialect.Dialect, name string) string {
	if d == nil || !NeedsQuote(d, name) {
		return name
	}
	return d.QuoteIdent(name)
}

// quoteArg quotes a command-line argument for command.Split when it
// contains blanks, quotes or backslashes (like ui.QuoteArg).
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
