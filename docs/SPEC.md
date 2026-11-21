# dbcc — Database Command & Control

Specification v1.4 · 2026-11-21 · A terminal database client. Product name: **dbcc**, short for *Database Command & Control*. `dbcc` is also the name of the binary, the Go module, the configuration directory and the Lua namespace.

Changes in v1.4: the tool is named dbcc (sections 3.7, 7.1, 8, 17); every occurrence of the former working title is replaced.

Changes in v1.3: the owner confirmed assumptions A1–A4, A12, A13, A18 and A19 (section 17).

Changes in v1.2: object tree specified in detail (sections 3.8, 7.2); editor tabs and the `edit` command (sections 8, 9.1, 13).

Changes in v1.1: every command is a Lua script (sections 3.2, 6, 8, 13); splash screen and About page (sections 3.7, 7.1); milestones reordered (section 16).

## 0. How to use this document

- Sections 1–3 are decisions made by the owner. Do not deviate without asking.
- Section 17 lists assumptions. Those marked *confirmed* are decisions with the same weight as sections 1–3. Those marked *default* may be implemented as written. Those marked *open* need the owner's input before the milestone named there.
- Implement milestone by milestone (section 16). Each milestone has acceptance criteria.
- Library facts were checked in November 2026. Verify current stable versions before pinning them.

## 1. Purpose

A single-binary, keyboard-driven, full-screen terminal database client written in Go, for the owner's personal use. It combines:

- shell-style commands for database objects (`cd`, `ll`, `cp`, `cat`, …),
- a SQL editor with syntax highlighting and on-demand completion,
- a script runner,
- a result grid with horizontal scrolling, sorting, filtering and a pivoted view,
- a form for inserting and editing rows with foreign-key lookup,
- a command layer written entirely in Lua, which the owner can extend and change.

## 2. Hard requirements

| # | Requirement |
|---|---|
| R1 | One static binary, no runtime dependencies, no helper programs, no extra installation. `CGO_ENABLED=0`. |
| R2 | Runs inside an SSH session. Pure terminal UI, no window system. |
| R3 | Databases: MySQL, MariaDB, PostgreSQL, Microsoft SQL Server. |
| R4 | Build target for v1: Linux amd64 only. |
| R5 | UI library: `rivo/tview` (on `tcell` v2). |
| R6 | Query results never wrap. Wide results scroll horizontally. |
| R7 | Everything reachable by keyboard. Shortcuts are discoverable through an always-visible footer and a help overlay. |

## 3. Decisions

### 3.1 Product

| Topic                | Decision                                                                                                                          |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| Audience             | Owner only in v1. No multi-user concerns, no plugin marketplace, no telemetry. The tool is branded as a Datamedrix product (3.7). |
| Priority of dialects | MySQL and MariaDB first and complete. PostgreSQL next. MSSQL last. The dialect interface exists from the first commit.            |
| Start screen         | Full-screen layout: object tree, editor, result area, command line, status line, F-key footer.                                    |
| Navigation model     | Paths of the form `/connection/database/schema`. MySQL and MariaDB have no schema level. `cd ..` works.                           |
| No `rm`              | There is no delete or drop command. Deleting happens through SQL only.                                                            |

### 3.2 Commands

| Topic | Decision |
|---|---|
| `cd` | Changes the current path. On PostgreSQL a database change reconnects transparently. |
| `ll <table\|view>` | `SELECT * FROM <object>`, opened in the result grid. |
| `cp <src> <new>` | Data-only copy: `CREATE TABLE <new> AS SELECT * FROM <src>`. |
| `cp -a <src> <new>` | Copy with structure (columns, defaults, primary key, indexes, checks) plus data. |
| `cat <table>` | Structured description in the style of MySQL `EXPLAIN <table>` / `DESCRIBE`, extended with **all** definitions and constraints. Not a DDL statement. |
| `cat <view\|procedure\|function>` | Source code with syntax highlighting. |
| `explain` | Not a special command. It stays plain SQL (query plan). |
| Additional commands in v1 | `ls`, `pwd`, `history`, `head`, `wc`, `du`, `mv`, `grep`, `ps`, `kill`, `export`, `edit`, `help`. |
| Implementation of commands | **Every** command is a Lua script, including `cd`, `ll`, `cp` and `cat`. No command is written in Go. The default scripts are embedded in the binary. |
| Extensibility | User scripts in the config directory add commands or replace default ones. |

### 3.3 Editor and execution

| Topic            | Decision                                                                                                              |
| ---------------- | --------------------------------------------------------------------------------------------------------------------- |
| Editor           | Simple multi-line, non-modal editor with Readline-style keys. No Vim mode, no `$EDITOR` hand-off.                     |
| Highlighting     | SQL per dialect, also for `cat` output of sources.                                                                    |
| Completion       | A list that opens **only** on Ctrl+Space (plus one configurable alternative key). Never automatically. No ghost text. |
| Completion depth | Token-level heuristic, no full parser.                                                                                |
| Run keys         | Two keys: statement at cursor; selection, or the whole buffer if nothing is selected.                                 |
| Scripts          | Statement splitting on `;` **and** MySQL `DELIMITER` support. No streaming of large dump files in v1.                 |
| Script errors    | Stop at the first error. Show the statement and its line number.                                                      |
| Multiple results | One tab per result plus a message log (rows affected, duration, warnings, generated statements).                      |
| Headless mode    | Yes: run a script or a single command without UI, with exit code.                                                     |
| Cancel           | A key cancels the running statement.                                                                                  |

### 3.4 Result grid

| Topic | Decision |
|---|---|
| Loading | Keep the cursor open and fetch more rows while scrolling, up to a configurable cap. |
| Sorting and filtering | Results that originate from one table (`ll`, `head`, object tree) are re-queried server-side with `WHERE` / `ORDER BY`. Free queries are sorted and filtered locally on the loaded rows, with a visible hint. |
| Filter kinds | All three: quick search across all columns, filter on the current column, free `WHERE` expression (table-origin results only). |
| Pivot view | Toggle that transposes the grid: attributes become rows, every record is a column. Attribute names stay fixed on the left; horizontal scrolling moves through records. |

### 3.5 Forms

| Topic | Decision |
|---|---|
| Scope | One form for inserting new rows and editing existing rows. Editing requires a primary key or a unique key. No row deletion. |
| Validation | **The database validates.** The form does not pre-check rules. Server errors are mapped back to the offending field where possible. |
| Foreign keys | Search window showing all columns of the referenced table, filter-as-you-type, searched server-side. After selection the form shows key plus first text column. |
| NULL / empty / default | Untouched fields are omitted from the `INSERT` so the database default applies. Empty text is an empty string. A dedicated key sets NULL. |

### 3.6 Transactions, profiles, keys

