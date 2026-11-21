package postgres

import (
	"sort"
	"strings"
)

// Keywords returns the SQL keywords offered by completion (spec 9.2), sorted.
func (p *Postgres) Keywords() []string { return words(pgKeywords) }

// Functions returns the built-in function names offered by completion, sorted.
func (p *Postgres) Functions() []string { return words(pgFunctions) }

func words(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.Fields(s) {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

const pgKeywords = `
ABORT ABSOLUTE ACCESS ACTION ADD ADMIN AFTER AGGREGATE ALL ALSO ALTER ALWAYS ANALYZE AND ANY ARRAY AS ASC
ASSERTION ASSIGNMENT ASYMMETRIC AT ATOMIC ATTACH ATTRIBUTE AUTHORIZATION BACKWARD BEFORE BEGIN BETWEEN BIGINT
BIGSERIAL BINARY BIT BOOLEAN BOTH BREADTH BY BYTEA CACHE CALL CALLED CASCADE CASCADED CASE CAST CATALOG CHAIN
CHAR CHARACTER CHARACTERISTICS CHECK CHECKPOINT CLASS CLOSE CLUSTER COALESCE COLLATE COLLATION COLUMN COLUMNS
COMMENT COMMENTS COMMIT COMMITTED COMPRESSION CONCURRENTLY CONFIGURATION CONFLICT CONNECTION CONSTRAINT
CONSTRAINTS CONTENT CONTINUE CONVERSION COPY COST CREATE CROSS CSV CUBE CURRENT CURRENT_CATALOG CURRENT_DATE
CURRENT_ROLE CURRENT_SCHEMA CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER CURSOR CYCLE DATA DATABASE DATE DAY
DEALLOCATE DEC DECIMAL DECLARE DEFAULT DEFAULTS DEFERRABLE DEFERRED DEFINER DELETE DELIMITER DEPENDS DEPTH DESC
DETACH DICTIONARY DISABLE DISCARD DISTINCT DO DOCUMENT DOMAIN DOUBLE DROP EACH ELSE ENABLE ENCODING ENCRYPTED END
ENUM ESCAPE EVENT EXCEPT EXCLUDE EXCLUDING EXCLUSIVE EXECUTE EXISTS EXPLAIN EXPRESSION EXTENSION EXTERNAL EXTRACT
FALSE FAMILY FETCH FILTER FINALIZE FIRST FLOAT FOLLOWING FOR FORCE FOREIGN FORWARD FREEZE FROM FULL FUNCTION
FUNCTIONS GENERATED GLOBAL GRANT GRANTED GREATEST GROUP GROUPING GROUPS HANDLER HAVING HEADER HOLD HOUR IDENTITY
IF ILIKE IMMEDIATE IMMUTABLE IMPLICIT IMPORT IN INCLUDE INCLUDING INCREMENT INDEX INDEXES INHERIT INHERITS
INITIALLY INLINE INNER INOUT INPUT INSENSITIVE INSERT INSTEAD INT INTEGER INTERSECT INTERVAL INTO INVOKER IS
ISNULL ISOLATION JOIN JSON JSONB KEY LABEL LANGUAGE LARGE LAST LATERAL LEADING LEAKPROOF LEAST LEFT LEVEL LIKE
LIMIT LISTEN LOAD LOCAL LOCALTIME LOCALTIMESTAMP LOCATION LOCK LOCKED LOGGED MAPPING MATCH MATCHED MATERIALIZED
MAXVALUE MERGE METHOD MINUTE MINVALUE MODE MONTH MOVE NAME NAMES NATIONAL NATURAL NCHAR NEW NEXT NFC NO NONE
NORMALIZE NOT NOTHING NOTIFY NOTNULL NOWAIT NULL NULLIF NULLS NUMERIC OBJECT OF OFF OFFSET OIDS OLD ON ONLY
OPERATOR OPTION OPTIONS OR ORDER ORDINALITY OTHERS OUT OUTER OVER OVERLAPS OVERLAY OVERRIDING OWNED OWNER
PARALLEL PARAMETER PARSER PARTIAL PARTITION PASSING PASSWORD PLACING PLANS POLICY POSITION PRECEDING PRECISION
PREPARE PREPARED PRESERVE PRIMARY PRIOR PRIVILEGES PROCEDURAL PROCEDURE PROCEDURES PROGRAM PUBLICATION QUOTE
RANGE READ REAL REASSIGN RECHECK RECURSIVE REF REFERENCES REFERENCING REFRESH REINDEX RELATIVE RELEASE RENAME
REPEATABLE REPLACE REPLICA RESET RESTART RESTRICT RETURN RETURNING RETURNS REVOKE RIGHT ROLE ROLLBACK ROLLUP
ROUTINE ROUTINES ROW ROWS RULE SAVEPOINT SCHEMA SCHEMAS SCROLL SEARCH SECOND SECURITY SELECT SEQUENCE SEQUENCES
SERIAL SERIALIZABLE SERVER SESSION SESSION_USER SET SETOF SETS SHARE SHOW SIMILAR SIMPLE SKIP SMALLINT SNAPSHOT
SOME SQL STABLE STANDALONE START STATEMENT STATISTICS STDIN STDOUT STORAGE STORED STRICT STRIP SUBSCRIPTION
SUBSTRING SUPPORT SYMMETRIC SYSID SYSTEM SYSTEM_USER TABLE TABLES TABLESAMPLE TABLESPACE TEMP TEMPLATE TEMPORARY
TEXT THEN TIES TIME TIMESTAMP TIMESTAMPTZ TO TRAILING TRANSACTION TRANSFORM TREAT TRIGGER TRIM TRUE TRUNCATE
TRUSTED TYPE TYPES UESCAPE UNBOUNDED UNCOMMITTED UNENCRYPTED UNION UNIQUE UNKNOWN UNLISTEN UNLOGGED UNTIL UPDATE
USER USING UUID VACUUM VALID VALIDATE VALIDATOR VALUE VALUES VARCHAR VARIADIC VARYING VERBOSE VERSION VIEW VIEWS
VOLATILE WHEN WHERE WHITESPACE WINDOW WITH WITHIN WITHOUT WORK WRAPPER WRITE XML YEAR YES ZONE`

const pgFunctions = `
abs age array_agg array_append array_cat array_length array_position array_remove array_to_json
array_to_string avg bit_length bool_and bool_or btrim cardinality cbrt ceil ceiling char_length
character_length chr clock_timestamp coalesce concat concat_ws corr count cume_dist current_database
current_date current_schema current_setting current_time current_timestamp current_user date_bin date_part
date_trunc decode degrees dense_rank div encode every exp extract first_value floor format format_type
gen_random_uuid generate_series generate_subscripts greatest has_table_privilege initcap isfinite
json_agg json_array_elements json_build_array json_build_object json_each json_extract_path json_object_agg
json_typeof jsonb_agg jsonb_array_elements jsonb_build_array jsonb_build_object jsonb_each jsonb_extract_path
jsonb_insert jsonb_object_agg jsonb_path_query jsonb_pretty jsonb_set jsonb_strip_nulls jsonb_typeof justify_days
justify_hours lag last_value lead least left length ln localtime localtimestamp log lower lpad ltrim make_date
make_interval make_time make_timestamp make_timestamptz max md5 min mod now nextval nth_value ntile nullif
octet_length overlay percent_rank percentile_cont percentile_disc pg_backend_pid pg_cancel_backend
pg_column_size pg_database_size pg_get_functiondef pg_get_viewdef pg_indexes_size pg_relation_size pg_size_pretty
pg_sleep pg_table_size pg_terminate_backend pg_total_relation_size pg_typeof pi position power quote_ident
quote_literal quote_nullable radians random rank regexp_count regexp_like regexp_match regexp_matches
regexp_replace regexp_split_to_array regexp_split_to_table repeat replace reverse right round row_number
row_to_json rpad rtrim setval sha256 sign sin split_part sqrt starts_with statement_timestamp stddev
string_agg string_to_array strpos substr substring sum timeofday to_char to_date to_json to_jsonb to_number
to_regclass to_timestamp translate trim trunc unnest upper uuid_generate_v4 variance version width_bucket
xmlagg`
