-- Test schema for the MySQL/MariaDB dialect integration tests.
-- Loaded through the project's own splitter (sqltext.NewMySQL) into a fresh
-- database that is the session default; object names are unqualified.

CREATE TABLE parent (
  a     INT         NOT NULL,
  b     VARCHAR(20) NOT NULL,
  label VARCHAR(40) NULL DEFAULT NULL COMMENT 'free text',
  PRIMARY KEY (a, b)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='parent of items';

CREATE TABLE items (
  id         INT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'surrogate key',
  code       VARCHAR(20)  NOT NULL,
  region     CHAR(2)      NOT NULL DEFAULT 'EU',
  name       VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  note       VARCHAR(40)  NULL DEFAULT 'NULL' COMMENT 'string default NULL',
  nothing    VARCHAR(40)  NULL DEFAULT NULL,
  no_default VARCHAR(40)  NOT NULL,
  empty_str  VARCHAR(10)  NOT NULL DEFAULT '',
  quote_str  VARCHAR(10)  NOT NULL DEFAULT 'it''s',
  price      DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  qty        INT          NOT NULL DEFAULT 1 CHECK (qty < 100000),
  ratio      DOUBLE       NULL DEFAULT -1.5,
  total      DECIMAL(12,2) GENERATED ALWAYS AS (price * qty) STORED,
  name_upper VARCHAR(200) GENERATED ALWAYS AS (UPPER(name)) VIRTUAL,
  status     ENUM('new','active','it''s done') NOT NULL DEFAULT 'new',
  tags       SET('a','b','c') NULL,
  uid        VARCHAR(36)  NOT NULL DEFAULT (UUID()),
  created    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  small      TINYINT(1)   NOT NULL DEFAULT 0,
  data       BLOB         NULL,
  bin        VARBINARY(16) NULL,
  doc        JSON         NULL,
  parent_a   INT          NULL,
  parent_b   VARCHAR(20)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_code_region (code, region),
  KEY ix_name_prefix (name(10)),
  KEY ix_price_desc (price DESC, qty),
  KEY ix_total (total),
  FULLTEXT KEY ft_name (name),
  KEY ix_parent (parent_a, parent_b),
  CONSTRAINT fk_items_parent FOREIGN KEY (parent_a, parent_b) REFERENCES parent (a, b)
    ON DELETE CASCADE ON UPDATE RESTRICT,
  CONSTRAINT chk_price CHECK (price >= 0),
  CONSTRAINT chk_code CHECK (code <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='items with everything';

CREATE TABLE logs (
  id      INT          NOT NULL,
  created DATE         NOT NULL,
  msg     VARCHAR(100) NULL,
  PRIMARY KEY (id, created)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
PARTITION BY RANGE (YEAR(created)) (
  PARTITION p2023 VALUES LESS THAN (2024),
  PARTITION p2024 VALUES LESS THAN (2025),
  PARTITION pmax  VALUES LESS THAN MAXVALUE
);

CREATE VIEW v_items AS
  SELECT id, code, price * qty AS amount FROM items WHERE status <> 'new';

DELIMITER $$
CREATE TRIGGER trg_items_bi BEFORE INSERT ON items FOR EACH ROW
BEGIN
  IF NEW.code = '-' THEN
    SET NEW.code = 'X';
  END IF;
END$$

CREATE TRIGGER trg_items_au AFTER UPDATE ON items FOR EACH ROW
  INSERT INTO logs (id, created, msg) VALUES (NEW.id, CURRENT_DATE, 'updated')$$

-- The body contains "$$" in a string so the replace script must pick another delimiter.
CREATE PROCEDURE p_count(IN p_region CHAR(2), OUT n INT)
  COMMENT 'count items of a region'
BEGIN
  SET @dbcc_marker = '$$';
  SELECT COUNT(*) INTO n FROM items WHERE region = p_region;
END$$

CREATE FUNCTION f_double(x INT) RETURNS INT DETERMINISTIC
  RETURN x * 2$$

CREATE EVENT ev_cleanup ON SCHEDULE EVERY 1 DAY DISABLE
  DO DELETE FROM logs WHERE created < '2000-01-01'$$
DELIMITER ;