| Topic | Decision |
|---|---|
| Transactions | Configurable per connection profile: autocommit (default) or manual with explicit `commit` / `rollback` and a status indicator. |
| Profiles | Own TOML profile file plus existing client files (`~/.my.cnf`, `~/.pgpass`). Passwords come from the credential store (A22), a prompt, an environment variable or those client files. No password in the profile file. Connections are managed in the UI (F2). |
| Key style | F-keys with a labelled footer, like Midnight Commander. |
| Help | Footer plus a context-sensitive help overlay. |

### 3.7 Branding

| Topic         | Decision                                                                                                                                                                               |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Splash screen | Shown for 3 seconds when the UI starts.                                                                                                                                                |
| Product name  | **dbcc**, short for *Database Command & Control*.                                                                                                                                      |
| Content       | The title `dbcc` with the line "Database Command & Control", the Datamedrix logo, the line "A product by Datamedrix GmbH", and Anton Grünberg as contact (a.gruenberg@datamedrix.com). |
| Help          | The same screen is available from the help.                                                                                                                                            |

### 3.8 Object tree

| Topic | Decision |
|---|---|
| Position | Left pane, always visible. Editor and results are on the right. |
| Model | The database explorer of DataGrip. |
| Top level | Everything the connected account can see. All visible databases are on the same level. |
| Below a database | Category nodes: Tables, Views, Procedures, Functions, and the further kinds the dialect has. |
| Below a table | Expandable: columns with type, keys, foreign keys, indexes, triggers. Loaded when expanded. |
| Open by key | A table opens its data in the result grid. A procedure or function opens its source in the editor. |
| Editor target | Every opened object gets its own editor tab. The owner's SQL draft keeps its own tab. |
| Form of the source | A runnable script that replaces the object when executed. |
| Mouse | Off by default, can be switched on. |

## 4. Non-goals for v1

- No `rm`, no row deletion from the grid, no schema designer.
- No DDL reconstruction for tables as executable `CREATE TABLE` text (except internally for `cp -a` on MSSQL).
- No streaming import of large dump files.
- No Vim mode, no `$EDITOR` integration, no ghost-text completion.
- No JOIN suggestions derived from foreign keys (candidate for v2).
- No BLOB editing in forms.
- No built-in SSH tunnel (candidate for v2); the tool runs on the host that can reach the database.
- No builds for macOS, Windows or arm64.

## 5. Technology

| Concern | Choice | Notes |
|---|---|---|
| Language | Go, current stable | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w"` |
| TUI | `github.com/rivo/tview` on `github.com/gdamore/tcell/v2` | See 5.1. |
| MySQL / MariaDB | `github.com/go-sql-driver/mysql` | Pure Go. Do **not** enable `multiStatements`; the tool splits statements itself. Support Unix sockets. |
| PostgreSQL | `github.com/jackc/pgx/v5` | Pure Go. |
| MSSQL | `github.com/microsoft/go-mssqldb` | Pure Go. |
| Highlighting | `github.com/alecthomas/chroma/v2` | Lexers exist for MySQL, PostgreSQL, PL/pgSQL and Transact-SQL. A v3 alpha exists; stay on v2. MariaDB uses the MySQL lexer. |
| Scripting | `github.com/yuin/gopher-lua` | Lua 5.1 (plus `goto`), VM and compiler in pure Go. |
| Config | A pure-Go TOML library | For example `github.com/pelletier/go-toml/v2`. |

### 5.1 tview facts that shape the design

- `Table` supports fixed rows and columns (`SetFixed`), virtual content (`SetContent` with a `TableContent` implementation), cell selection (`SetSelectable`) and an explicit scroll offset (`SetOffset(row, column)`). Use virtual content for the result grid and the pivot view.
- `TextArea` has **one** text style. It cannot colour tokens. The SQL editor is therefore a custom primitive (embed `tview.Box`, implement drawing and input handling), or a fork of `TextArea` with per-cell styles.
- `TextView` supports colour tags and, with wrapping off, scrolls in both directions. Use it for `cat` output, the message log and the help overlay.
- `InputField` has `SetAutocompleteFunc` and `Autocomplete()`. The command line can use it, but the list must appear only on demand (return no entries unless completion was requested by key).
- `Form` covers plain fields and drop-downs. NULL state, "untouched" state and the foreign-key lookup need custom `FormItem` implementations.
- `TreeView` for the object tree, `Pages` for modal windows and overlays.
- Mouse support exists (`EnableMouse`) and is off by default. Keep it off by default.

### 5.2 Terminal constraints

- Ctrl+Enter is not distinguishable from Enter in most terminals. Do not bind it.
- F1, F10 and F11 are intercepted by some terminal emulators. Provide the Midnight Commander fallback: Esc followed by a digit, and Alt+digit, act as F1–F10.
- Ctrl+Space can be captured by desktop input methods. Provide a second, configurable completion key.
- The binary must start without external files or helper programs under `TERM=xterm-256color`, `screen-256color`, `tmux-256color` and `linux`. Check how the pinned tcell version resolves terminal capabilities and embed what is needed.
- Degrade gracefully to 256 and 16 colours.

## 6. Architecture

```
cmd/dbcc/main.go
internal/app          wiring; TUI mode and headless mode
internal/config       profiles, settings, keymap, ~/.my.cnf and ~/.pgpass readers
internal/session      pinned connections, transactions, cancel, lazy cursors
internal/dialect      Dialect interface and shared helpers
internal/dialect/mysql      (MySQL and MariaDB, with version-specific branches)
internal/dialect/postgres
internal/dialect/mssql
internal/meta         metadata model and cache
internal/sqltext      tokenizer, statement splitter, completion context
internal/exec         statement and script runner, result model, message log
internal/command      command registry and dispatch (no command logic)
internal/script       Lua runtime and API primitives
internal/script/commands   default commands, one .lua file each, embedded with go:embed
internal/branding     product title, logo text art, contact, version (embedded assets)
internal/export       CSV and JSON writers
internal/ui           tview application, layout, panes, footer, help
internal/ui/editor    custom editor primitive
internal/ui/grid      result grid, pivot view, sort and filter
internal/ui/form      row form, foreign-key lookup window
internal/ui/splash    splash screen and About page
```

Rules:

- `internal/ui` depends on everything else; nothing below depends on `internal/ui`. Headless mode uses the same `exec`, `command`, `script` and `dialect` packages. This separation is what makes the engine testable.
- All dialect differences that the engine needs live behind the `Dialect` interface. No `if dialect == …` in Go outside `internal/dialect`.
- Go contains no command logic. It offers primitives through the Lua API (section 13); every command is a Lua script that combines them. A command may branch on `ctx.dialect` where no primitive covers a difference (for example `ps`, `kill`, `du`).
- Keys, tree actions and the footer invoke commands **by name** through the registry, so a replaced command also changes the behaviour of the UI.

### 6.1 Dialect interface (guidance)

