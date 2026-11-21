-- Rich test schema for the MSSQL dialect (integration tests).
-- Runs through sqltext.NewMSSQL() batch by batch in database dbcc_it_dialect.

CREATE SCHEMA sales AUTHORIZATION dbo;
GO
CREATE TYPE dbo.code_t FROM varchar(10) NOT NULL;
GO
CREATE SEQUENCE sales.order_no AS bigint START WITH 1000 INCREMENT BY 10 MINVALUE 1000 MAXVALUE 999999 NO CYCLE CACHE 20;
GO

CREATE TABLE dbo.parent (
    a     int          NOT NULL,
    b     nvarchar(20) COLLATE Latin1_General_CI_AS NOT NULL,
    label nvarchar(40) NULL,
    CONSTRAINT PK_parent PRIMARY KEY CLUSTERED (a, b)
);
GO

CREATE TABLE sales.items (
    id          int IDENTITY(100, 5) NOT NULL,
    code        dbo.code_t,
    name        nvarchar(80)  NOT NULL CONSTRAINT DF_items_name DEFAULT (N'unnamed'),
    note        nvarchar(max) NULL,
    no_default  varchar(10)   NOT NULL,
    price       decimal(10,2) NOT NULL CONSTRAINT DF_items_price DEFAULT ((0)),
    qty         smallint      NOT NULL DEFAULT 1,
    total       AS (price * qty) PERSISTED,
    label       AS (UPPER(name)),
    created     datetime2(3)  NOT NULL DEFAULT (SYSUTCDATETIME()),
    rate        float         NULL DEFAULT (-1.5),
    small       real          NULL,
    tiny        tinyint       NULL,
    flag        bit           NOT NULL DEFAULT 0,
    guid        uniqueidentifier NULL DEFAULT NEWID(),
    bin         varbinary(16) NULL,
    ver         rowversion,
    parent_a    int           NULL,
    parent_b    nvarchar(20)  COLLATE Latin1_General_CI_AS NULL,
    order_no    bigint        NULL DEFAULT (NEXT VALUE FOR sales.order_no),
    status      varchar(10)   NOT NULL DEFAULT 'new',
    CONSTRAINT PK_items PRIMARY KEY NONCLUSTERED (id DESC),
    CONSTRAINT UQ_items_code UNIQUE (code),
    CONSTRAINT FK_items_parent FOREIGN KEY (parent_a, parent_b) REFERENCES dbo.parent (a, b)
        ON DELETE CASCADE ON UPDATE NO ACTION,
    CONSTRAINT CK_items_qty CHECK (qty >= 0),
    CONSTRAINT CK_items_price_qty CHECK (price >= 0 AND qty < 1000),
    CONSTRAINT CK_items_status CHECK (status IN ('new', 'active', 'done'))
);
GO
CREATE CLUSTERED INDEX CIX_items_created ON sales.items (created);
CREATE UNIQUE INDEX UX_items_name ON sales.items (name, status DESC);
CREATE INDEX IX_items_price ON sales.items (price DESC) INCLUDE (name, qty) WHERE (qty > 0) WITH (FILLFACTOR = 80);
CREATE UNIQUE INDEX UX_items_guid ON sales.items (guid) WHERE guid IS NOT NULL;
ALTER TABLE sales.items NOCHECK CONSTRAINT CK_items_price_qty;
GO

EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'Items for sale',
    @level0type = N'SCHEMA', @level0name = N'sales', @level1type = N'TABLE', @level1name = N'items';
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'display name',
    @level0type = N'SCHEMA', @level0name = N'sales', @level1type = N'TABLE', @level1name = N'items',
    @level2type = N'COLUMN', @level2name = N'name';
GO

-- Trigger with comments before CREATE
CREATE TRIGGER sales.trg_items_ins ON sales.items
AFTER INSERT, UPDATE
AS
BEGIN
    SET NOCOUNT ON;
    UPDATE i SET note = COALESCE(i.note, N'auto')
    FROM sales.items i JOIN inserted x ON x.id = i.id
    WHERE i.note IS NULL AND i.status = 'trigger';
END
GO

CREATE TABLE dbo.logs (
    id         int NOT NULL CONSTRAINT PK_logs PRIMARY KEY,
    msg        nvarchar(100) NULL,
    valid_from datetime2 GENERATED ALWAYS AS ROW START NOT NULL,
    valid_to   datetime2 GENERATED ALWAYS AS ROW END NOT NULL,
    PERIOD FOR SYSTEM_TIME (valid_from, valid_to)
) WITH (SYSTEM_VERSIONING = ON (HISTORY_TABLE = dbo.logs_history));
GO

CREATE VIEW sales.v_items
AS
SELECT id, name, price * qty AS amount
FROM sales.items
WHERE status <> 'done'
WITH CHECK OPTION;
GO

/* A procedure with a block comment before CREATE */
CREATE PROCEDURE sales.calc_total
    @min_qty int = 0
AS
BEGIN
    SET NOCOUNT ON;
    SELECT SUM(total) AS total FROM sales.items WHERE qty >= @min_qty;

    SELECT COUNT(*) AS n FROM sales.items;
END
GO

CREATE FUNCTION dbo.add_tax (@amount decimal(10,2))
RETURNS decimal(10,2)
AS
BEGIN
    RETURN @amount * 1.19;
END
GO

CREATE FUNCTION sales.items_over (@min decimal(10,2))
RETURNS TABLE
AS
RETURN (SELECT id, name, price FROM sales.items WHERE price > @min);
GO

CREATE FUNCTION sales.item_names ()
RETURNS @out TABLE (name nvarchar(80))
AS
BEGIN
    INSERT INTO @out SELECT name FROM sales.items;
    RETURN;
END
GO
