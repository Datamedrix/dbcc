-- MySQL-only additions (MariaDB has no functional key parts).
ALTER TABLE items ADD INDEX ix_func ((LOWER(code)), region);