```go
type Dialect interface {
    Name() string                         // "mysql", "mariadb", "postgres", "mssql"
    Open(ctx context.Context, p Profile) (*sql.DB, error)

    // Navigation
    HasSchemas() bool
    ListDatabases(ctx context.Context, c Conn) ([]string, error)
    ListSchemas(ctx context.Context, c Conn, db string) ([]string, error)
    ListObjects(ctx context.Context, c Conn, at Path, kinds ObjectKinds) ([]Object, error)
    ChangeDatabase(ctx context.Context, s *Session, db string) error // USE, or reconnect

    // Metadata
    DescribeTable(ctx context.Context, c Conn, obj Path) (*meta.Table, error)
    ObjectSource(ctx context.Context, c Conn, obj Path, kind ObjectKind) (string, error)

    // SQL generation
    QuoteIdent(name string) string
    Qualify(obj Path) string
    LimitQuery(query string, n int) string
    CopyTable(src, dst Path, t *meta.Table, withStructure bool) ([]string, error)
    ReplaceScript(ctx context.Context, c Conn, obj Path, kind ObjectKind) (string, error) // runnable re-create script
    TextSearchPredicate(columns []meta.Column, param string) string // quick search

    // Text handling
    Splitter() sqltext.Splitter
    LexerName() string                    // chroma lexer
    Keywords() []string
    Functions() []string

    // Session control
    SessionID(ctx context.Context, c Conn) (string, error)
    Cancel(ctx context.Context, aux Conn, sessionID string) error
    SetReadOnly(ctx context.Context, c Conn) error
    ImplicitCommit(stmt string) bool      // DDL that commits implicitly

    // Errors
    MapError(err error, t *meta.Table) *FieldError // column or constraint, if derivable
}
```

### 6.2 Metadata model (guidance)

One model feeds `cat`, completion, forms, `cp -a` and the grid.

```go
type Table struct {
    Path        Path
    Kind        ObjectKind            // table or view
    Columns     []Column
    PrimaryKey  *Key
    UniqueKeys  []Key
    ForeignKeys []ForeignKey
    Checks      []Check               // name, expression
    Indexes     []Index               // name, columns with order and prefix length, unique, type
    Triggers    []Trigger             // name, timing, event
    Comment     string
    Options     map[string]string     // engine, charset, collation, row estimate, partitioning summary
}

type Column struct {
    Name          string
    Position      int
    DataType      string              // as declared: varchar(40), decimal(10,2) unsigned
    Nullable      bool
    Default       *string             // nil means no default
    AutoIncrement bool                // auto_increment, identity, serial
    Generated     *Generated          // expression, stored or virtual
    EnumValues    []string
    Collation     string
    Comment       string
}

type ForeignKey struct {
    Name       string
    Columns    []string
    RefTable   Path
    RefColumns []string
    OnUpdate   string
    OnDelete   string
}
```

Cache metadata per schema, loaded lazily on first use. Invalidate after any statement the dialect classifies as DDL and through a `refresh` action.

### 6.3 Sessions

- Each open profile has one **main session**: a pinned `*sql.Conn`, never the pool. Session state (current database, transaction, variables, temporary tables) lives there. All user statements run on it.
- Each open profile has one **auxiliary connection** for metadata queries and for cancelling the main session. The tree and completion keep working while the main session is busy. Consequence: DDL that is not yet committed in the main session (possible on PostgreSQL and MSSQL) is not visible in tree and completion until commit.
- Several profiles can be open at once; `cd /other-connection/db` switches between them.
- **Open-cursor rule:** a lazily loaded result holds the main session. Issuing any new statement first closes that cursor; its tab keeps the rows loaded so far (its current page, section 10.2) and is marked *partial*. Paging on such a tab re-runs the statement and skips to the page. In a script run, every result except the last is buffered up to `max_rows_buffered`.
- Cancel: MySQL and MariaDB use `KILL QUERY <id>` on the auxiliary connection; PostgreSQL and MSSQL use the driver's cancellation.

## 7. Screen layout

```
+ prod -----------+-[scratch]-[calc_total]---------- Editor +
| v shop          | SELECT ...                              |
|   v Tables 12   |                                         |
|     > customers |                                         |
|     > orders    +-[1 customers]-[2 orders]-[Messages]-----+
|   > Views 3     | id | name        | city     | ...       |
|   > Procedures 5|  1 | ...         | ...      |           |
|   > Functions 2 |                                         |
| > crm           |                                         |
+-----------------+-----------------------------------------+
| /prod/shop > _                                 command line |
| prod | autocommit | 1,204+ rows | 38 ms | filter: city     |
| 1Help 2Conn 3View 4Edit 5Run 6RunAll 7New 8Cancel 9Cmd 10Quit |
+-------------------------------------------------------------+
```

- Panes: object tree on the left (section 7.2); editor with tabs and result area with tabs on the right; command line below. Tab and Shift+Tab cycle the focus.
- The footer shows the F-key labels **for the focused pane**; it changes with focus.
- The status line shows profile, transaction state, row count (with `+` while more can be loaded, `partial` or `capped` where applicable; paged results show the page, e.g. `rows 3,001–6,000 · page 2 +`, section 10.2), duration, and active sort and filter.
- In manual-transaction profiles the status line shows `TXN OPEN` after the first statement and flags statements that commit implicitly (MySQL and MariaDB DDL).
- Read-only profiles show `READ ONLY` permanently.

### 7.1 Splash screen and About page

One component renders both.

Content, centred on the screen:

1. The Datamedrix logo as text art
2. The product title `dbcc`, below it `Database Command & Control`, and the version
3. `A product by Datamedrix GmbH`
4. `Contact: Anton Grünberg`, followed by the contact details the owner supplies

Behaviour:

- The splash screen appears when the UI starts, stays for 3 seconds, then gives way to the main layout.
- Start-up work (configuration, Lua scripts, connecting, metadata) runs behind it, so the 3 seconds are not added on top of the connect time. A password prompt appears after the splash.
- No splash in headless mode or when stdout is not a terminal.
- The help overlay has an **About** page that shows the same screen without a timer. `help about` opens it directly.

Logo:

- Terminal graphics protocols are not reliable over SSH. The logo is text art built from ASCII and Unicode block characters.
- **Do not invent a logo.** The owner supplies the official Datamedrix logo; the text-art variants are derived from it and approved by the owner. Until then, show the word "Datamedrix" in plain text.
- Two variants are embedded as text files: wide (up to 60 columns by 12 rows) and compact (up to 30 by 6). Choose by terminal size; below the compact size show text only.
- Use the brand colours on true-colour and 256-colour terminals, monochrome otherwise.

### 7.2 Object tree

The left pane is a tree of everything the connected account can see, modelled on the database explorer of DataGrip.

```
shop                          database
  Tables (12)
    customers
      Columns
        id        int unsigned   PK auto
        name      varchar(80)    not null
        city_id   int unsigned   FK
      Keys
      Foreign keys
      Indexes
      Triggers
    orders
  Views (3)
  Procedures (5)
  Functions (2)
  Triggers (4)
  Events (1)
crm
information_schema            system databases last, dimmed
```

