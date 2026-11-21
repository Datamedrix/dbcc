-- Test schema for the PostgreSQL dialect integration tests. Loaded through
-- the project's own splitter (sqltext.NewPostgres) into a fresh database.
-- Objects live in schemas "app" and "public".

CREATE SCHEMA app;
COMMENT ON SCHEMA app IS 'application schema';

CREATE TYPE app.status AS ENUM ('new', 'active', 'it''s done');

CREATE TABLE app.parent (
  a     integer     NOT NULL,
  b     varchar(20) NOT NULL,
  label text,
  PRIMARY KEY (a, b)
);
COMMENT ON TABLE app.parent IS 'parent of items';
COMMENT ON COLUMN app.parent.label IS 'free text';

CREATE SEQUENCE app.ticket_seq AS integer START WITH 100 INCREMENT BY 5 CACHE 1;

CREATE TABLE app.items (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  seq_no     serial,
  code       varchar(20)   NOT NULL,
  region     char(2)       NOT NULL DEFAULT 'EU',
  name       text COLLATE "C" NOT NULL,
  note       varchar(40)   DEFAULT 'NULL',
  nothing    varchar(40)   DEFAULT NULL,
  quote_str  varchar(10)   NOT NULL DEFAULT 'it''s',
  price      numeric(10,2) NOT NULL DEFAULT 0.00,
  qty        integer       NOT NULL DEFAULT 1 CONSTRAINT chk_qty CHECK (qty > 0 AND qty < 100000),
  ratio      double precision DEFAULT -1.5,
  total      numeric(12,2) GENERATED ALWAYS AS (price * qty) STORED,
  status     app.status    NOT NULL DEFAULT 'new',
  tags       text[],
  doc        jsonb,
  bin        bytea,
  uid        uuid          NOT NULL DEFAULT gen_random_uuid(),
  ticket     integer       DEFAULT nextval('app.ticket_seq'),
  created    timestamptz   NOT NULL DEFAULT now(),
  day        date,
  small      smallint,
  parent_a   integer,
  parent_b   varchar(20),
  CONSTRAINT uq_code_region UNIQUE (code, region),
  CONSTRAINT chk_code CHECK (code <> '' AND code !~ ';'),
  CONSTRAINT fk_parent FOREIGN KEY (parent_a, parent_b) REFERENCES app.parent (a, b)
    ON UPDATE RESTRICT ON DELETE CASCADE
);
COMMENT ON TABLE app.items IS 'items with everything';
COMMENT ON COLUMN app.items.id IS 'surrogate key';

CREATE INDEX ix_items_price_desc ON app.items (price DESC NULLS LAST, qty);
CREATE INDEX ix_items_lower_name ON app.items (lower(name));
CREATE INDEX ix_items_active ON app.items (code varchar_pattern_ops) INCLUDE (qty) WHERE status = 'active';
CREATE INDEX ix_items_doc ON app.items USING gin (doc jsonb_path_ops);
CREATE UNIQUE INDEX uq_items_uid ON app.items (uid);

CREATE FUNCTION app.trg_items_touch() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  -- a semicolon; and 'quotes' inside the body
  IF NEW.note IS NULL THEN
    NEW.note := 'touched;';
  END IF;
  RETURN NEW;
END;
$fn$;

CREATE TRIGGER trg_items_bi BEFORE INSERT OR UPDATE OF note ON app.items
  FOR EACH ROW WHEN (NEW.qty > 0) EXECUTE FUNCTION app.trg_items_touch();
CREATE TRIGGER trg_items_trunc AFTER TRUNCATE ON app.items
  FOR EACH STATEMENT EXECUTE FUNCTION app.trg_items_touch();
ALTER TABLE app.items DISABLE TRIGGER trg_items_trunc;

CREATE VIEW app.v_items WITH (security_barrier) AS
  SELECT i.id, i.code, p.label FROM app.items i LEFT JOIN app.parent p ON p.a = i.parent_a AND p.b = i.parent_b
  WHERE i.qty > 0;
COMMENT ON VIEW app.v_items IS 'items with labels';

CREATE MATERIALIZED VIEW app.mv_totals AS
  SELECT code, sum(total) AS total FROM app.items GROUP BY code
WITH NO DATA;
CREATE UNIQUE INDEX ix_mv_totals_code ON app.mv_totals (code);
COMMENT ON MATERIALIZED VIEW app.mv_totals IS 'totals';

CREATE FUNCTION app.f_double(x integer) RETURNS integer LANGUAGE sql IMMUTABLE AS $$ SELECT x * 2 $$;
CREATE FUNCTION app.f_double(x numeric) RETURNS numeric LANGUAGE sql IMMUTABLE AS $$ SELECT x * 2 $$;

CREATE PROCEDURE app.p_count(INOUT n bigint DEFAULT NULL) LANGUAGE plpgsql AS $$
BEGIN
  SELECT count(*) INTO n FROM app.items;
END
$$;

CREATE FUNCTION app.f_atomic(a integer) RETURNS integer LANGUAGE sql
BEGIN ATOMIC
  SELECT CASE WHEN a > 0 THEN a END;
END;

CREATE TABLE app.logs (
  id      bigint      NOT NULL,
  created date        NOT NULL,
  msg     text,
  PRIMARY KEY (id, created)
) PARTITION BY RANGE (created);
CREATE TABLE app.logs_2023 PARTITION OF app.logs FOR VALUES FROM ('2023-01-01') TO ('2024-01-01');
CREATE TABLE app.logs_2024 PARTITION OF app.logs FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');
CREATE TABLE app.logs_default PARTITION OF app.logs DEFAULT;

CREATE TABLE public.plain (id integer PRIMARY KEY, val text);