Structure:

- **Top level:** the databases visible to the account of the current connection, all on one level. The pane title shows the connection.
- PostgreSQL and MSSQL insert the schema level between database and categories. On PostgreSQL, expanding another database connects to it in the background.
- **Categories:** Tables, Views, Procedures and Functions on every dialect; in addition Triggers, Events (MySQL, MariaDB), Sequences (MariaDB, PostgreSQL, MSSQL) and Materialized views (PostgreSQL). Empty categories are hidden. A category shows its object count.
- **Below a table:** Columns (name, declared type, markers for primary key, foreign key, NOT NULL, auto), Keys, Foreign keys (with their target), Indexes, Triggers. A view expands into its columns.
- Nodes load lazily when they are expanded, on the auxiliary connection, through the metadata cache of section 6.2. F5 reloads the selected node.
- `/` filters the tree by name while typing; Esc clears the filter.
- The tree and the current path are independent. Opening an object from the tree uses its qualified name and does not change the path. A key sets the current path to the selected node; `cd` in the command line moves the tree selection to the new path.

Actions. Each one invokes a Lua command by name (section 8):

| Node | Enter | F3 | F4 | F7 |
|---|---|---|---|---|
| Database, schema, category | Expand or collapse | – | – | – |
| Table | `ll`: data in the result grid | `cat`: structure | – | New row |
| View | `ll`: data in the result grid | `cat`: source, read-only | `edit`: source in an editor tab | – |
| Procedure, function, trigger, event | `edit`: source in an editor tab | `cat`: source, read-only | `edit` | – |
| Column, key, foreign key, index | – | `cat` of the table | – | – |

With the mouse switched on (`mouse = true`): a click selects, a double click acts like Enter, the wheel scrolls. Text selection in the terminal then needs the Shift key.

## 8. Commands

Input in the command line: the first word is looked up in the command registry. If it is not a command, the whole line is executed as SQL. Arguments complete on Ctrl+Space (object names, paths, profiles).

Note on the product name: T-SQL has its own `DBCC` statements (for example `DBCC CHECKDB`). They are plain SQL and pass through to the server. Never register a command named `dbcc`.

| Command | Meaning | Writes | Main primitives |
|---|---|---|---|
| `cd <path>` | `cd db`, `cd ..`, `cd /conn/db/schema`, `cd -` | no | `ctx:chdir` |
| `ls [path]` | List objects at the path level with their kind | no | `ctx:objects`, `ctx:rows` |
| `pwd` | Print the current path | no | `ctx.path` |
| `ll <table\|view>` | `SELECT * FROM obj` in the grid, table-origin result | no | `ctx:grid` |
| `head <table\|view> [n]` | First n rows, default 10 | no | `ctx:limit`, `ctx:grid` |
| `wc <table\|view>` | `SELECT COUNT(*)` | no | `ctx:query` |
| `du [table]` | Data and index size, row estimate | no | `ctx:query`, `ctx:rows` |
| `cat [-t kind] <object>` | Structure of a table; source of a view, procedure or function | no | `ctx:meta`, `ctx:source`, `dbcc.fmt.table`, `ctx:text` |
| `edit [-t kind] <object>` | Open the source of a view, procedure, function, trigger or event in an editor tab as a runnable replace script, see 9.1 | no | `ctx:replace_script`, `ctx:editor_open` |
| `cp [-a] <src> <new>` | Copy, see 8.2 | yes | `ctx:meta`, `ctx:copy_sql`, `ctx:exec` |
| `mv <object> <new>` | Rename a table or view | yes | `ctx:exec` |
| `grep <pattern>` | Search object names, column names and sources of views and routines | no | `ctx:query`, `ctx:rows` |
| `ps` | Running sessions and statements | no | `ctx:query`, `ctx:rows` |
| `kill <id>` | Terminate a session | yes | `ctx:exec` |
| `export <file>` | Write the active result to a file, see 10.7 | no | `ctx:export` |
| `history` | Show the statement history; Enter loads an entry into the editor | no | `ctx:history`, `ctx:pick`, `ctx:editor_set` |
| `help [command]` | Command help; `help about` shows the About page | no | `dbcc.commands`, `ctx:about` |

Every command in this table is a Lua script (`internal/script/commands/<name>.lua`, embedded with `go:embed`). There is no second kind of command. The scripts use only the public API of section 13, so they double as reference examples, and a user script of the same name replaces any of them.

Commands marked *writes* declare `write = true`: the runtime shows the SQL they are about to run, asks for confirmation and blocks them in read-only profiles.

If a default command needs something the API does not offer, add a primitive to the API. Do not move command logic into Go.

### 8.1 `cat` for tables

Output is text in a horizontally scrollable view, in this order:

1. **Columns** in the layout of MySQL `DESCRIBE`: `#`, Field, Type, Null, Key, Default, Extra, Comment. Extra carries auto-increment, generated-column expressions and `ON UPDATE` clauses.
2. **Primary key and unique keys** with their columns.
3. **Foreign keys**: name, columns, referenced table and columns, `ON UPDATE`, `ON DELETE`.
4. **Check constraints**: name and expression, highlighted as SQL.
5. **Indexes**: name, columns with order and prefix length, uniqueness, type.
6. **Triggers**: name, timing, event.
7. **Table options**: engine, charset, collation, row estimate, comment, partitioning summary.

Nothing that the catalog knows about the table may be missing. Headless mode prints the same text without colour when stdout is not a terminal.

`cat.lua` builds this text from `ctx:meta` and `ctx:source` with the formatting helpers of the API. The layout is therefore changeable by the owner without recompiling.

Sources per dialect: MySQL and MariaDB read `information_schema` (`COLUMNS`, `TABLE_CONSTRAINTS`, `KEY_COLUMN_USAGE`, `REFERENTIAL_CONSTRAINTS`, `CHECK_CONSTRAINTS`, `STATISTICS`, `TRIGGERS`, `TABLES`). PostgreSQL reads `pg_catalog`. MSSQL reads `sys.*`. View and routine sources: `SHOW CREATE VIEW|PROCEDURE|FUNCTION` on MySQL and MariaDB, `pg_get_viewdef` and `pg_get_functiondef` on PostgreSQL, `sys.sql_modules` on MSSQL.

### 8.2 `cp`

| Variant | MySQL / MariaDB | PostgreSQL | MSSQL |
|---|---|---|---|
| `cp src new` | `CREATE TABLE new AS SELECT * FROM src` | same | `SELECT * INTO new FROM src` |
| `cp -a src new` | `CREATE TABLE new LIKE src`, then `INSERT … SELECT` | `CREATE TABLE new (LIKE src INCLUDING ALL)`, then `INSERT … SELECT` | Generate `CREATE TABLE` from the metadata model, then `INSERT … SELECT` |

The dialect-specific statements come from the primitive `ctx:copy_sql(src, dst, { structure = true })`. `cp.lua` handles arguments, confirmation, execution and messages.

Edge cases to handle and test in `cp -a`:

- Generated columns must be excluded from the `INSERT` column list.
- Identity columns: `OVERRIDING SYSTEM VALUE` on PostgreSQL, `SET IDENTITY_INSERT … ON` on MSSQL.
- Foreign keys are not copied (see section 17).

## 9. Editor, completion, scripts

### 9.1 Editor

- Custom primitive. Multi-line buffer, selection, undo and redo, line numbers, Readline-style movement and kill keys, bracketed paste.
- Highlighting through chroma tokens mapped to tcell styles. Re-highlight on change; buffers are small.
- The current statement (as found by the splitter) is subtly marked so it is clear what "run statement" will execute.

Tabs:

- The editor has tabs. The first one is the **scratch** tab for the owner's own SQL. Scratch tabs are saved to the state directory on exit and restored on start. More scratch tabs can be opened.
- **Object tabs** are opened by `edit`, from the tree or the command line: one per object, titled with the object name. Opening an object that already has a tab focuses that tab.
- An object tab holds a runnable **replace script** produced by `Dialect.ReplaceScript` from the current source:
  - MariaDB and PostgreSQL: `CREATE OR REPLACE …`
  - MySQL: `DROP … IF EXISTS` followed by `CREATE …`
  - MSSQL: `CREATE OR ALTER …`
  - MySQL and MariaDB wrap the body in `DELIMITER` lines, so *Run all* works on the script unchanged.
- *Run all* in an object tab replaces the object. It counts as a write: blocked in read-only profiles, confirmed where `confirm_writes` is set. After a successful run the tab is marked saved and the metadata cache is refreshed.
- The source as loaded is kept. A key restores it into the tab, and after a failed run the message says whether the object still exists on the server.
- Tabs with unsaved changes are marked. Closing one, or quitting, asks first.

### 9.2 Completion

Opens only on Ctrl+Space or the alternative key. Candidates depend on the tokens before the cursor:

| Context | Candidates |
|---|---|
| After `FROM`, `JOIN`, `INTO`, `UPDATE`, `TABLE`, `DESCRIBE` | Tables and views of the current database or schema; databases |
| After `db.` | Objects of that database |
| After `alias.` or `table.` | Columns of that table. Aliases are resolved within the current statement. |
| After `CALL`, `EXEC` | Procedures |
| Elsewhere inside a statement | Columns of the tables named in the statement, then keywords and functions of the dialect |
| Command line, first word | Commands |
| Command line, arguments | By argument type declared by the command: object, path, profile, file |

The list filters while typing, Enter or Tab accepts, Esc closes. Identifiers are quoted only when necessary.

Use one hand-written tokenizer per dialect in `internal/sqltext` for both the splitter and the completion context. Chroma is used for colouring only.

### 9.3 Statement splitting

The splitter returns statements with their starting line number. The same splitter finds the statement at the cursor.

- **MySQL / MariaDB (v1):** `;` as default delimiter; `DELIMITER <token>` lines exactly as the `mysql` client handles them (the line itself is never sent to the server); single- and double-quoted strings with backslash escapes and doubled quotes; backtick identifiers; comments `-- `, `#`, `/* */`; executable comments `/*! … */` are passed through.
- **PostgreSQL:** additionally dollar-quoted strings (`$tag$ … $tag$`), `E''` strings, nested block comments.
- **MSSQL:** batches are separated by `GO [count]` on its own line; `;` does not split batches.

### 9.4 Running statements and scripts

- *Run statement* executes the statement at the cursor. *Run all* executes the selection, or the whole buffer if nothing is selected.
- `dbcc -f file.sql` and *Run all* use the same runner.
- Statements run sequentially on the main session. On the first error the run stops; the message log and the status line show the error, the statement text and its line number, and the editor cursor jumps to that line.
- Every statement produces a message-log entry: text (shortened), rows affected or returned, duration, warnings (`SHOW WARNINGS` on MySQL and MariaDB when the warning count is non-zero).
- Each result set gets its own tab. Procedures returning several result sets produce several tabs.
- No whole-script rollback is promised. On MySQL and MariaDB, DDL commits implicitly.

## 10. Result grid

### 10.1 Basics

- `tview.Table` with a custom `TableContent`. Header row fixed. Cell cursor. No wrapping, ever.
- Column width is computed from the header and the first 200 loaded rows and capped at `max_column_width` (default 40). Longer values are cut with an ellipsis.
- Enter on a cell opens a detail window with the full value, wrapped and scrollable. JSON is pretty-printed.
- NULL is rendered distinctly (dim `NULL`), binary values as `<BLOB n bytes>`. Numbers are right-aligned.
- A key copies the current cell, another the current row, through OSC 52.

### 10.2 Lazy loading and pages

- A result tab holds at most **one page** of `page_size` rows (default 3,000, at least 10; A24). Within the page, rows are fetched in chunks (`fetch_size`, default 500) when the cursor approaches the end of the loaded rows.
- When the page is full the cursor stays open (the result is not capped). `n` or F6 shows the next page: the rows of the current page are dropped (their memory is released) and the next page is read from the open cursor. On the last page the status line says `last page`.
- `p` shows the previous page. A cursor cannot read backwards: the statement is re-run from the start with the same arguments, the rows before the page are read and discarded (status hint `skipping 6,000 rows…`, F8 cancels), then the page is loaded. This is done only for statements the dialect classifies as read queries; otherwise: `previous pages are not available: the statement is not a read query`.
- The same re-run and skip happens when moving forward after the cursor was closed by the open-cursor rule (section 6.3).
- The status line shows the rows of the page, the page number and `+` while a next page may exist, e.g. `rows 3,001–6,000 · page 2 +`, plus `partial` after the cursor was closed. A complete result of one page reads `42 rows`.
- `max_rows` no longer applies to the grid; headless output streams every row as before.
- The open-cursor rule of section 6.3 applies.

### 10.3 Result origin

Every result carries its origin:

- **table-origin**: produced by `ll`, `head`, or opening a table or view from the tree. The grid knows the object and its metadata.
- **free**: anything else.

Table-origin results enable server-side sort and filter, the free `WHERE` filter, and row editing.

### 10.4 Sorting

- A key cycles the current column through ascending, descending, off. A second key adds the current column as a further sort key.
- The header shows direction and rank.
- Table-origin: the grid re-runs `SELECT * FROM obj [WHERE …] ORDER BY …` and restarts lazy loading at page 1.
- Free: sort the loaded rows of the current page locally, type-aware, NULLs first. The status line shows `sorted within N loaded rows` (the rows of the page). There is no "load all" (section 10.2, A24); a sort over the whole result needs `ORDER BY` in the statement.

### 10.5 Filtering

Three kinds, combinable with AND, each visible in the status line and individually removable:

1. **Quick search**: one text matched against all columns (contains, case-insensitive).
2. **Column filter** on the current column: contains, starts with, equals, not equals, `<`, `<=`, `>`, `>=`, is NULL, is not NULL.
3. **Free `WHERE`**: an expression typed by the owner, with completion. Table-origin results only.

Execution:

- Table-origin: all three become one server-side `WHERE`; the result starts again at page 1. Quick search and column filters use bound parameters. The free expression is inserted verbatim in parentheses; if the server rejects it, the previous result stays and the error goes to the message log. Quick search on a large table is a full scan; the cancel key must work.
- Free: quick search and column filters apply locally to the loaded rows of the current page, with the hint `filtered within N loaded rows`.

### 10.6 Pivot view

- A key toggles between the normal grid and the transposed grid.
- Transposed: one row per attribute, one column per record. The first column (attribute names) is fixed. The column header shows the record number, or the primary-key value for table-origin results.
- Horizontal scrolling moves through the records of the current page and triggers lazy loading near the loaded end; `n`, F6 and `p` change the page as in the normal view. The record header shows the row number within the whole result. Vertical scrolling moves through attributes.
- The cursor keeps pointing at the same record and attribute when toggling.
- Sorting, filtering, cell detail, copy, edit and new-row actions work in both views and always refer to records.
- Record columns use a common width (`pivot_column_width`, default 30).

### 10.7 Export

`export <file>` writes the whole active result, not just the page in memory. Format follows the extension: `.csv`, `.json`. On the first page with the cursor still open, the export writes the loaded rows and continues the cursor to the end; otherwise it re-runs the statement from the start (with its arguments; read queries only) and streams every row to the file. It shows progress and can be cancelled. An active local filter is applied while streaming. An active local sort forces loading all rows into memory before writing (memory grows with the result).

## 11. Row form

- Opens for a **new row** from a table in the tree or from a table-origin result, and for **editing** the record under the cursor in a table-origin result.
- Editing requires a primary key or a unique key whose columns are all present and NOT NULL in the row. Otherwise the action is disabled with an explanation.
- One field per column, labelled with name and declared type. Markers for NOT NULL, key, foreign key, default.

| Column kind | Field |
|---|---|
| Auto-increment, identity, generated | Read-only, shown as `auto` |
| `ENUM` | Drop-down |
| `SET`, boolean | Check boxes |
| Foreign key | Read-only display of key plus first text column of the referenced row; a key opens the lookup window |
| `TEXT`, `JSON` | One-line preview; a key opens a multi-line editor window |
| `BLOB` and other binary | Not editable in v1 |
| Everything else | Text input |

Field states: *untouched*, *value* (including the empty string), *NULL*. A key sets NULL, another resets the field to untouched.

Statements:

- Insert: only fields that are not untouched appear in the column list. Bound parameters throughout.
- Update: only changed fields in `SET`; `WHERE` on the original key values. If the affected-row count is not 1, report it prominently.
- Every statement goes to the message log. In manual-transaction profiles it becomes part of the open transaction.

Validation: none on the client. On a server error the form stays open, `Dialect.MapError` resolves the column or constraint, the field is marked and the server message is shown. For MySQL and MariaDB cover at least: column cannot be NULL, no default for field, data too long, out of range, incorrect value for column, duplicate key, foreign-key failure, check-constraint violation. Verify the error numbers of both servers; they differ for check constraints.

Lookup window: a grid of the referenced table with all columns, a filter line (quick search, server-side, limited), lazy loading. Enter selects. A composite foreign key fills all its fields at once.

Keys in the form: save; save and open a new empty form; cancel.

## 12. Transactions and safety

Per profile:

- `autocommit = true` (default): every statement commits.
- `autocommit = false`: the tool starts a transaction with the first statement. `commit` and `rollback` are plain SQL. The status line shows `TXN OPEN`. Quitting or `cd` to another database with an open transaction asks: commit, roll back, or stay.
- `read_only = true`: the session is set read-only on the server where the dialect supports it, and a client-side guard rejects statements whose first keyword is not a read statement. The guard is best effort; the server setting is the real protection. Write commands and forms are disabled.
- `confirm_writes = true`: every statement that is not a read statement asks for confirmation.

History: statements are appended to a history file in the state directory with timestamp, profile, path, duration and outcome. Statements containing `IDENTIFIED BY` or `PASSWORD` are not recorded.

## 13. Lua scripting

Principle: Go provides primitives, Lua provides every command. The tool has no command that is not a Lua script.

- Runtime: gopher-lua. One Lua state, used from one goroutine.
- Sandbox: load only the base, table, string and math libraries. No `os`, no `io`. File access happens only through primitives such as `ctx:export`.
- Default commands: `internal/script/commands/*.lua`, embedded in the binary, loaded first.
- User scripts: `~/.config/dbcc/commands/*.lua`, loaded afterwards. A user command with the name of a default command replaces it. This holds for every command.
- A user script that fails to load is reported in the message log and skipped. If it was meant to replace a default command, the default stays active, so a broken `cd.lua` cannot lock the owner out.
- `dbcc --no-user-commands` starts with the defaults only. `dbcc --export-commands <dir>` writes the embedded scripts to a directory as a starting point for changes.

```lua
dbcc.command{
  name  = "head",
  usage = "head <table|view> [n]",
  help  = "Show the first n rows (default 10)",
  args  = { "object", "int?" },   -- drives argument completion and arity checks
  write = false,                  -- true: confirmation, blocked in read-only profiles
  run = function(ctx, args)
    local n = tonumber(args[2]) or 10
    local obj = ctx:resolve(args[1])
    return ctx:grid(ctx:limit("SELECT * FROM " .. obj.sql, n), { origin = obj })
  end,
}
```

| API | Purpose |
|---|---|
| **Context** | |
| `ctx.dialect` | `"mysql"`, `"mariadb"`, `"postgres"`, `"mssql"` |
| `ctx.path` | Current connection, database, schema |
| `ctx.profile` | Name and flags of the current profile (read-only, autocommit) |
| **Navigation and metadata** | |
| `ctx:chdir(path)` | Change the current path; issues `USE` or reconnects as the dialect requires |
| `ctx:objects(path, kinds)` | List databases, schemas or objects at a path |
| `ctx:resolve(name)` | Resolve a name against the current path; returns kind, parts and quoted SQL name |
| `ctx:meta(name)` | Metadata model of a table or view as a Lua table (section 6.2) |
| `ctx:source(name, kind)` | Source text of a view, procedure, function or trigger |
| **SQL helpers** | |
| `ctx:quote(ident)` | Quote an identifier |
| `ctx:limit(sql, n)` | Dialect-specific row limit |
| `ctx:copy_sql(src, dst, opts)` | Statements that copy a table; `opts.structure` selects the `cp -a` variant |
| `ctx:replace_script(name, kind)` | Runnable script that re-creates a view, routine, trigger or event from its current source |
| **Execution** | |
| `ctx:query(sql, ...)` | Run a query with parameters; returns rows as a table (buffered, capped) |
| `ctx:exec(sql, ...)` | Run a statement; returns affected rows |
| **Output** | |
| `ctx:grid(sql, opts, ...)` | Open a lazily loaded result tab; `opts.origin` marks it table-origin |
| `ctx:rows(headers, rows, opts)` | Show rows computed in Lua in a result tab |
| `ctx:text(str, lexer)` | Show highlighted text in a tab |
| `dbcc.fmt.table(headers, rows)` | Aligned text table as a string |
| `ctx:message(msg)` | Write to the message log |
| `ctx:export(file, opts)` | Write the active result to a file (section 10.7) |
| **Interaction** | |
| `ctx:confirm(msg)` | Ask yes or no |
| `ctx:pick(items, opts)` | Let the owner choose from a list; returns the chosen item |
| `ctx:editor_set(text)` | Put text into the current scratch tab |
| `ctx:editor_open(title, text, opts)` | Open or focus an editor tab; `opts.object` binds it to a database object |
| `ctx:about()` | Show the About page (section 7.1) |
| **Registry and history** | |
| `dbcc.command{...}` | Register a command |
| `dbcc.commands()` | List registered commands with usage and help |
| `ctx:history(n)` | The last n history entries |

Parameters of `ctx:query`, `ctx:exec` and `ctx:grid` are written as `?` in every dialect: when arguments are passed, the runtime rewrites each `?` outside strings, comments and quoted identifiers into the dialect's placeholder (`$1…` on PostgreSQL, `@p1…` on MSSQL). Without arguments the statement is sent unchanged (`?` is also a jsonb operator on PostgreSQL). Scripts may also write the native placeholders directly; mixing both in one statement is not supported.

All primitives work in headless mode: `ctx:grid`, `ctx:rows`, `ctx:text`, `ctx:about` and `ctx:editor_open` print to stdout (so `dbcc -c "edit calc_total"` prints the replace script); `ctx:confirm` fails unless `--yes` is given; `ctx:pick` and `ctx:editor_set` fail with a clear message.

The default commands are the contract tests of this API. A change to a primitive must keep all of them passing.

## 14. Keys and help

Defaults; every binding can be changed in the configuration. Esc+digit and Alt+digit act as F1–F10. Modal windows (form, lookup, dialogs) override the global bindings while they are open.

**Global**

| Key | Action |
|---|---|
| F1 | Help overlay for the focused pane |
| F2 | Profiles: open, switch, close connections |
| F9 | Focus the command line |
| F10 | Quit |
| Tab / Shift+Tab | Next / previous pane |
| F8 | Cancel the running statement |

**Object tree**

| Key | Action |
|---|---|
| Up, Down, PgUp, PgDn | Move |
| Right / Left | Expand / collapse, or go to the parent node |
| Enter | Default action of the node (section 7.2): expand, open data in the grid, or open source in an editor tab |
| F3 | `cat` the selected object |
| F4 | `edit`: source of a view or routine in an editor tab |
| F7 | New row for the selected table |
| F5 | Reload the selected node |
| `/` | Filter the tree by name |
| `c` | Set the current path to the selected node |

**Editor**

| Key | Action |
|---|---|
| F5 | Run statement at cursor |
| F6 | Run selection, or the whole buffer of the current tab |
| Ctrl+Space | Completion list |
| Alt+, / Alt+. | Previous / next editor tab |
| Alt+N | New scratch tab |
| Alt+X | Close the tab (asks if there are unsaved changes) |
| Alt+R | Restore the source as loaded (object tabs) |

**Result grid**

| Key | Action |
|---|---|
| Arrows, PgUp, PgDn, Home, End | Move; Left and Right scroll by column |
| F3 | Toggle pivot view |
| F4 | Edit the record under the cursor |
| F7 | New row |
| F5 | Re-run the query |
| Enter | Cell detail |
| `s` / `S` | Sort by current column (cycle) / add as further sort key |
| `/` | Quick search |
| `f` | Column filter on the current column |
| `w` | Free `WHERE` (table-origin only) |
| `x` | Remove filters and sorting (menu if several are active) |
| `n` / F6 | Next page (section 10.2) |
| `p` | Previous page (re-runs a read query and skips the rows before it) |
| `c` / `C` | Copy cell / row |
| `[` / `]` | Previous / next result tab |

**Form**

| Key | Action |
|---|---|
| F2 | Save |
| F6 | Save and new |
| F4 | Open lookup or multi-line editor for the current field |
| Ctrl+N | Set NULL |
| Ctrl+U | Reset field to untouched |
| Esc | Cancel |

Help:

- The footer always shows the F-key labels of the focused pane.
- F1 opens an overlay with **all** bindings of the focused pane and the global ones, generated from the live keymap so it can never be out of date. It is searchable.
- `help` in the command line lists commands; `help <command>` shows usage and description, taken from the command's Lua registration.
- The help overlay has an **About** page with the content of the splash screen (section 7.1). `help about` opens it directly.

## 15. Configuration and command line

Directories: configuration in `~/.config/dbcc/` (`config.toml`, `commands/`), state in `~/.local/state/dbcc/` (history, editor buffer). Honour `XDG_CONFIG_HOME` and `XDG_STATE_HOME`.

```toml
[settings]
fetch_size         = 500
max_rows           = 100000       # headless/compatibility; the UI grid pages instead
page_size          = 3000         # rows per result page in the grid (at least 10)
max_rows_buffered  = 10000
max_column_width   = 40
pivot_column_width = 30
completion_key_alt = "ctrl+o"
mouse              = false
splash             = true         # 3-second splash screen at start

[profiles.shop-dev]
driver         = "mysql"        # mysql | postgres | mssql; MariaDB is detected at connect
host           = "127.0.0.1"
port           = 3306
# socket       = "/var/run/mysqld/mysqld.sock"
user           = "anton"
database       = "shop"
password       = "prompt"       # prompt | env:DBCC_SHOP_PW | mycnf | pgpass
autocommit     = true
read_only      = false
confirm_writes = false

[keys.grid]
pivot = "f3"
```

- `password = "mycnf"` reads the `[client]` section of `~/.my.cnf` (user, password, host, port, socket). `pgpass` reads `~/.pgpass`.
- The profile file never contains a password. Warn if its permissions are wider than 0600.

Command line:

```
dbcc [profile]                          start the UI, optionally connect
dbcc -p <profile> -f <file.sql>         run a script without UI
dbcc -p <profile> -c "<command|SQL>"    run one command or statement without UI
dbcc -p <profile> < file.sql            stdin is not a terminal: run without UI
      --format table|csv|tsv|json         output format for results (default: table)
      --yes                               answer confirmations with yes
      --no-splash                         skip the splash screen
      --no-user-commands                  load only the embedded default commands
      --export-commands <dir>             write the embedded command scripts to a directory
```

Exit codes: 0 success, 1 SQL or script error, 2 usage, configuration or connection error. Errors go to stderr with statement and line number.

## 16. Milestones

Each milestone is usable on its own and ends with tests.

| # | Scope | Acceptance |
|---|---|---|
| M0 | Project skeleton, config and profiles, MySQL/MariaDB connect with pinned session, tokenizer and splitter with `DELIMITER`, runner, headless `-f` and `-c` for SQL | A script with procedures and `DELIMITER` runs against MySQL and MariaDB; a failing statement stops the run with the right line number and exit code 1; binary is static |
| M1 | Lua runtime, sandbox and core API; command registry; default commands `cd`, `ls`, `pwd`, `ll`, `head`, `wc` as Lua scripts, usable headless; user scripts with override and fallback | `dbcc -c "ll customers"` works without UI; a user script adds a command and replaces a default one without recompiling; a broken override falls back to the default; Go contains no command logic |
| M2 | TUI shell: splash screen, layout, object tree down to object names (databases on the top level, categories with counts, lazy loading, filter), command line, footer, help overlay with About page; result grid with lazy loading and horizontal scrolling; message log | Splash shows for 3 seconds at start and from the help; all databases the account can see appear on the top level; Enter on a table opens its data; a 1-million-row table stays responsive; no cell ever wraps; works over SSH and inside tmux |
| M3 | Custom editor with highlighting and tabs; run statement and run all; result tabs; cancel; `history` | Several statements produce several tabs; F8 stops a long query; scratch tabs survive a restart |
| M4 | Metadata model for MySQL/MariaDB; `cat` for tables, views, procedures, functions; table children in the tree; `edit` with object tabs and replace scripts; completion on Ctrl+Space | `cat` shows every constraint, index and trigger of a test schema; a procedure opened from the tree, changed and run with *Run all* is replaced on MySQL and on MariaDB; completion resolves aliases |
| M5 | Remaining default commands in Lua: `du`, `mv`, `grep`, `ps`, `kill`, `cp`, `cp -a`, `export`, `help` | Every command of section 8 exists as one Lua file; `cp -a` preserves keys and indexes, including a table with generated columns |
| M6 | Grid sorting, all three filter kinds, pivot view | Server-side sort and filter on table-origin results; local with hint on free queries; pivot keeps the cursor position |
| M7 | Row form: insert, edit, foreign-key lookup, NULL handling, error mapping | Constraint violations mark the right field; a composite foreign key is filled by one selection |
| M8 | Manual transactions, read-only and confirm-writes profiles, status indicators | Open transaction survives navigation; read-only profile cannot write |
| M9 | PostgreSQL dialect | All features above on PostgreSQL, including schema level and reconnect on `cd` |
| M10 | MSSQL dialect | All features above on MSSQL, including `GO` batches and generated DDL for `cp -a` |

Testing:

- Unit tests for tokenizer, splitter, completion context, SQL generation and error mapping. Golden files for `cat` output.
- Integration tests against real servers in containers (development only): current MySQL 8.x, MariaDB LTS, PostgreSQL, SQL Server.
- UI tests with tcell's simulation screen for grid scrolling, pivot, form states, key handling and the splash timer.
- Every default Lua command has a headless test against each supported server. These tests are the contract tests of the Lua API.
- A check in CI that the binary is statically linked.

## 17. Assumptions and open points

| # | Assumption | Status |
|---|---|---|
| A1 | `cp -a` does not copy foreign keys. | confirmed |
| A2 | `cp view new` creates a snapshot table; `cp -a view new` creates a second view with the same definition. | confirmed |
| A3 | Profiles get `read_only` and `confirm_writes` flags for production databases. | confirmed |
| A4 | Default key map of section 14, including letter keys for sort and filter in the grid. | confirmed |
| A5 | Headless output formats: table, csv, tsv, json. | default |
| A6 | `max_rows` defaults to 100,000; `fetch_size` to 500. For the UI grid, `max_rows` is superseded by `page_size` (A24). | default |
| A7 | Copying to the clipboard goes through OSC 52. | default |
| A8 | Lua sandbox without `os` and `io`. | default |
| A9 | `cat` lists triggers and table options in addition to columns, keys, constraints and indexes. | default |
| A10 | Statement-at-cursor on MSSQL uses `;` and blank lines as boundaries inside a `GO` batch. | open, decide in M10 |
| A11 | The tool is named `dbcc` (Database Command & Control). | confirmed |
| A12 | Any key dismisses the splash screen before the 3 seconds are over. | confirmed |
| A13 | The splash screen can be switched off (`splash = false`, `--no-splash`). | confirmed |
| A14 | The owner supplies the official Datamedrix logo; the text-art variants are derived from it and approved by the owner. | confirmed (2026-11-21): logo docs/dmx_logo.png; variants wide 60×6 and compact 30×3 generated by internal/branding/gen/logo2text.py are approved |
| A15 | The owner supplies the contact details shown beside the name (e-mail, phone, web address). | confirmed: e-mail and https://www.datamedrix.com |
| A16 | The About page also shows version, build date and the licence notices of the bundled libraries. | default |
| A17 | `--no-user-commands` and `--export-commands` exist. | default |
| A18 | With several connections open, the tree shows the current connection only and switches with it. | confirmed |
| A19 | Enter on a view opens its data; F4 opens its source in an editor tab. | confirmed |
| A20 | System databases are listed last and dimmed, not hidden. | default |
| A21 | On MySQL, replacing a routine by `DROP` plus `CREATE` is not atomic and may discard privileges granted on the routine. Verify this, and state it in the confirmation dialog. | confirmed: verified on MySQL 8.4 (grants are dropped while `automatic_sp_privileges = 1`, the default); the confirmation dialog states it |
| A22 | Connections are created, edited, tested and deleted in the UI (F2) and saved under a name. Their credentials are saved too: in a separate credential store `~/.config/dbcc/credentials.toml` (mode 0600), never in `config.toml`; profiles reference it with `password = "stored"`. Supersedes the "no stored password" rule of 3.6. | confirmed (2026-11-21) |
| A23 | The About page shows the web address https://www.datamedrix.com (A15). | confirmed |
| A24 | A result tab holds at most `page_size` rows (default 3,000, configurable in `[settings]`); after that the owner pages: next page, next 3,000 rows, and so on (`n`/F6, `p`). Moving back, or forward after the cursor was closed, re-runs a read statement and skips rows. Supersedes `max_rows`, `capped` and "load all" of 10.2 for the UI grid; export writes the whole result. | confirmed (2026-11-21) |
| A25 | Scratches work like DataGrip's scratch files: one file per scratch under `~/.local/state/dbcc/scratches/<connection>/`, named after its creation time (`scratch_YYYY-MM-DD_HH-MM-SS.sql`), saved automatically about 1 s after the last change; closing saves first and is refused when saving fails; Alt+O lists and re-opens scratches, Alt+E/Alt+R rename (names unique per connection, case-insensitive), Alt+D deletes. Supersedes the scratch-tab persistence of 9.1. | confirmed (2026-11-21) |
