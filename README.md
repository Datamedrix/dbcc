# dbcc — Database Command & Control

A keyboard-driven, full-screen terminal database client for **MySQL**, **MariaDB**, **PostgreSQL** and **Microsoft SQL Server**. One static binary, no runtime dependencies, works inside any SSH session. A product by Datamedrix GmbH.

[Deutsche Anleitung: docs/HILFE.md](docs/HILFE.md) · [Specification: docs/SPEC.md](docs/SPEC.md)

This README is the complete reference. It describes the behaviour as implemented in the code. Key names are written the way dbcc shows them in its help (`Alt+F1`, `Shift+F6`, `Ctrl+Space`).

## Contents

- [Features](#features)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Command line](#command-line)
- [Screen layout](#screen-layout)
- [Connections](#connections)
- [Paths and navigation](#paths-and-navigation)
- [Object tree](#object-tree)
- [Editor and scratches](#editor-and-scratches)
- [Running SQL](#running-sql)
- [Result grid](#result-grid)
- [Row form](#row-form)
- [Command line and commands](#command-line-and-commands)
- [Completion](#completion)
- [Transactions and safety](#transactions-and-safety)
- [Headless mode and scripts](#headless-mode-and-scripts)
- [Configuration](#configuration)
- [Key reference](#key-reference)
- [Writing your own commands in Lua](#writing-your-own-commands-in-lua)
- [Database specifics](#database-specifics)
- [Files and directories](#files-and-directories)
- [Troubleshooting](#troubleshooting)
- [About](#about)

---

## Features

- **Shell-style commands** for database objects: `cd`, `ls`, `pwd`, `ll`, `head`, `tail` (including `tail -f`), `wc`, `du`, `cat`, `edit`, `cp`, `mv`, `grep`, `ps`, `kill`, `export`, `history`, `help`.
- **SQL editor** with per-dialect syntax highlighting, tabs, auto-saved scratch files and on-demand completion (`Ctrl+Space`, never automatic).
- **Script runner** with a statement splitter per dialect (MySQL `DELIMITER`, PostgreSQL dollar quoting, SQL Server `GO n`); stops at the first error and reports the line.
- **Result grid** with paged lazy loading, horizontal scrolling (cells never wrap), sorting, three kinds of filters, a pivot view, cell detail and clipboard copy via OSC 52.
- **Row form** to insert and edit records, with NULL/untouched field states, a foreign-key lookup window and server errors mapped back to the offending field.
- **Every command is a Lua script.** Your own scripts add commands or replace the built-in ones.
- **Headless mode** for scripts and single commands without UI, with exit codes and table/CSV/TSV/JSON output.
- Multiple open connections, manual transactions, read-only and confirm-writes profiles, statement history.

Deliberately not included: no `rm`/drop command (deleting happens through SQL only), no row deletion from the grid, no Vim mode, no `$EDITOR` hand-off, no built-in SSH tunnel, no BLOB editing in forms.

---

## Installation

Requirements: Go as declared in `go.mod` (currently `go 1.27.3`). The build target is Linux amd64.

```sh
git clone <repository> dbcc && cd dbcc
make build          # bin/dbcc (CGO_ENABLED=0 GOOS=linux GOARCH=amd64, -trimpath, -ldflags "-s -w")
make static-check   # verifies that bin/dbcc is statically linked
make test           # unit tests
make test-servers   # start MySQL, MariaDB, PostgreSQL and SQL Server test containers (docker)
make itest          # integration tests against those servers
```

`make build` embeds the version (`git describe`) and the build date. The result is a single static file; copy it to the target host and run it.

---

## Quick start

```sh
bin/dbcc                 # start the UI; press F2 to create a connection
bin/dbcc shop-dev        # start and connect to the profile shop-dev
```

1. Press `F2`, then `n`, enter host, user and password, press **Test**, then **Save & connect**.
2. The object tree on the left shows all databases. Select a table and press `Enter` to see its data, `F3` for its structure.
3. Type SQL in the editor and press `F5` (statement at cursor) or `F6` (whole buffer or selection).
4. Press `F9` for the command line: `cd shop`, `ls`, `head customers 20`, `tail -f events`, `cat orders`, `help`.
5. `F1` shows every key of the focused pane. `F10` quits.

A minimal `~/.config/dbcc/config.toml` written by hand:

```toml
[profiles.shop-dev]
driver   = "mysql"
host     = "127.0.0.1"
user     = "anton"
database = "shop"
password = "prompt"
```

---

## Command line

```text
dbcc [profile]                          start the UI, optionally connect
dbcc -p <profile> -f <file.sql>         run a script without UI
dbcc -p <profile> -c "<command|SQL>"    run one command or statement without UI
dbcc -p <profile> < file.sql            stdin is not a terminal: run without UI
```

| Option | Meaning |
|---|---|
| `-p <profile>` | Connection profile. Alternatively the single positional argument (`dbcc prod`); both with different names is an error. |
| `-f <file>` | Run a SQL script without UI. Cannot be combined with `-c`. |
| `-c "<text>"` | Run one command (first word is a registered command) or SQL without UI. |
| `--format table\|csv\|tsv\|json` | Output format of results in headless mode (default `table`). |
| `--yes` | Answer confirmations with yes (headless). |
| `-v` | Headless: log every statement with line, row count and duration to stderr. |
| `--no-splash` | Skip the splash screen. |
| `--no-user-commands` | Load only the embedded commands (no scripts from `~/.config/dbcc/commands`). |
| `--export-commands <dir>` | Write the embedded command scripts to a directory and exit. |
| `--config <file>` | Use another configuration file instead of `~/.config/dbcc/config.toml`. |
| `--version` | Print the version (`dbcc <version>`). |
| `--licenses` | Print the licence notices of the Go standard library and of every bundled third-party module, and exit. |
| `-h`, `--help` | Usage. |

Headless mode is selected when `-f` or `-c` is given **or stdin is not a terminal**. Exit codes: `0` success, `1` SQL or command-script error, `2` usage, configuration or connection error. See [Headless mode](#headless-mode-and-scripts).

**Splash screen.** When the UI starts, a splash screen shows for 3 seconds: the Datamedrix logo as text art, the title `dbcc`, "Database Command & Control", version and build date, "A product by Datamedrix GmbH", contact, e-mail and web address. Any key dismisses it. Configuration, Lua commands and connecting already run behind it; a password prompt appears after it. Switch it off with `splash = false` or `--no-splash`. There is no splash in headless mode.

---

## Screen layout

```text
+ prod -----------------+-[scratch 2026-11-21 09:14]-[calc_total*]--- Editor +
| shop                  | SELECT c.id, c.name                                 |
|   Tables (12)         |   FROM customers c                                  |
|     customers         |  WHERE c.city = 'Berlin';                           |
|       Columns         |                                                     |
|       Keys            +- 1 customers │ 2 SELECT c.id… │ Messages ----------+
|     orders            | id ▲1 | name          | city     | …                |
|   Views (3)           |     1 | Müller GmbH   | Berlin   |                  |
|   Procedures (5)      |     2 | …             | …        |                  |
| crm                   |                                                     |
| information_schema    |                                                     |
+-----------------------+-----------------------------------------------------+
| /prod/shop > _                                                 command line |
|  prod | autocommit | rows 1–3,000 · page 1 + | 38 ms | sort: id ▲           |
| 1Help 2Conn 3View 4Edit 5Reload 7New 8Cancel 9Cmd 10Quit                    |
+-----------------------------------------------------------------------------+
```

- **Object tree** on the left (32 columns). Its title shows the current connection, plus the filter while filtering.
- **Editor** at the top right with a tab bar; `*` after a title marks unsaved changes.
- **Result area** at the bottom right with tabs: one tab per result, titled `N title`, plus the **Messages** tab, which is always last. `*` after a result title means *partial* or *capped* (see [Result grid](#result-grid)).
- **Command line**: the prompt shows the current path (`/prod/shop > `).
- **Status line**, fields separated by `|`: profile (or `not connected`), `READ ONLY` for read-only profiles, transaction state (`autocommit`, `manual txn`, or bold `TXN OPEN`), `running… (F8 cancels)` while a statement runs, row count of the current result, duration, active sort (`sort: …`), active filters (`filter: …`), hints (`sorted within 3,000 loaded rows`, `skipping 6,000 rows…`, `last page`, …) and the last error.
- **Footer** with the F-key labels **of the focused pane**; it changes with the focus.

Focus: `Tab`/`Shift+Tab` cycle tree → editor → results → command line; jump directly with `Alt+F1` (tree), `Alt+F2` (editor), `Alt+F3` (results) and `F9` (command line).

**Full height (`F11`):** `F11` gives the focused pane – editor or results – the full height of the right column and hides the other one; `F11` again restores the split. Moving the focus to the hidden pane (`Tab`, `Alt+F2`, `Alt+F3`) restores the split automatically. In the tree or the command line `F11` shows a hint. Many terminals (e.g. GNOME Terminal) use `F11` for full screen; rebind it under `[keys.global]`, e.g. `maximize = "alt+m"`. A new result takes the focus only when the command line or the grid had it; working in the tree or the editor keeps the focus there.

Colours: on terminals with 256 colours or more dbcc uses the Datamedrix blue (#0069B4) for borders and the status line, below that the 16 basic colours, below 16 colours monochrome.

---

## Connections

### Connection manager (F2)

`F2` lists the saved connections: name, database type, `user@host:port/database` and flags (`read-only`, `manual txn`, `confirm writes`). Open connections are marked `● open`, the current one `● current`. Below the list are the paths of the config file and the credential store.

| Key | Action |
|---|---|
| `Enter` | Connect (same as `cd /<name>`). On "+ New connection…": create one. |
| `n` | New connection |
| `e` | Edit the selected connection |
| `c` | Copy the selected connection (suggested name `<name>-copy`) |
| `d` | Delete the connection and its stored password (asks first; an open connection is closed) |
| `x` | Disconnect an open connection; with an open transaction it asks: Commit, Roll back or Stay |
| `Esc` | Close |

### Creating and editing a connection

| Field | Meaning |
|---|---|
| Name | Becomes the first path element `/<name>`. Letters, digits, `-`, `_`, `.`; starts with a letter or digit; at most 64 characters; unique. |
| Database | MySQL / MariaDB, PostgreSQL or SQL Server. Switching the type sets the default port (3306, 5432, 1433) while the port is a default port or empty. |
| Host, Port | Host or socket is required. |
| Socket (optional) | Unix socket (MySQL/MariaDB: socket file; PostgreSQL: socket directory or socket file `…/.s.PGSQL.5432`). |
| User | User name. |
| Password | Masked input. With a stored password: leave empty to keep it. |
| Password storage | `save with the connection` (= `stored`), `ask when connecting` (= `prompt`), `environment variable` (= `env:NAME`), `~/.my.cnf` (= `mycnf`), `~/.pgpass` (= `pgpass`). |
| Env variable | Name of the environment variable (only for "environment variable"). |
| Default database | The database the connection lands on. |
| Read only, Autocommit, Confirm writes | See [Transactions and safety](#transactions-and-safety). |
| Driver options | Driver parameters as `key=value, key=value`, e.g. `tls=skip-verify` or `sslmode=require`. See [Configuration](#configuration). |

Buttons: **Test** (opens a trial connection and reports "Connection works (mysql)" or the error), **Save**, **Save & connect**, **Cancel**. `Tab`/`Shift+Tab` move between fields, `Enter` on a drop-down opens it, `Esc` cancels.

The profile is written to `config.toml` (its `[profiles.<name>]` section; everything else in the file, comments included, stays untouched); the password goes to the separate credential store `credentials.toml` (mode 0600). Choosing a password source other than `stored` removes a stored password. Changes to an open connection apply after reconnecting (F2, `x`, connect again).

### Password sources

| `password =` | Behaviour |
|---|---|
| `"prompt"` (or empty) | Asked when connecting (UI: masked input window; headless: on `/dev/tty`). |
| `"stored"` | Taken from `credentials.toml`. If nothing is stored yet, dbcc asks and stores the password after a successful connect. |
| `"env:NAME"` | The value of environment variable `NAME`; an error if it is not set. |
| `"mycnf"` | The `[client]` section of `~/.my.cnf`: `password`, and `user`, `host`, `port`, `socket` where the profile does not set them. |
| `"pgpass"` | The first matching line of `$PGPASSFILE` or `~/.pgpass` (host – default `localhost` –, port, database, user; `*` wildcards). |

A plain-text password in `config.toml` is rejected: the file does not load.

---

## Paths and navigation

Every location has a path `/connection/database[/schema]`:

| Level | Example | `ls` shows |
|---|---|---|
| root | `/` | the connection profiles (driver, host, database, `(current)`/`(open)`) |
| connection | `/prod` | the databases |
| database | `/prod/shop` | MySQL/MariaDB: the objects; PostgreSQL/SQL Server: the schemas |
| schema | `/pg/app/public` | the objects (PostgreSQL and SQL Server only) |

MySQL and MariaDB have no schema level.

`cd` changes the path: `cd shop`, `cd ..`, `cd /prod/shop`, `cd -` (previous path), `cd` alone (the default database of the current profile). Several connections can be open at once; `cd /other/db` switches between them. The tree always shows the current connection and follows the path. Switching to another connection lands on its current database (and, with schemas, its current schema); `cd ..` within the same connection stays on the level above.

Object names in commands are resolved against the current path: `obj`, `db.obj` (MySQL/MariaDB), `schema.obj` and `db.schema.obj` (PostgreSQL/SQL Server), with SQL quoting of the parts (`"…"`, `` `…` ``, `[…]`). Absolute or relative paths whose last element is the object work too: `/prod/shop/customers`, `/pg/app/public/orders`, `../crm/contacts` – also on another connection, which is opened when needed. If a name has no exact match, a unique case-insensitive match is used.

---

## Object tree

Modelled on the database explorer of DataGrip.

- **Top level:** every database the account of the current connection can see. System databases (MySQL: `information_schema`, `mysql`, `performance_schema`, `sys`; SQL Server: `master`, `model`, `msdb`, `tempdb`; PostgreSQL: template databases) come last and are dimmed.
- **PostgreSQL and SQL Server** add the schema level (system schemas last, dimmed). On PostgreSQL, expanding another database connects to it in the background.
- **Categories** with object counts: Tables, Views, Materialized views (PostgreSQL), Procedures, Functions, Triggers, Events (MySQL/MariaDB), Sequences (MariaDB, PostgreSQL, SQL Server). Empty categories are hidden.
- **Below a table:** Columns (name, type, markers `PK`, `FK`, `not null`, `auto`, `generated`), Keys, Foreign keys (with target `→ table (columns)`), Indexes, Triggers. A view expands into its columns.
- Nodes load when expanded, on the auxiliary connection, so tree and completion keep working while the main session runs a statement.

| Key | Action |
|---|---|
| `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` | Move |
| `Right` | Expand, or go to the first child |
| `Left` | Collapse, or go to the parent |
| `Enter` | Default action (table below) |
| `F3` | `cat` the object |
| `F4` | `edit`: source in an editor tab (not for tables) |
| `F5` | Reload the node (drops its cached metadata) |
| `F7` | New row for the selected table (row form) |
| `/` | Filter the tree by name; `Esc` in the filter clears it, `Enter` keeps it |
| `c` | Set the current path to the selected node (`cd`) |

| Node | Enter | F3 | F4 | F7 |
|---|---|---|---|---|
| Database, schema, category, group | expand / collapse | – | – | – |
| Table | `ll`: data in the grid | `cat`: structure | – | new row |
| View, materialized view | `ll`: data in the grid | `cat` | `edit` | – |
| Procedure, function, trigger, event | `edit` | `cat`: source | `edit` | – |
| Column, key, foreign key, index | – | `cat` of the table | – | – |

The tree and the current path are independent: opening an object from the tree uses its full path and does not change the current path. Only `c` sets the path; conversely, `cd` moves the tree selection to the new path. All tree actions invoke the Lua commands by name, so a replaced command changes the tree's behaviour too.

With `mouse = true`: click selects, double click acts like Enter, the wheel scrolls. Hold `Shift` to select text in the terminal.

---

## Editor and scratches

### The editor

A simple, non-modal multi-line editor with Readline-style keys, line numbers, selection, undo/redo and bracketed paste (pasted text is inserted as one undo step and never interpreted as keys). Highlighting follows the dialect of the current connection (lexer `mysql`, `postgresql` or `tsql`). The statement at the cursor – what `F5` would run – is subtly marked.

Editing keys (as shown by F1 in the editor):

| Key | Action |
|---|---|
| `Left`/`Right`, `Ctrl+B`/`Ctrl+F` | Character left / right |
| `Alt+B`/`Alt+F`, `Ctrl+Left`/`Ctrl+Right` | Word left / right |
| `Up`/`Down`, `Ctrl+P`/`Ctrl+N` | Line up / down |
| `Home`/`End`, `Ctrl+A`/`Ctrl+E` | Line start / end (`Home` toggles first non-blank) |
| `PgUp`/`PgDn` | Page up / down |
| `Ctrl+Home`/`Ctrl+End`, `Alt+<`/`Alt+>` | Buffer start / end |
| `Shift`+movement | Extend the selection |
| `Alt+A` | Select all |
| `Ctrl+Shift+C` (also `Ctrl+C`) | Copy the selection to the clipboard |
| `Ctrl+Shift+X` (also `Ctrl+X`) | Cut the selection to the clipboard |
| `Ctrl+Shift+V` | Paste: the terminal's paste; otherwise the last copied text |
| `Ctrl+W` | Kill word backward, or cut the selection |
| `Ctrl+K` | Kill to end of line (at the end of a line: join lines) |
| `Ctrl+U` | Kill to line start |
| `Alt+D` | Kill word forward |
| `Alt+Backspace` | Kill word backward |
| `Ctrl+Z`, `Ctrl+_` | Undo |
| `Alt+Z` | Redo |
| `Ctrl+D`, `Delete` | Delete character |
| `Backspace` | Delete character backward |
| `Enter` | New line, keeping indentation |
| `Alt+I` | Insert indentation |

Killed, copied and cut text goes into an internal buffer and also, through OSC 52, to the terminal's clipboard. `Tab` does not indent; it moves the focus.

Many terminals handle `Ctrl+Shift+C`/`Ctrl+Shift+V` themselves (copying the terminal selection, pasting through bracketed paste). When the keys reach dbcc they act on the editor. Legacy terminals send `Ctrl+Shift+X` as `Ctrl+X`, which cuts as well. **`Ctrl+C` never quits dbcc**: in the editor it copies; elsewhere the status line says "Ctrl+C does not quit dbcc — F10 quits".

### Editor tabs

| Key | Action |
|---|---|
| `Alt+,` / `Alt+.` | Previous / next tab |
| `Alt+N` | New scratch |
| `Alt+O` | Open a scratch (list of all scratches) |
| `Alt+E` | Rename the current scratch |
| `Alt+X` | Close the tab |
| `Alt+R` | Object tab: restore the source as loaded |

There are two kinds of tabs:

- **Scratch tabs** for your own SQL (below).
- **Object tabs**, opened by `edit` (tree `Enter`/`F4`, or the command line): one per object, titled with the object name. Opening an object that already has a tab focuses it.

### Scratches

Scratches work like DataGrip's scratch files:

- Each scratch is a file under `~/.local/state/dbcc/scratches/<connection>/`, named after its creation time: `scratch_YYYY-MM-DD_HH-MM-SS.sql` (`-2`, `-3`, … on a clash). Scratches without a connection live in the folder `_none`. A scratch belongs to the connection that was current when it was created.
- The tab title is `scratch 2026-11-21 09:14`, or the name you gave it.
- Saved automatically about 1 second after the last change, and on exit.
- `Alt+X` saves before closing; if saving fails the tab stays open and a message explains why. Closing the last tab creates a new empty scratch – there is always at least one tab.
- On the next start the scratch tabs that were open are restored with their cursor position (index `open.json`).
- **Rename** (`Alt+E`, or `Alt+R` in the scratch list): 1–100 characters, no `/`, `\` or control characters, not starting with `.`; `.sql` is appended. Names are unique per connection, case-insensitively. On an error the dialog stays open.

**Scratch list (`Alt+O`):** every scratch of every connection, newest first, with connection, modification time, `● open` and a preview of its first line. The search field filters by connection, name and content.

| Key | Action |
|---|---|
| typing | Filter |
| `Up`/`Down`/`PgUp`/`PgDn` | Move |
| `Enter` | Open |
| `Alt+R` | Rename |
| `Alt+D` | Delete (asks first; cannot be undone) |
| `Esc` | Close |

### Object tabs and replace scripts

`edit` loads the current source of a view, procedure, function, trigger, event or materialized view as a **runnable script that replaces the object**:

| Dialect | Script |
|---|---|
| MariaDB | `CREATE OR REPLACE …` |
| MySQL | View: `CREATE OR REPLACE VIEW`; routine, trigger, event: `DROP … IF EXISTS` followed by `CREATE …` |
| MySQL, MariaDB | Bodies of routines, triggers and events are wrapped in `DELIMITER` lines, so *Run all* works on the script unchanged |
| PostgreSQL | View, function, procedure (every overload): `CREATE OR REPLACE`; trigger: `CREATE OR REPLACE TRIGGER` on PostgreSQL 14+, otherwise (and for constraint triggers) `DROP TRIGGER IF EXISTS … ON table` plus `CREATE`; materialized view: `DROP MATERIALIZED VIEW IF EXISTS`, `CREATE`, its indexes and its comment |
| SQL Server | GO batches: a guard batch that stops with `THROW` unless the session is in the object's database; `SET ANSI_NULLS`/`QUOTED_IDENTIFIER OFF` where the module was created that way; the module text with `CREATE OR ALTER` and `[schema].[name]` (before SQL Server 2016: a stub plus `ALTER`); finally the options back `ON` |

*Run all* (`F6`) in an object tab replaces the object. It counts as a write: blocked in read-only profiles, and with `confirm_writes` it asks ("Replace X on prof?"). **On MySQL it always asks**, also without `confirm_writes`, with the note that `DROP` + `CREATE` is not atomic and that privileges granted on the routine are removed while `automatic_sp_privileges = 1`, the default (MariaDB replaces with `CREATE OR REPLACE` and keeps them). After a successful run the tab is marked saved and the log says "replaced \<name\>". `Alt+R` restores the source as loaded (as an undoable step). Closing a changed object tab, or quitting, asks first.

---

## Running SQL

### Statement and script

| Key (editor) | Action |
|---|---|
| `F5` | **Run statement**: the statement at the cursor |
| `F6` | **Run all**: the selection, or the whole buffer of the current tab if nothing is selected |
| `F8` | Cancel the running statement |

SQL can also be typed in the command line: if the first word is not a command, the whole line runs as a SQL script (several statements separated by `;` are fine).

- Statements run sequentially on the **main session** of the current connection – a pinned connection, never a pool – so session state (current database, variables, temporary tables, transactions) persists.
- **The run stops at the first error.** The message log and the status line show the error, the statement and its line number (`line 12: …`); the editor cursor jumps to that line and the Messages tab is shown. When a selection runs, line numbers refer to the whole buffer.
- Every statement adds a **message-log** entry: time, line, shortened text, `N rows`, `N rows affected` or `ok`, or `ERROR …`, duration, notes and server warnings (MySQL/MariaDB: `SHOW WARNINGS` when the warning count is non-zero; PostgreSQL: NOTICE/WARNING messages). The log keeps the last 5,000 lines and scrolls horizontally.
- **Each result set gets its own tab.** Procedures returning several result sets (MySQL `CALL`, SQL Server batches with `EXEC` or several `SELECT`s) produce several tabs.
- The last statement of a run that returns exactly one result set stays **lazily loaded** (paged, see [Result grid](#result-grid)). All other results are buffered up to `max_rows_buffered` (default 10,000); a result cut at that limit is *capped*.
- No whole-script rollback is promised; on MySQL/MariaDB DDL commits implicitly.

### Statement splitting

| Dialect | Rules |
|---|---|
| MySQL / MariaDB | Delimiter `;`. `DELIMITER <token>` lines exactly as the `mysql` client handles them: only at the start of a line and only while the current statement has no code yet; the line is never sent to the server; the argument is unquoted up to the first blank, or quoted with `'…'`, `"…"`, `` `…` ``. Strings `'…'` and `"…"` with backslash escapes and doubled quotes, `X'…'`, `B'…'`, `N'…'`; identifiers `` `…` ``. Comments `#`, `-- ` (followed by whitespace), `/* */`. Executable comments `/*! … */`, `/*!80000 … */`, `/*M! … */` are passed through and never split. |
| PostgreSQL | Delimiter `;`, except inside parentheses (`CREATE RULE … DO (…; …)`) and inside a `BEGIN ATOMIC … END` body. Dollar quoting `$tag$ … $tag$` and `$$ … $$`, `E'…'` with backslash escapes, `'…'` without backslash escapes, `B'…'`, `X'…'`, `N'…'`, `U&'…'`; identifiers `"…"`; comments `--` and nested `/* */`. psql meta-commands (`\set`, …) are not supported. |
| SQL Server | Batches are separated by a line `GO` (case-insensitive) with an optional repeat count: `GO 5` runs the batch five times. `;` does not split batches. `GO 0` is an error. Strings `'…'`/`N'…'`, identifiers `[…]` and `"…"`, comments `--` and nested `/* */`. |

Comments on the line of the previous delimiter (`SELECT 1; -- note`) belong to that statement, so the next statement's line number points at real code.

**Statement at the cursor on SQL Server:** within the GO batch at the cursor, statements are bounded by `;` and by blank lines – outside strings, comments, parentheses and `BEGIN … END`/`CASE … END` blocks. A batch that defines a module (`CREATE`/`ALTER PROCEDURE`, `FUNCTION`, `TRIGGER`, `VIEW`, `SCHEMA`, `DEFAULT`, `RULE`) always runs whole. A statement starting with `ELSE` is joined to the one before it.

### Cancelling

`F8` cancels the running statement: MySQL/MariaDB send `KILL QUERY <id>` on the auxiliary connection; PostgreSQL and SQL Server cancel through the driver (cancel request, TDS attention). In both cases the main session and its transaction stay usable. On a following result (`tail -f`) `F8` also stops following.

### Open-cursor rule

A lazily loaded result holds the main session. Any new statement first closes that cursor; its tab keeps the rows loaded so far (its current page) and is marked *partial*. Paging on such a tab re-runs the statement (see [Pages](#pages-and-lazy-loading)).

---

## Result grid

### Basics

- Fixed header row, cell cursor, **no wrapping, ever**. Wide results scroll horizontally; `Left`/`Right` move by column.
- Column width is computed from the header and the first 200 loaded rows, capped at `max_column_width` (default 40). Longer values are cut with `…`; line breaks inside a value show as `↵`, tabs as a blank.
- `NULL` is rendered dimmed as `NULL`, binary values as `<BLOB n bytes>`. Numbers are right-aligned.
- Navigation: arrows, `PgUp`, `PgDn`, `Home`, `End`.

### Result origin

- **table-origin**: produced by `ll`, `head`, `tail`, or by opening a table or view from the tree. The grid knows the object and its metadata: sorting and filtering run on the server, the free `WHERE` and row editing are available.
- **free**: everything else. Sorting and filtering apply locally to the loaded rows of the current page.

### Pages and lazy loading

- A result tab holds at most **one page** of `page_size` rows (default 3,000, at least 10). Within the page rows are fetched in chunks of `fetch_size` (default 500) when the cursor approaches the end of the loaded rows.
- When the page is full the cursor stays open. **`n` or `F6`** shows the next page: the rows of the current page are dropped (their memory is released) and the next page is read from the open cursor. On the last page the status line says `last page`.
- **`p` or `Shift+F6`** shows the previous page. A cursor cannot read backwards: the statement is re-run with the same arguments, the rows before the page are read and discarded (hint `skipping 6,000 rows…`, `F8` cancels), then the page is loaded. This works only for statements the dialect classifies as read queries, otherwise: `previous pages are not available: the statement is not a read query`. On page 1 the status line says `first page`.
- Moving forward after the cursor was closed by the open-cursor rule re-runs and skips the same way (for non-read statements: `further pages are not available: …`).
- Status line: `42 rows` for a complete one-page result; `1,204+ rows` while more can be loaded; with pages `rows 3,001–6,000 · page 2 +` (`+`: a next page may exist), plus `· partial` after the cursor was closed; a buffered result cut at the limit reads `10,000 rows capped`.
- Headless output has no pages and prints every row.

### Sorting

| Key | Action |
|---|---|
| `s` | Cycle the current column ascending → descending → off (replaces other sort keys) |
| `S` | Add the current column as a further sort key (or cycle it) |

The header shows direction and rank: `name ▲1`, `city ▼2`.

- **table-origin:** the grid runs `SELECT * FROM obj [WHERE …] ORDER BY …` and restarts at page 1.
- **free:** the loaded rows of the current page are sorted locally, type-aware, NULLs first; hint `sorted within N loaded rows`. A sort over the whole result needs `ORDER BY` in the statement.

### Filtering

Three kinds, combined with AND, each visible in the status line (`filter: search "foo" AND city starts with "B" AND (id > 3)`) and removable one by one:

| Key | Filter |
|---|---|
| `/` | **Quick search**: one text matched against all columns (contains, case-insensitive). Empty input removes it. |
| `f` | **Column filter** on the current column. Operators (selectable by digit): `1` contains, `2` starts with, `3` `=`, `4` `<>`, `5` `<`, `6` `<=`, `7` `>`, `8` `>=`, `9` is NULL, `0` is not NULL. Operators with a value then ask for it; for `=`, `<>`, `<`, `<=`, `>`, `>=` the input starts with the text value of the current cell. |
| `w` | **Free `WHERE`**: an expression you type. Table-origin results only. Empty input removes it. `Ctrl+Space` (or the alternative completion key) opens a list of the table's columns, keywords and functions; `Up`/`Down` choose, `Enter`/`Tab` accept, `Esc` closes only the list. The list never opens by itself. |
| `x` | Remove filters and sorting: with one active it is removed; with several a menu appears (`1`–`9` for an entry, `a` for all). |

Execution:

- **table-origin:** everything becomes one server-side `WHERE`; the result restarts at page 1. Quick search and column filters use bound parameters (contains through the dialect's text-search predicate, starts with as `LIKE … ESCAPE '!'`, comparisons with a parameter). The free expression is inserted verbatim in parentheses; if the server rejects it, the previous result stays and the error goes to the message log. A quick search on a large table is a full scan; `F8` cancels.
- **free:** quick search and column filters apply locally to the loaded rows of the page, hint `filtered within N loaded rows (M match)`. contains/starts with ignore case; comparisons are type-aware (numeric for numeric columns); `NULL` matches only `is NULL`. A free `WHERE` is rejected: `free WHERE is only available for table-origin results`.

While a re-query runs, further sort or filter changes are rejected with `a re-query is still running`.

### Pivot view

`F3` toggles between the normal and the transposed grid. Transposed: one row per attribute, one column per record. The attribute names stay fixed on the left; horizontal scrolling moves through the records of the page (loading more as needed); `n`/`F6`/`p` page as usual. The record header shows the primary-key value for table-origin results, otherwise the row number within the whole result. Record columns share the width `pivot_column_width` (default 30). The cursor keeps pointing at the same record and attribute when toggling. Sorting, filtering, cell detail, copy, edit and new-row work in both views and always refer to records.

### Cell detail and copy

- `Enter` opens the **cell detail**: the full value, wrapped and scrollable. JSON objects and arrays are pretty-printed, binary values shown as size plus a hex dump (at most 64 KiB). In the detail window `c` copies the value, `Esc` or `Enter` closes.
- `c` copies the current cell (the full value as in the detail), `C` the current row (values separated by tabs). Copying uses **OSC 52**, which works over SSH where the terminal (and tmux) allows it.

### More grid actions

| Key | Action |
|---|---|
| `F5` | Re-run (table-origin with an active sort/filter: with that state; otherwise the original statement with its arguments) |
| `F4` | Edit the record under the cursor (row form; table-origin only) |
| `F7` | New row (row form; table-origin only) |
| `[` / `]` | Previous / next result tab |
| `Alt+X` | Close the result tab and release its memory. The Messages tab always stays. |
| `Alt+Q` | Close all result tabs (Messages stays) |

### Following results (`tail -f`)

A result opened with `tail -f` appends new rows at the bottom automatically. It keeps at most `page_size` rows; older rows are dropped from the front. The status line shows e.g. `rows 1,201–4,200 · following · 12 new rows · every 2 s`, afterwards `following stopped`. With the cursor on the last row it moves to the new last row. Paging does not apply (`n`/`p`/`F6` say so). Following stops when the tab is closed, on `F8` with that tab current, when a sort or filter re-queries it, when the connection closes or dbcc quits; an error (e.g. the table was dropped) stops it too and goes to the message log.

### Exporting a result

`export <file.csv|file.json>` writes the **whole** active result, not just the page in memory. See [`export`](#export).

---

## Row form

One form for inserting new rows and editing existing ones.

- **New row:** `F7` in the tree on a table, or `F7` in a table-origin result.
- **Edit:** `F4` in a table-origin result, for the record under the cursor.

Read-only profiles disable forms ("read-only profile: forms are disabled"). Free results have no form ("forms need a table-origin result (ll, head, or the tree)").

**Editing requires** that the record can be identified: the table needs a primary key or a unique key whose columns are all present in the result and not NULL in this row. Views cannot be edited. Otherwise a notice explains why.

### Fields

One field per column, labelled with name and declared type, markers `PK` (primary key) or `UK` (unique key), `NN` (NOT NULL), `FK` (foreign key), `DF` (has a default), plus a description of the default.

| Column kind | Field |
|---|---|
| auto-increment, identity, generated | read-only, shown as `auto` |
| `ENUM` | drop-down |
| `SET` | one check box per member |
| boolean (`bool`, `boolean`, `tinyint(1)`, `bit(1)`) | check box |
| foreign key | read-only display of key plus first text column of the referenced row; `F4`/`Enter` opens the lookup window |
| `TEXT`, `JSON` and similar (`tinytext`…`longtext`, `ntext`, `jsonb`, `xml`, `citext`, …) | one-line preview; `F4`/`Enter` opens a multi-line editor |
| `BLOB`, binary, spatial types, `bit(n>1)` | not editable |
| everything else | text input |

Field states: **untouched**, **value** (including the empty string) and **NULL**.

### Keys in the form

| Key | Action |
|---|---|
| `F2` | Save (and close) |
| `F6` | Save, then a new empty form for the same table |
| `F4` | Lookup window (foreign key), multi-line editor (text/JSON and text fields) or drop-down (ENUM) |
| `Ctrl+N` | Set NULL (all columns of a composite foreign key) |
| `Ctrl+U` | Reset the field to untouched |
| `Esc` | Cancel; with changes it asks "Discard changes? (y/n)" |
| `Up`/`Down`, `Tab`/`Shift+Tab` | Previous / next field |
| `PgUp`/`PgDn` | 10 fields back / forward |
| `Enter` | text field: next field; ENUM: open the list; FK/text: lookup or editor |
| `Space`/`Enter` | toggle a boolean; SET: toggle the member |
| `Left`/`Right` | SET: choose the member; ENUM: cycle the value |

In the multi-line editor `F2` or `F4` apply the text, `Esc` discards it. In the ENUM list `Enter` chooses, `Esc` closes.

### Generated statements

- **Insert:** only fields that are not untouched appear in the column list, so the database default applies to untouched fields. With every field untouched: `INSERT INTO t () VALUES ()` (MySQL/MariaDB) or `INSERT INTO t DEFAULT VALUES`. Bound parameters throughout.
- **Update:** only changed fields in `SET`, `WHERE` on the original key values. Without changes: "No changes". If the update does not affect exactly one row, a prominent warning appears (`UPDATE affected 0 rows, expected 1 …`).
- Every statement goes to the message log with its parameters. In manual-transaction profiles it becomes part of the open transaction. The read-only guard and `confirm_writes` apply as for any statement. After a successful save the grid is re-queried.

### Validation and errors

**The database validates**; the form does not pre-check anything. On a server error the form stays open; dbcc maps the error to the column or constraint, marks the field (or all columns of the constraint), moves the cursor there and shows the server message. Covered are, among others: NOT NULL violated, no default for field, data too long, out of range, incorrect value for column, duplicate key, foreign-key failure, check-constraint violation (MySQL and MariaDB error numbers distinguished), value for a generated column.

### Foreign-key lookup window

A grid of the referenced table with all columns and a filter line: typing searches on the server (quick search across all text columns), at most 1,000 rows, lazily loaded. `Up`/`Down`/`PgUp`/`PgDn`/`Left`/`Right` move, `Enter` selects the row, `Esc` closes. A composite foreign key fills all its fields at once. The field then shows the key plus the first text column of the referenced row.

---

## Command line and commands

### The command line

`F9` focuses the command line. The first word is looked up in the command registry (exact, case-sensitive). If it is not a command, the whole line runs as SQL. So `KILL 12` (upper case) stays SQL while `kill 12` runs the command; T-SQL `DBCC …` statements always go to the server (a command named `dbcc` is not allowed).

| Key | Action |
|---|---|
| `Enter` | Run the command or SQL |
| `Esc` | Clear the line and go to the editor |
| `Up` / `Down` | Previous / next entry of the command history (a ring that includes the empty line). The history is kept across restarts in `~/.local/state/dbcc/cmdline_history` (last 1000 lines; lines containing `IDENTIFIED BY` or `PASSWORD` are not written to disk). |
| `Ctrl+Space` (or `Ctrl+O`) | Completion list; while open it has the focus: `Up`/`Down`/`PgUp`/`PgDn` select, `Enter`/`Tab` accept, `Esc` closes only the list, typing filters |

Arguments are split like a shell: `'…'` is literal, in `"…"` the escapes `\"` and `\\` work, a backslash outside quotes escapes the next character. A trailing `;` after a command is tolerated (`pwd;`).

Flags come before positional arguments: `-a`, `-t value`, `-t=value` (also with `--`); `--` ends the flags; `-` alone and negative numbers are positional. A wrong invocation reports the command's usage.

Commands that write (`cp`, `mv`, `kill`) show the SQL before running it, ask for confirmation and are blocked in read-only profiles.

### Command overview

| Command | Purpose | Writes |
|---|---|---|
| [`cd [path]`](#cd) | Change the current path | no |
| [`ls [path]`](#ls) | List a path level | no |
| [`pwd`](#pwd) | Print the current path | no |
| [`ll <table\|view>`](#ll) | All rows in the grid | no |
| [`head <table\|view> [n]`](#head) | First n rows | no |
| [`tail [-f] [-i seconds] [-k column] <table\|view> [n]`](#tail) | Last n rows, optionally follow | no |
| [`wc <table\|view>`](#wc) | Count rows | no |
| [`du [table]`](#du) | Data and index size | no |
| [`cat [-t kind] <object>`](#cat) | Structure or source | no |
| [`edit [-t kind] <object>`](#edit) | Source as a replace script in the editor | no |
| [`cp [-a] <src> <new>`](#cp) | Copy a table | yes |
| [`mv <table\|view> <new-name>`](#mv) | Rename | yes |
| [`grep <pattern> [path]`](#grep) | Search names and sources | no |
| [`ps`](#ps) | Running sessions | no |
| [`kill [-q] <id>`](#kill) | Terminate a session | yes |
| [`export [-f] <file.csv\|file.json>`](#export) | Write the active result to a file | no |
| [`history [n]`](#history) | Statement history | no |
| [`reload`](#reload) | Reload the commands (install your own scripts at run time) | no |
| [`help [command\|about]`](#help) | Help, About page | no |

Objects can be given as name, qualified name or path (see [Paths and navigation](#paths-and-navigation)). Commands that show data (`ll`, `head`, `tail`, `wc`) accept tables, views and materialized views.

### cd

```text
cd [path]
```

| Call | Effect |
|---|---|
| `cd shop` | relative: one level down |
| `cd ..` | one level up |
| `cd /prod/shop`, `cd /pg/app/public` | absolute |
| `cd ../crm` | combination |
| `cd -` | previous path |
| `cd /` | root (connections stay open) |
| `cd` | the current profile's default: `/<profile>[/<database>]` |

Leniency: on the database level of MySQL/MariaDB, `cd other` means the sibling database (`../other`). On the schema level (PostgreSQL, SQL Server), `cd other` means a sibling schema first, else a sibling database.

A database change uses `USE` (MySQL/MariaDB, SQL Server) or reconnects transparently (PostgreSQL). With an open manual transaction dbcc asks first: Commit, Roll back or Stay. Entering a database on PostgreSQL/SQL Server lands on its current schema. On PostgreSQL a schema change sets `search_path` to `schema, public`; on SQL Server the schema is only validated (see [Database specifics](#database-specifics)).

### ls

```text
ls [path]
```

Lists the level of the path (default: current) with the columns `name`, `kind`, `comment`: at the root the profiles (driver, host:port or socket, database, `(current)`/`(open)`), below a connection its databases, below a database its objects – or, with schemas, its schemas –, below a schema its objects. System objects are marked `(system)`.

### pwd

```text
pwd
```

Prints the current path.

### ll

```text
ll <table|view>
```

`SELECT * FROM <object>` in the grid as a table-origin result (server-side sort/filter, row form).

### head

```text
head <table|view> [n]
```

The first `n` rows (default 10) as a table-origin result, limited the dialect's way (`LIMIT n` or `TOP (n)`).

```text
head customers 50
head /prod/crm/contacts
```

### tail

```text
tail [-f] [-i seconds] [-k column] <table|view> [n]
```

The last `n` rows (default 10) by the primary key (all its columns) or by `-k column`, shown in **ascending** order, as a table-origin result: `SELECT * FROM (<last n by key descending>) AS dbcc_tail ORDER BY <key>`. A view, or a table without primary key, needs `-k`.

| Flag | Meaning |
|---|---|
| `-k column` | Order key instead of the primary key |
| `-f` | Follow: rows whose key is greater than the last one seen are appended as they are inserted |
| `-i seconds` | Poll interval for `-f` (default 2, at least 0.5; only with `-f`) |

`tail -f` follows **inserts only, in key order**: updated or deleted rows, and rows committed later with a smaller key, are not shown. It needs a single-column key (use `-k` for a composite primary key) and `n` of at least 1. Polling runs in the background on the **auxiliary connection**, never on the main session, so the UI stays usable. Grid behaviour: [Following results](#following-results-tail--f); headless: [Headless mode](#headless-mode-and-scripts).

```text
tail orders
tail -k created_at audit_log 100
tail -f -i 0.5 events
```

### wc

```text
wc <table|view>
```

`SELECT COUNT(*)`; result columns `rows` and `object`.

### du

```text
du [table]
```

Data and index size and row estimate of one table, or – without argument – of every table of the current database (or schema; on PostgreSQL/SQL Server without a schema: all user schemas, names qualified), largest first. Columns: `table`, `engine`, `rows (estimate)`, `data`, `index`, `total`, `free`, `total bytes`; with several tables a total line `(total N tables)`.

| Dialect | Source |
|---|---|
| MySQL / MariaDB | `information_schema.TABLES` (`DATA_LENGTH`, `INDEX_LENGTH`, `DATA_FREE`, `TABLE_ROWS`); on MySQL `information_schema_stats_expiry = 0` is set first so the numbers are current |
| PostgreSQL | `pg_table_size`, `pg_indexes_size`, `reltuples`; engine = access method. Reads the catalog of the current database only: `cd` into another database first |
| SQL Server | `sys.partitions` and `sys.allocation_units` (8 KiB pages); engine `clustered` or `heap`; any database through three-part names |

### cat

```text
cat [-t kind] <object>
```

For **tables** (and materialized views) a structured description in the style of MySQL `DESCRIBE`, extended with every definition and constraint – not DDL. Sections:

1. **Columns**: `#`, Field, Type, Null, Key (`PRI`, `UNI`, `MUL`), Default, Extra (auto_increment or the identity definition, `GENERATED ALWAYS AS (…) STORED|VIRTUAL`, `on update …`, a collation that differs from the table's), Comment.
2. **Primary and unique keys** with name and columns.
3. **Foreign keys**: name, columns, referenced table and columns, On update, On delete.
4. **Check constraints**: name, expression, Enforced.
5. **Indexes**: name, columns (with `DESC`, prefix length, expressions), Include and Filter where present (PostgreSQL, SQL Server), Unique, Type, Visible, Comment.
6. **Triggers**: name, timing, event.
7. **Table options**: engine, charset, collation, rows (estimate), auto_increment, row_format, create_options, partitioning and others, comment.

For materialized views keys, foreign keys, checks and triggers are left out and the definition follows at the end. For **views, procedures, functions, triggers and events** `cat` shows the highlighted source (read-only). Output appears in a horizontally scrollable tab.

`-t kind` chooses the kind when names are ambiguous: `table`, `view`, `procedure` (`proc`), `function` (`func`, `fn`), `trigger`, `event`, `materialized view` (`mview`). Without `-t` dbcc notes in the message log when objects of another kind share the name ("… is also a procedure; use: cat -t procedure …").

The layout comes from `cat.lua` and can be changed without recompiling.

### edit

```text
edit [-t kind] <object>
```

Opens a view, procedure, function, trigger, event or materialized view in an editor tab as a script that replaces it (*Run all* saves it). See [Object tabs](#object-tabs-and-replace-scripts). `-t` as for `cat`. Without UI the script is printed (`dbcc -p prod -c "edit calc_total"`).

### cp

```text
cp [-a] <src> <new>
```

Copies a table on the same connection. The new name may be `name`, `db.name` (MySQL/MariaDB), `schema.name`, `db.schema.name` or a path. dbcc shows all statements and asks first; the reported row count is that of the copying statement.

| Variant | MySQL / MariaDB | PostgreSQL | SQL Server |
|---|---|---|---|
| `cp src new` (data only) | `CREATE TABLE new AS SELECT * FROM src` | same | `SELECT * INTO new FROM src` |
| `cp -a src new` (structure + data) | `CREATE TABLE new LIKE src`, then `INSERT … SELECT` | `CREATE TABLE new (LIKE src INCLUDING ALL)`, own sequences for serial columns, `INSERT … [OVERRIDING SYSTEM VALUE] SELECT`, `setval` | `CREATE TABLE` generated from the metadata, `SET IDENTITY_INSERT … ON`, `INSERT … SELECT`, `… OFF`, then the remaining indexes |

`cp -a` copies columns, defaults, keys, indexes and checks, but **no foreign keys and no triggers**. Generated columns are left out of the `INSERT` column list. With a view as source, `cp view new` creates a snapshot table and `cp -a view new` a second view with the same definition.

Further limits of `cp -a`: PostgreSQL does not copy partitioning (a partitioned source becomes a plain table). SQL Server does not copy default-constraint names, XML, spatial and hash indexes, filegroups/partitioning, compression, system versioning or extended properties; disabled checks are added `WITH NOCHECK` and disabled.

### mv

```text
mv <table|view> <new-name>
```

Renames a table or view within its database or schema; the new name must be a plain name without `.` or `/`. MySQL/MariaDB: `RENAME TABLE`; PostgreSQL: `ALTER TABLE|VIEW|MATERIALIZED VIEW … RENAME TO`; SQL Server: `[db].sys.sp_rename`.

### grep

```text
grep <pattern> [path]
```

Searches object names, column names and the sources of views, routines, triggers and events in the current database or schema (or those of the path). Case-insensitive, literal (no wildcards). Result columns: `object`, `kind`, `match` (`name`, `column`, `source`), `detail` (column with type, or `line N: …` of the match).

On PostgreSQL only the catalog of the current database is read (`cd` first otherwise); without a schema every user schema is searched and names are qualified. SQL Server searches `sys.objects`, `sys.columns` and `sys.sql_modules` of any database.

### ps

```text
ps
```

Running sessions and their statements; `*` marks this session. Columns: `me`, `id`, `user`, `host`, `db`, `command`, `time`, `state`, `info`.

| Dialect | Source |
|---|---|
| MySQL | `performance_schema.processlist`, else `information_schema.PROCESSLIST` |
| MariaDB | `information_schema.PROCESSLIST` |
| PostgreSQL | `pg_stat_activity` (client backends only, with wait events) |
| SQL Server | `sys.dm_exec_sessions` with the running request and its SQL text (user sessions only) |

### kill

```text
kill [-q] <id>
```

Terminates the session with the given id (see `ps`); `-q` cancels only its running statement. Your own session is refused. MySQL/MariaDB: `KILL` or `KILL QUERY`; PostgreSQL: `pg_terminate_backend` or `pg_cancel_backend` (checks that the session exists); SQL Server: `KILL` – `-q` is not available there.

### export

```text
export [-f] <file.csv|file.json>
```

Writes the active result (the current result tab) to a file; the format follows the extension. A leading `~` is the home directory. An existing file is overwritten only with `-f`. On an error the half-written file is removed.

- The **whole** result is written, not just the page in memory: on the first page with the cursor still open, the loaded rows are written and the cursor is continued to the end; otherwise the statement is re-run from the start (with its arguments; read queries only) and every row is streamed to the file.
- An active **local filter** is applied while streaming. An active **local sort** first loads every row into memory (memory grows with the result).
- Progress messages in the message log every 10,000 rows.
- CSV: header row, NULL as an empty field. JSON: an array of objects, values as strings, NULL as `null`.

### history

```text
history [n]
```

The last `n` executed statements (default 100). In the UI a filterable list appears (newest first, with time, path and an `[error]` marker); `Enter` loads the statement into the current scratch tab, `Esc` closes. Without UI the list is printed (oldest first) with `#`, `time`, `profile`, `path`, `ms`, `outcome`, `statement`.

### reload

```text
reload
```

Reloads all commands – the built-in ones and the user scripts from `~/.config/dbcc/commands` (or `$XDG_CONFIG_HOME/dbcc/commands`) – without restarting dbcc; connections, transactions, tabs and results stay. This is how you install a new or changed script at run time: save the file, `F9`, `reload`. The message log reports `commands reloaded: N commands (user scripts from <dir>)`; a broken script is reported as `user command script skipped: <path>: <error>` and skipped (a built-in command it replaces then stays active). UI only; headless, every invocation loads the scripts anyway. Details: [Installing scripts at run time](#installing-scripts-at-run-time-reload).

### help

```text
help [command|about]
```

Without argument: every command with usage and description. `help <command>`: usage, description, flags, argument types, whether it writes, and its source (`embedded:<name>.lua` or the user script's path). `help about` opens the About page.

---

## Completion

The completion list opens **only** on `Ctrl+Space` or the alternative key (`completion_key_alt`, default `Ctrl+O`) – never automatically, no ghost text. It filters while you type; `Enter` or `Tab` accepts, `Esc` closes, `Up`/`Down`/`PgUp`/`PgDn` move. A single candidate is inserted directly. Identifiers are quoted only when necessary (not a plain name, a reserved word; on PostgreSQL also names with upper-case letters).

Candidates depend on the tokens before the cursor (token-level heuristic, no parser):

| Context | Candidates |
|---|---|
| After `FROM`, `JOIN`, `INTO`, `UPDATE`, `TABLE`, `DESCRIBE`, and in comma-separated FROM lists | Tables and views of the current database or schema, then databases (and schemas) |
| After `db.` (or `schema.`, `db.schema.`) in a table position | Objects of that database or schema |
| After `alias.` or `table.` | Columns of that table; aliases are resolved within the current statement (also those after the cursor) |
| After `CALL`, `EXEC`, `EXECUTE` | Procedures |
| Elsewhere inside a statement | Columns of the tables named in the statement, then keywords and functions of the dialect |
| Inside strings and comments | nothing |
| Command line, first word | Commands |
| Command line, arguments | By the declared argument type: objects, paths, profiles, files – also after flags (`cat -t view ` → objects) |
| Command line, flags | After `-` the command's flag names (`tail -` → `-f`, `-i`, `-k`); after a value flag its declared values (`cat -t ` → `table`, `view`, …) or values of its type; `--` ends the flags |
| Command line with SQL | As in the editor |

Metadata comes from a cache that is loaded per schema on first use and dropped after DDL statements and with `F5` in the tree. Note: DDL not yet committed in an open transaction of the main session (possible on PostgreSQL and SQL Server) is not visible in tree and completion until commit, because they read through the auxiliary connection.

---

## Transactions and safety

Per profile:

| Setting | Effect |
|---|---|
| `autocommit = true` (default) | Every statement commits. Status line: `autocommit`. |
| `autocommit = false` | Manual transactions. The first statement opens a transaction; `COMMIT` and `ROLLBACK` are plain SQL. Status line: `manual txn`, with an open transaction bold `TXN OPEN`. |
| `read_only = true` | The session is set read-only on the server where the dialect supports it (MySQL/MariaDB: `SET SESSION TRANSACTION READ ONLY`; PostgreSQL: `SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY`; SQL Server has no such setting). A client-side guard also rejects statements that are not read statements ("read-only profile: statement rejected"). Write commands, forms and *Run all* in object tabs are blocked. Status line permanently `READ ONLY`. |
| `confirm_writes = true` | Every statement that is not a read statement asks first ("Run this statement?"). Replacing an object asks too. |

The client-side guard is a token-level heuristic (first keyword; `SELECT … INTO`, `WITH … UPDATE`, `FOR UPDATE` and, on SQL Server, any `EXEC`, `INSERT` etc. anywhere in the batch count as writes). `SET`, `USE`, `BEGIN`, `COMMIT`, `ROLLBACK` count as reads. **The real protection is the server setting and the account's privileges.**

Manual transactions per dialect:

- **MySQL/MariaDB:** `SET autocommit = 0`. DDL commits implicitly; dbcc notes it in the message log ("this statement committed the open transaction implicitly") and resets the state.
- **PostgreSQL:** no session switch; dbcc sends `BEGIN` before the first statement of each transaction (note "BEGIN sent first (manual transaction)"). Not before transaction control, `SET`/`RESET`/`SHOW`/`DISCARD`/`LISTEN` and similar, or statements that cannot run in a transaction block (`VACUUM`, `CREATE/DROP DATABASE`, `… CONCURRENTLY`, `ALTER SYSTEM`, …). `SET TRANSACTION`, `SET LOCAL` and `SET CONSTRAINTS` do get a `BEGIN`. The state is read from the protocol; a failed transaction counts as open.
- **SQL Server:** `SET IMPLICIT_TRANSACTIONS ON`; the state is tracked through `@@TRANCOUNT`.

**Open transaction:** quitting (`F10`), `cd` to another database and disconnecting (`F2`, `x`) ask: **Commit**, **Roll back** or **Stay** (`Esc` = Stay). An open transaction survives navigation within the same database and switching between connections.

**Statement history:** every executed statement is appended with timestamp, profile, path, duration and outcome to `~/.local/state/dbcc/history.jsonl` (mode 0600). Statements containing `IDENTIFIED BY` or `PASSWORD` (any case) are **not** recorded.

---

## Headless mode and scripts

dbcc runs without UI when `-f` or `-c` is given or stdin is not a terminal. A profile (`-p`) is required; the start path is `/<profile>[/<database>]`.

```sh
dbcc -p shop-dev -f migrate.sql                  # script
dbcc -p shop-dev -c "ll customers" --format csv  # Lua command
dbcc -p shop-dev -c "SELECT now()"               # SQL
dbcc -p shop-dev < dump.sql                      # stdin
dbcc -p shop-dev -c "cp -a orders orders_bak" --yes
dbcc -p shop-dev -c "tail -f -i 1 events" --format json
```

- `-c`: if the first word is a registered command, the Lua command runs; otherwise the text is SQL (several statements allowed).
- **Output:** results go to stdout in `--format`: `table` (aligned, a blank line between results), `csv`, `tsv` (line breaks inside values are replaced), `json` (an array of objects). NULL is an empty field in CSV/TSV and `null` in JSON. There are no pages; all rows are streamed. Text output (`cat`, `edit`, `help`, `pwd`) is printed as is, without colour.
- **Messages** go to stderr: notes from commands, server warnings (`warning (line N): …`), implicit commits. With `-v` also every statement: `-- line N: <text>, 42 rows (12ms)`.
- **Errors** go to stderr: `ERROR at line N: <error>` followed by the indented statement, or `ERROR: <error>`.
- **Confirmations:** write commands fail without `--yes` ("confirmation required; run with --yes"). In `confirm_writes` profiles writing statements are refused without `--yes` ("cancelled by user").
- `history` prints the list, `edit` the replace script, `help about` the About text (with a pointer to `dbcc --licenses`). Primitives that need a pick list or the editor fail with "not available without UI".
- Password prompts use `/dev/tty`.
- `Ctrl+C` (SIGINT) cancels the running statement.
- **`tail -f` headless** prints the last n rows, then each new row as it arrives – `table` without repeating the header, `csv`/`tsv` records, `json` as **JSON Lines** (one object per line, no enclosing array) – until SIGINT or SIGTERM, then exits 0. An error that stops following (e.g. the table was dropped) exits 1. A headless following result keeps at most 3,000 rows in memory.

| Exit code | Meaning |
|---|---|
| `0` | success |
| `1` | SQL error or failure of a command script |
| `2` | usage, configuration or connection error (including wrong command arguments) |

---

## Configuration

File: `~/.config/dbcc/config.toml` (or `$XDG_CONFIG_HOME/dbcc/config.toml`, or `--config`). Without a file the defaults apply. If the file is accessible to group or others (wider than 0600), dbcc warns at start. dbcc writes new connections into this file itself (mode 0600) and leaves everything else in it unchanged.

### Example

```toml
[settings]
fetch_size         = 500
max_rows           = 100000
page_size          = 3000
max_rows_buffered  = 10000
max_column_width   = 40
pivot_column_width = 30
completion_key_alt = "ctrl+o"
mouse              = false
splash             = true

[profiles.shop-dev]
driver         = "mysql"          # mysql | postgres | mssql; MariaDB is detected at connect
host           = "127.0.0.1"
port           = 3306
# socket       = "/var/run/mysqld/mysqld.sock"
user           = "anton"
database       = "shop"
password       = "stored"         # prompt | stored | env:VAR | mycnf | pgpass
autocommit     = true
read_only      = false
confirm_writes = false

[profiles.shop-dev.params]
tls = "preferred"

[profiles.pg-prod]
driver    = "postgres"
host      = "db.example.org"
user      = "report"
database  = "app"
password  = "pgpass"
read_only = true

[profiles.pg-prod.params]
sslmode = "require"

[profiles.mssql]
driver   = "mssql"
host     = "sql01\\INST1"         # host\instance: leave out the port, the SQL Browser resolves it
user     = "sa"
password = "env:DBCC_MSSQL_PW"

[profiles.mssql.params]
TrustServerCertificate = "true"

[keys.grid]
pivot = "f3"

[keys.editor]
run_statement = "f5"
```

### `[settings]`

| Key | Default | Meaning |
|---|---|---|
| `fetch_size` | `500` | Rows per fetch chunk |
| `max_rows` | `100000` | Kept for compatibility; the UI grid pages instead (`page_size`), headless output streams everything |
| `page_size` | `3000` | Rows per result page in the grid; at least 10 (smaller values are a configuration error). Also the cap of a following result (`tail -f`) |
| `max_rows_buffered` | `10000` | Cap of buffered results: all but the last of a script, every set of a multi-result statement, `ctx:query` in Lua |
| `max_column_width` | `40` | Maximum column width in the grid |
| `pivot_column_width` | `30` | Width of record columns in the pivot view |
| `completion_key_alt` | `"ctrl+o"` | Second completion key (editor and command line); `""` disables it |
| `mouse` | `false` | Mouse support |
| `splash` | `true` | 3-second splash screen |

### `[profiles.<name>]`

| Key | Meaning |
|---|---|
| `driver` | `mysql` (also MariaDB), `postgres` or `mssql`. Required. |
| `host` | Host name or IP (default 127.0.0.1). SQL Server: also `host\instance`. |
| `port` | Defaults to 3306 / 5432 / 1433 when neither port nor socket is given. |
| `socket` | Unix socket (MySQL/MariaDB: file; PostgreSQL: directory or file `.s.PGSQL.<port>`). |
| `user` | User. |
| `database` | Default database (PostgreSQL without one: `postgres`). |
| `password` | Password **source**: `prompt` (default), `stored`, `env:NAME`, `mycnf`, `pgpass`. **Never the password itself.** |
| `autocommit` | Default `true`. |
| `read_only` | Default `false`. |
| `confirm_writes` | Default `false`. |
| `params` | Sub-table of driver parameters (below). |

Driver parameters (`[profiles.<name>.params]`):

| Dialect | Use |
|---|---|
| MySQL / MariaDB | `tls` becomes the driver's TLS setting (e.g. `true`, `false`, `skip-verify`, `preferred`); every other key is passed as a go-sql-driver DSN parameter. Connect timeout 10 s. |
| PostgreSQL | libpq keywords such as `sslmode`, `sslrootcert`, `connect_timeout` (default 10), `application_name` (default `dbcc`), `options`, `search_path`; unknown keys become run-time parameters. As with libpq, `PG*` environment variables apply to settings that are not given. |
| SQL Server | go-mssqldb DSN parameters, e.g. `encrypt = "disable"`, `TrustServerCertificate = "true"`, `ApplicationIntent = "ReadOnly"`, `"connection timeout" = "30"`; `app name` defaults to `dbcc`, `dial timeout` to 10. |

### Credential store

`~/.config/dbcc/credentials.toml`, mode 0600 (restricted to 0600 automatically when read with wider permissions), written atomically:

```toml
# dbcc credential store — keep private (mode 0600).

[profiles.shop-dev]
password = "…"
```

Profiles use it with `password = "stored"`. The connection manager (F2) maintains it; deleting a connection removes its password.

### Rebinding keys: `[keys.<pane>]`

Every binding can be changed: `action = "key"`. Panes: `global`, `tree`, `editor`, `grid`, `form`, `cmdline`. Action names are listed in the [Key reference](#key-reference). Unknown panes, actions or keys stop the start with an explanation.

Key syntax (prefixes case-insensitive):

- Prefixes `ctrl+`, `alt+`, `shift+`, combinable.
- Function keys `f1` … `f24`.
- Names: `enter`, `tab`, `backtab`, `esc`/`escape`, `backspace`, `delete`, `insert`, `home`, `end`, `pgup`, `pgdn`, `up`, `down`, `left`, `right`, `space`.
- `ctrl+a` … `ctrl+z`, `ctrl+space`.
- A single character: `s`, `S` (upper case = with Shift), `/`, `[`, `alt+,`.
- **Not allowed:** `alt+` with an upper-case letter or with `@ [ \ ] ^ _`. Terminals send Alt+key as `ESC` followed by the key; `ESC` followed by one of these characters is a C1 control sequence (`ESC P` = DCS, …) that never arrives as a key. dbcc rejects such bindings with an explanation; use a lower-case letter or another key.

Notes: bindings of the focused pane take precedence over global ones. An action with several default keys (grid `next_page` = `n` and `F6`, `prev_page` = `p` and `Shift+F6`) gets the new key on all its entries when rebound. The footer shows only F1–F10 without modifiers. `Esc` followed by a digit, and `Alt`+digit, always act as F1–F10.

---

## Key reference

F1 shows these tables from the live keymap (so including your changes), plus every command; the search field filters keys and commands. In the help `F1` or `Tab` opens the About page, `Esc` closes, `Up`/`Down`/`PgUp`/`PgDn`/`Home`/`End` scroll. On the About page `L` opens the third-party licences (scroll with `Up`/`Down`/`PgUp`/`PgDn`/`Home`/`End`; `L` or `Esc` goes back); `Esc` closes the About page.

**Esc+digit and Alt+digit act as F1–F10** (`0` = F10), Esc+digit within one second. Modal windows (form, lookup, dialogs) override the global bindings while they are open.

### Global (`[keys.global]`)

| Key | Action | Effect |
|---|---|---|
| `F1` | `help` | Help overlay for the focused pane |
| `F2` | `profiles` | Connection manager |
| `F8` | `cancel` | Cancel the running statement; on a following result (`tail -f`): stop following |
| `F9` | `cmdline` | Focus the command line |
| `F10` | `quit` | Quit (asks about changed object tabs and open transactions) |
| `Alt+F1` | `focus_tree` | Go to the object tree |
| `Alt+F2` | `focus_editor` | Go to the editor |
| `Alt+F3` | `focus_results` | Go to the results |
| `F11` | `maximize` | Editor or results in full height; again restores the split |
| `Tab` | `next_pane` | Next pane |
| `Shift+Tab` | `prev_pane` | Previous pane |

### Object tree (`[keys.tree]`)

| Key | Action | Effect |
|---|---|---|
| `Enter` | `open` | Default action: expand, open data, or open source |
| `F3` | `cat` | `cat` the selected object |
| `F4` | `edit` | `edit`: source of a view or routine in an editor tab |
| `F5` | `reload` | Reload the selected node |
| `F7` | `new_row` | New row for the selected table |
| `/` | `filter` | Filter the tree by name |
| `c` | `chdir` | Set the current path to the selected node |

Fixed: `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` move; `Right`/`Left` expand/collapse or go to child/parent.

### Editor (`[keys.editor]`)

| Key | Action | Effect |
|---|---|---|
| `F5` | `run_statement` | Run statement at cursor |
| `F6` | `run_all` | Run selection, or the whole buffer |
| `Ctrl+Space` | `complete` | Completion list (also `completion_key_alt`, default `Ctrl+O`) |
| `Alt+,` | `prev_tab` | Previous editor tab |
| `Alt+.` | `next_tab` | Next editor tab |
| `Alt+N` | `new_tab` | New scratch |
| `Alt+O` | `open_scratch` | Scratch list |
| `Alt+E` | `rename_scratch` | Rename the current scratch |
| `Alt+X` | `close_tab` | Close the tab |
| `Alt+R` | `restore` | Restore an object tab |

The editing keys are listed under [The editor](#the-editor); they cannot be rebound. Clipboard: `Ctrl+Shift+C` copy, `Ctrl+Shift+X` cut, `Ctrl+Shift+V` paste.

### Result grid (`[keys.grid]`)

| Key | Action | Effect |
|---|---|---|
| `F3` | `pivot` | Toggle pivot view |
| `F4` | `edit_row` | Edit the record under the cursor |
| `F5` | `rerun` | Re-run the query |
| `F7` | `new_row` | New row |
| `Enter` | `detail` | Cell detail |
| `s` | `sort` | Sort by current column (cycle) |
| `S` | `sort_add` | Add current column as further sort key |
| `/` | `search` | Quick search |
| `f` | `filter_column` | Column filter |
| `w` | `filter_where` | Free WHERE (table-origin only) |
| `x` | `clear_filters` | Remove filters and sorting |
| `n`, `F6` | `next_page` | Next page |
| `p`, `Shift+F6` | `prev_page` | Previous page |
| `c` | `copy_cell` | Copy cell |
| `C` | `copy_row` | Copy row |
| `[` | `prev_result` | Previous result tab |
| `]` | `next_result` | Next result tab |
| `Alt+X` | `close_result` | Close the result tab (Messages stays) |
| `Alt+Q` | `close_all_results` | Close all result tabs |

Fixed: arrows, `PgUp`, `PgDn`, `Home`, `End`.

### Form (`[keys.form]`)

| Key | Action | Effect |
|---|---|---|
| `F2` | `save` | Save |
| `F4` | `lookup` | Lookup or multi-line editor |
| `F6` | `save_new` | Save and new |
| `Ctrl+N` | `set_null` | Set NULL |
| `Ctrl+U` | `untouch` | Reset field to untouched |
| `Esc` | `cancel` | Cancel |

### Command line (`[keys.cmdline]`)

| Key | Action | Effect |
|---|---|---|
| `Ctrl+Space` | `complete` | Completion list (on demand only; also `completion_key_alt`) |
| `Up` | `history_prev` | Previous command (no list open) |
| `Down` | `history_next` | Next command (no list open) |
| `Enter` | `execute` | Run the command or SQL |

Fixed: `Esc` clears the line and goes to the editor.

### Footer per pane (default keys)

| Pane | Footer |
|---|---|
| Tree | `1Help 2Conn 3View 4Edit 5Reload 7New 8Cancel 9Cmd 10Quit` |
| Editor | `1Help 2Conn 5Run 6RunAll 8Cancel 9Cmd 10Quit` |
| Grid | `1Help 2Conn 3Pivot 4Edit 5Rerun 6NextPg 7New 8Cancel 9Cmd 10Quit` |
| Command line | `1Help 2Conn 8Cancel 9Cmd 10Quit` |

---

## Writing your own commands in Lua

Every dbcc command – `cd`, `ll`, `cp` and `cat` included – is a Lua script. Go only provides primitives (run queries, resolve objects, show results, ask the user …); what a command does is written entirely in its script. Your own scripts add commands or replace the built-in ones. They behave like built-in commands: completion, `help`, headless mode, keys and tree actions work without any extra step.

In this chapter:

- [Where the scripts are](#where-the-scripts-are)
- [Loading, replacing, fallback](#loading-replacing-fallback)
- [Installing scripts at run time: `reload`](#installing-scripts-at-run-time-reload)
- [Step by step: your first command](#step-by-step-your-first-command)
- [Declaring a command: `dbcc.command`](#declaring-a-command-dbcccommand)
- [Arguments and flags at invocation](#arguments-and-flags-at-invocation)
- [Commands that write (`write = true`)](#commands-that-write-write--true)
- [The context `ctx`](#the-context-ctx)
- [Primitives](#primitives)
- [Parameters and placeholders](#parameters-and-placeholders)
- [Behaviour without UI](#behaviour-without-ui)
- [Metadata table of `ctx:meta`](#metadata-table-of-ctxmeta)
- [`dbcc.commands()` and `dbcc.fmt`](#dbcccommands-and-dbccfmt)
- [Errors and exit codes](#errors-and-exit-codes)
- [Sandbox](#sandbox)
- [Examples](#examples)
- [Tips: testing, debugging, quoting, safety](#tips-testing-debugging-quoting-safety)

### Where the scripts are

**Built-in commands** are embedded in the binary, so they exist on every machine you copy the single `dbcc` file to. Their source is in the repository under `internal/script/commands/` – one file per command, the file name is the command name:

| File | Command | File | Command |
|---|---|---|---|
| `cat.lua` | [`cat`](#cat) | `ll.lua` | [`ll`](#ll) |
| `cd.lua` | [`cd`](#cd) | `ls.lua` | [`ls`](#ls) |
| `cp.lua` | [`cp`](#cp) (writes) | `mv.lua` | [`mv`](#mv) (writes) |
| `du.lua` | [`du`](#du) | `ps.lua` | [`ps`](#ps) |
| `edit.lua` | [`edit`](#edit) | `pwd.lua` | [`pwd`](#pwd) |
| `export.lua` | [`export`](#export) | `reload.lua` | [`reload`](#reload) |
| `grep.lua` | [`grep`](#grep) | `tail.lua` | [`tail`](#tail) |
| `head.lua` | [`head`](#head) | `wc.lua` | [`wc`](#wc) |
| `help.lua` | [`help`](#help) | `kill.lua` | [`kill`](#kill) (writes) |
| `history.lua` | [`history`](#history) | | |

The built-in scripts use only the public API described below – they are the best examples. Without the repository, get them with

```sh
dbcc --export-commands ~/dbcc-commands     # writes every built-in script there
```

The directory is created if needed. `--export-commands` never overwrites: if even one target file exists, nothing is written (exit code 2) – so choose an empty or new directory.

**Your own commands** are `*.lua` files in the user directory

```text
$XDG_CONFIG_HOME/dbcc/commands/*.lua     # when XDG_CONFIG_HOME is set
~/.config/dbcc/commands/*.lua            # otherwise
```

Create the directory yourself (`mkdir -p ~/.config/dbcc/commands`); a missing directory is not an error. Subdirectories and other file extensions are not read. One file may register several commands; one command per file, named after the command, is the usual and clearer way. `help <command>` shows where a command comes from (`source: embedded:head.lua` or the full path of the file).

### Loading, replacing, fallback

- **When:** At start dbcc loads all built-in scripts first, then the user scripts in alphabetical order of their file names. In the UI, [`reload`](#installing-scripts-at-run-time-reload) loads everything again without a restart. Headless, every `dbcc -c …` loads the scripts afresh – saving the file is enough there.
- **Replacing:** A user command with the name of a built-in one **replaces** it – everywhere, including keys and tree actions, which invoke commands by name (your own `ll.lua` also changes what `Enter` on a table in the tree shows; `F3`/`F4` in the tree run `cat` and `edit`). If two user files register the same name, the alphabetically later one wins.
- **Fallback:** A script that does not load (syntax error, run-time error while loading, invalid declaration, timeout) is reported in the message log (headless: stderr) – `user command script skipped: <path>: <error>` – and skipped. A built-in command of the same name then stays active, so a broken `cd.lua` cannot lock you out. A script's registrations take effect only if the whole script loads without error: a script that calls `dbcc.command{…}` and fails afterwards registers nothing.
- **Time limit:** Loading a script (its top-level code, not the `run` function) may take at most 5 seconds; otherwise: `loading took longer than 5s`.
- **Built-ins only:** `dbcc --no-user-commands` loads no user scripts – the way out when one of your scripts gets in the way. It also applies to `reload` in that session.

A broken replacement of `head`, headless (shortened):

```text
$ dbcc -p doc -c "head logs 1"
warning: user command script skipped: ~/.config/dbcc/commands/head.lua: ~/.config/dbcc/commands/head.lua line:21(column:7) near '=':   syntax error

id | level | msg     | created
---+-------+---------+--------------------
 4 | info  | cleanup | 2026-06-01 09:00:00
```

The warning goes to stderr; the result comes from the built-in `head`.

### Installing scripts at run time: `reload`

New or changed scripts can be installed while dbcc runs – without a restart and without losing connections, transactions, editor tabs or results:

1. Put the `.lua` file into the user directory, or change it there (in a second terminal, for example).
2. In dbcc press `F9` (command line) and type `reload`.
3. The new command is available at once – with completion and `help`.

`reload` builds a new Lua runtime, loads the built-in scripts and then the user scripts exactly as at start, and then replaces the old runtime. Consequences:

- A broken script is reported and skipped; if it replaces a built-in command, the built-in one stays active. All other commands keep working.
- State that scripts hold at top level (local variables, tables) starts afresh.
- Deleted files are gone: a removed user command disappears after `reload`, a removed replacement brings the built-in command back.
- Headless there is no `reload` (it fails with "only available in the UI (headless mode loads the commands at every start)"), and none is needed.

This is what the message log shows (a real session: `tpl` is typed before `tpl.lua` exists – dbcc then takes it for SQL –, then the file is created and `reload` is run):

```text
00:40:01 line 1 tpl  ERROR Error 1064 (42000): You have an error in your SQL syntax; …
00:40:02 reloading commands; user scripts are read from /home/anna/.config/dbcc/commands
00:40:02 commands reloaded: 26 commands (user scripts from /home/anna/.config/dbcc/commands)
00:40:12 tpl: template for settings in the editor
```

With a broken script in the directory:

```text
00:40:18 reloading commands; user scripts are read from /home/anna/.config/dbcc/commands
00:40:19 ERROR user command script skipped: /home/anna/.config/dbcc/commands/oops.lua: … syntax error
00:40:19 commands reloaded: 26 commands (user scripts from /home/anna/.config/dbcc/commands)
```

Scripts can trigger the reload themselves with [`ctx:reload_commands()`](#primitives).

### Step by step: your first command

1. Create the directory:

   ```sh
   mkdir -p ~/.config/dbcc/commands
   ```

2. Write the file `~/.config/dbcc/commands/hello.lua`:

   ```lua
   -- hello.lua: the smallest useful command
   dbcc.command{
     name  = "hello",
     help  = "Greet someone and show the current path",
     args  = { "string?" },
     run = function(ctx, args)
       local who = args[1] or "world"
       ctx:message("hello " .. who .. ", you are at " .. ctx.path.str
         .. " (" .. (ctx.dialect or "not connected") .. ")")
     end,
   }
   ```

   `dbcc.command{…}` registers the command while the script loads. `run` runs on every invocation; `ctx` is the context with all primitives, `args` are the arguments as strings.

3. Test it without UI – the fastest way, because every invocation loads the scripts again:

   ```text
   $ dbcc -p doc -c "hello"
   hello world, you are at /doc/dbcc_doc (mariadb)
   $ dbcc -p doc -c "hello Anna"
   hello Anna, you are at /doc/dbcc_doc (mariadb)
   $ dbcc -p doc -c "help hello"
   usage: hello [string]

   Greet someone and show the current path

   arguments: string?

   source: /home/anna/.config/dbcc/commands/hello.lua
   ```

   dbcc generated the usage line from `args`. Headless, `ctx:message` writes to stderr; in the UI to the message log.

4. In the running UI: `F9`, `reload`, then `hello` – or restart dbcc.

The following sections describe every field and every primitive; [worked examples](#examples) follow after them.

### Declaring a command: `dbcc.command`

`dbcc.command{…}` may only be called while a script loads (at top level, not inside `run`); otherwise: "dbcc.command may only be called while a command script loads". Fields:

| Field | Type | Meaning |
|---|---|---|
| `name` | string, required | `^[a-z][a-z0-9_-]*$` (lower-case letters, digits, `_`, `-`; starts with a letter). `dbcc` is reserved (T-SQL `DBCC` statements pass through as SQL). |
| `usage` | string | Usage line for help and error messages. Generated when absent: `name [-a] [-t string] <object> [int] [string ...]` |
| `help` | string | Description (command list of `help`, `help <command>`, help overlay) |
| `args` | list of strings | Positional arguments with a type: `object`, `path`, `profile`, `file`, `int`, `string`. Suffix `?` = optional, `...` = any number (also none; last only). Required arguments may not follow optional ones. The type drives completion (objects, paths, profiles, files) and the check (`int`). |
| `flags` | table name → type | `bool` (a switch without value) or one of the argument types (a flag with a value; `int` is checked). Names: a letter, then letters, digits, `_`, `-`. |
| `flag_values` | table flag → list of strings | Values that completion offers for a value flag (e.g. `flag_values = { t = { "table", "view" } }` in `cat`). Other values stay allowed. |
| `write` | boolean | `true`: the command writes – see [Commands that write](#commands-that-write-write--true) |
| `run` | function, required | `run(ctx, args, flags)` |

Any invalid declaration fails the whole script. The messages (preceded by file and line):

| Error | Message |
|---|---|
| unknown field | `dbcc.command: unknown field(s) [descr] (name, usage, help, args, flags, flag_values, write, run)` |
| invalid name | `dbcc.command: invalid command name "B2" (lower-case letters, digits, '_' and '-')` |
| name `dbcc` | `dbcc.command: the name "dbcc" is reserved: DBCC statements are plain SQL` |
| wrong field type | `field "name" must be a string, got number`, `field "write" must be a boolean`, `field "args" must be a list` … |
| `run` missing | `dbcc.command b8: field "run" must be a function` |
| unknown argument type | `args[1]: unknown type "number" (object, path, profile, file, int, string)` |
| order | `args[2]: a required argument cannot follow an optional one`, `args[1]: a variadic argument must be the last one` |
| unknown flag type | `flags.all: unknown type "boolean" (bool, object, path, profile, file, int, string)` |
| `flag_values` without a matching flag | `flag_values.x: no such flag with a value` |

### Arguments and flags at invocation

`run(ctx, args, flags)` receives:

- `args`: the positional arguments as a list of **strings** (`int` arguments too – convert them with `tonumber`). Missing optional arguments are `nil`.
- `flags`: the flags given; switches are `true`, value flags hold the value as a string. Flags not given are absent (`nil`).

The line is split into words like in a shell: blanks separate, `'…'` and `"…"` group (inside `"…"`, `\` escapes `"` or `\`), `\` outside escapes the next character. A trailing `;` is ignored. Then:

- Flags come **before** the positional arguments. `-a` and `--a` are the same. Values: `-t value`, `-t=value`, `--t=value`. Switches cannot be bundled (`-ab` is the flag `ab`).
- The first word that does not look like a flag ends the flags, and so does `--` (everything after it is positional). `-` alone and negative numbers (`-5`) are positional.
- A command **without** declared flags takes every word as a positional argument, `-x` included.
- Checked **before** `run`: unknown flags, a value on a switch, a missing value, `int` values, the number of arguments, `int` arguments. These are invocation errors (headless exit code 2):

```text
$ dbcc -p doc -c "counts -x"
ERROR: counts: unknown flag -x; usage: counts [-m min] [path]
$ dbcc -p doc -c "latest logs x"
ERROR: latest: argument 2 must be an integer, got "x"; usage: latest <table> [n]
$ dbcc -p doc -c "setting"
ERROR: setting: usage: setting <name>
```

Other messages: `flag -n takes no value`, `flag -m needs a int value`, `flag -m must be an integer, got "x"`.

### Commands that write (`write = true`)

`write = true` marks a command that changes data or structure (built-in: `cp`, `mv`, `kill`). Effects:

- **Read-only profiles:** If the current path is in a profile with `read_only = true`, the command does not start at all: `archive-logs: writes and is blocked in the read-only profile docro`. `ctx:exec` on another read-only connection (`ctx:on(…)`) is rejected as well.
- **Every `ctx:exec` is confirmed:** Before each statement a yes/no dialog "`<command>` will run on `<profile>`:" shows the statement. "No" aborts the command with "cancelled by user"; whatever ran before stays (with autocommit). The dialog shows the statement with its placeholders (`?`), not the parameter values.
- **Asking once:** If the user says yes to a `ctx:confirm(…)` in this invocation, the following `ctx:exec` calls do not ask again. That way all statements (and the values) are shown in one question – as `cp` does, see [example d](#d-a-write-command-with-parameters-and-confirmation).
- **`confirm_writes`** of the profile does not ask a second time for a statement confirmed this way.
- **Headless:** Without `--yes` the confirmation fails with `confirmation required; run with --yes:` followed by the question (exit code 1); with `--yes` every confirmation counts as yes, with `-v` also logged (`-- confirmed (--yes): …`).
- `ctx:query` (reading) is never confirmed.

Commands without `write = true` can write with `ctx:exec` too; then only the safeguards that apply to typed SQL are in effect (the profile's read-only guard: "read-only profile: statement rejected", `confirm_writes`). For anything that changes data, `write = true` is right.

### The context `ctx`

`ctx` is valid only while its command runs (a `ctx` kept in a variable fails later with "ctx is only valid while its command runs"). Always call methods with a colon: `ctx:query(…)`; `ctx.query(…)` fails with "call it with a colon".

Fields (read only):

| Field | Content |
|---|---|
| `ctx.command` | name of the running command |
| `ctx.path` | current path: `{conn=, database=, schema=, str="/c/d/s", level=}`; `level` is `"root"`, `"connection"`, `"database"` or `"schema"`; missing parts are `nil`; `tostring(ctx.path)` = `str` |
| `ctx.dialect` | `"mysql"`, `"mariadb"`, `"postgres"`, `"mssql"`, or `nil` (not connected, path `/`) |
| `ctx.has_schemas` | `true` on PostgreSQL and SQL Server (paths with a schema level) |
| `ctx.profile` | `{name=, driver=, database=, read_only=, autocommit=, confirm_writes=}` or `nil`; `driver` is the configured value (`mysql` for MariaDB too), `database` only when set in the profile |
| `ctx.conn` | only on a context bound with `ctx:on`: the connection name |

Example (real output on MariaDB, profile `doc` with `database = "dbcc_doc"`): `ctx.path.str` = `/doc/dbcc_doc`, `ctx.path.level` = `database`, `ctx.dialect` = `mariadb`, `ctx.profile.driver` = `mysql`, `ctx.has_schemas` = `false`.

**`ctx:on(target)`** returns a context bound to another connection: `target` is a profile name, an object from `ctx:resolve` or a path table; the connection is opened if needed. On a bound context, `dialect`, `has_schemas`, `profile`, all SQL helpers (`quote`, `limit`) and execution (`query`, `exec`, `grid`, `resolve` with plain names) refer to that connection; `ctx.path` stays the current path. If `target` is the current connection, the context follows it like `ctx`. Rule of thumb: **always address objects through `ctx:on(obj)`**, because an object from the tree or an absolute path may live on another connection.

### Primitives

All primitives block until their database work is done and raise a Lua error on failure (see [Errors](#errors-and-exit-codes)); `pcall` catches it.

**Navigation and metadata:**

| Method | Returns / effect |
|---|---|
| `ctx:chdir(path)` | changes the path like `cd` (`USE` or reconnect, asking first if a transaction is open); updates `ctx.path`, `ctx.dialect`, `ctx.profile`; returns the new path (a path table) |
| `ctx:abspath(path)` | resolves a path without side effects (`"."` or `nil` = current, `".."`, relative and absolute paths); returns a path table. `path` may also be a path table |
| `ctx:objects(path, kinds)` | at `/conn`: the databases; at a database of a schema dialect: the schemas; otherwise the objects of the database or schema, filtered by `kinds` (a string or a list: `database`, `schema`, `table`, `view`, `materialized view`, `procedure`, `function`, `trigger`, `event`, `sequence`; `nil` = all). Elements `{name=, kind=, comment=, system=}`. At `/` an error: use `ctx:profiles()` there |
| `ctx:profiles()` | all profiles of the configuration, sorted by name: `{name=, driver=, host=, port=, socket=, user=, database=, read_only=, autocommit=, confirm_writes=, open=, current=}`; unset texts are `""`, `port` is a number, `open` = connection open, `current` = current profile |
| `ctx:resolve(name, kinds)` | resolves an object name, see below |
| `ctx:on(target)` | context bound to the connection of `target` (see above) |
| `ctx:meta(obj)` | metadata of a table, view or materialized view (`obj` = name or object), see [metadata table](#metadata-table-of-ctxmeta) |
| `ctx:source(obj, kind)` | source text of a view, procedure, function, trigger or event (`kind` optional, restricts the lookup) |

`ctx:resolve(name, kinds)` accepts:

- a plain name (`logs`) – looked up in the current database or schema;
- qualified names: `db.obj` (MySQL/MariaDB), `schema.obj` and `db.schema.obj` (PostgreSQL, SQL Server); parts may be quoted (`` `my table` ``, `"x.y"`, `[a b]`; doubled quotes escape themselves);
- paths whose last segment is the object: `/conn/db/obj`, `/conn/db/schema/obj`, `../otherdb/obj`. A path may point to another profile; its connection is opened.

The lookup is exact, otherwise case-insensitive when that is unambiguous (`LOGS` finds `logs`). `kinds` (as for `ctx:objects`) restricts the lookup; an object of another kind then counts as not found. Returns:

```text
{ name="logs", kind="table", comment="", conn="doc", sql="`dbcc_doc`.`logs`",
  parts={ conn="doc", database="dbcc_doc", schema=nil, object="logs" } }
```

`sql` is the fully qualified, quoted name – ready to use in SQL; `tostring(obj)` = `obj.sql`. Errors: `no such object: x in /doc/dbcc_doc`, `"a.b.c": too many name parts (mariadb has no schema level)`, `"x": no database selected; cd into a database or qualify the name`, `not connected: cd /<profile> first`.

Real resolutions on MariaDB (current path `/doc/dbcc_doc`):

| Input | `obj.sql` |
|---|---|
| `logs`, `` `logs` ``, `LOGS`, `dbcc_doc.logs` | `` `dbcc_doc`.`logs` `` |
| `/doc/shop/orders`, `../shop/orders` | `` `shop`.`orders` `` |

**SQL helpers:**

| Method | Returns |
|---|---|
| `ctx:quote(ident)` | the identifier quoted for the dialect: `` `we``ird` `` (MySQL/MariaDB), `"we""ird"` (PostgreSQL), `[we]]ird]` (SQL Server) |
| `ctx:limit(sql, n)` | the query with a row limit `n` (≥ 0). MySQL/MariaDB/PostgreSQL: if the query has a top-level `ORDER BY`, `LIMIT n` is appended (`SELECT * FROM t ORDER BY a LIMIT 5`), otherwise it is wrapped (`SELECT * FROM (SELECT * FROM t) AS dbcc_limit LIMIT 5`). SQL Server: `TOP (n)` is inserted into the main `SELECT` (`SELECT TOP (5) * FROM t ORDER BY a`); with `UNION`, `OFFSET` or an existing `TOP` the query is wrapped |
| `ctx:copy_sql(src, dst, opts)` | `stmts, target`: the list of statements that copy `src` (name or object) into the new table `dst` (as `cp` does); `opts.structure = true` selects the `cp -a` variant with keys and indexes. `target` is an object-like table of the new table (`name`, `kind`, `conn`, `sql`, `parts`). Runs nothing |
| `ctx:replace_script(obj, kind)` | runnable script that re-creates the object from its current source (as `edit` does), see [object tabs](#object-tabs-and-replace-scripts) |

**Execution:**

| Method | Returns / effect |
|---|---|
| `ctx:query(sql, ...)` | runs a query (parameters after `sql`) and buffers the result up to `max_rows_buffered` (default 10,000). Returns an array of rows; each row is an array of **strings** (numbers and dates as text, the way the grid shows them) or **`nil` for NULL**. In addition `row.colname` (with duplicate names the first column; unknown names give `nil`), `res.columns` (list of column names) and `res.capped` (`true` when cut off at `max_rows_buffered`) |
| `ctx:exec(sql, ...)` | runs a statement; returns the number of affected rows. In `write` commands the confirmation comes first (see above) |
| `ctx:reload_commands()` | reloads all commands once the running command has finished (like `reload`); returns the user command directory. UI only |

Result of `ctx:query("SELECT id, level, msg FROM logs ORDER BY id")` (real values):

```lua
#res          --> 3
res.columns   --> { "id", "level", "msg" }
res.capped    --> false
res[1][1]     --> "4"          (a string, not the number 4)
res[1].level  --> "info"
res[3].msg    --> nil          (NULL)
```

`ctx:query` and `ctx:exec` run in the session like typed SQL: in the profile's transaction mode, with the profile's read-only guard and `confirm_writes`. They are not recorded in the statement history.

**Output:**

| Method | Effect |
|---|---|
| `ctx:grid(sql, opts, ...)` | opens a lazily loaded result in a result tab (parameters come **after** `opts`; without options `ctx:grid(sql, nil, p1)`). `opts.origin` (an object from `ctx:resolve`) makes it **table-origin** and runs it on the object's connection: sorting and filtering on the server, free `WHERE`, row form (`F4`), new row (`F7`). `opts.title` names the tab. `opts.follow = {sql=, key=, interval=}` makes it a following result (below). Returns at once; further rows load while paging |
| `ctx:rows(headers, rows, opts)` | shows rows computed in Lua as a result tab: `headers` is a list of names, `rows` a list of row lists; values are converted with `tostring`, `nil` = NULL. Columns whose values are all Lua numbers are numeric (right-aligned). More values than headers give columns `col2`, `col3` …. `opts.title` (default: the command name). Not table-origin |
| `ctx:text(str, lexer, opts)` | text in a tab: `lexer = "sql"` = the dialect's SQL lexer, otherwise a Chroma lexer name (`"lua"`, `"json"` …), `nil` = plain text; `opts.title` (default: the command name) |
| `ctx:message(msg)` | an entry in the message log; `msg` is converted with `tostring` |
| `print(...)` | like `ctx:message`, arguments separated by tabs; also usable while a script loads |
| `ctx:export(file, opts)` | writes the active result (the current result tab) to `.csv` or `.json` (format from the extension) like `export`: the open cursor is read to the end, a local filter applies, a local sort loads all rows first. `~/` is expanded. `opts.force = true` overwrites an existing file, `opts.progress` = row interval of progress messages (default 10,000, 0 = none). Returns the number of rows written. On failure no half-written file is left |

`opts.follow` of `ctx:grid`: `follow.sql` is a query with exactly one `?` for the last seen value of the result column `follow.key`, returning the newer rows ordered by it ascending. It runs every `interval` seconds (default 2, at least 0.5) in the background on the auxiliary connection; its rows are appended, and the result keeps at most `page_size` rows. Following ends when the tab is closed or the result is cancelled. Template: `tail.lua`.

Note on table-origin: server-side sorting and filtering rebuild the query from the object (`SELECT * FROM <obj> WHERE … ORDER BY …`). The `ORDER BY` and `LIMIT` of the command's query no longer apply after that – `head` and `tail` behave the same way.

**Interaction:**

| Method | Effect |
|---|---|
| `ctx:confirm(msg)` | yes/no dialog; returns `true` or `false`. In a `write` command a "yes" covers all following `ctx:exec` calls of this invocation |
| `ctx:pick(items, opts)` | filterable list (typing filters, `Enter` chooses, `Esc` cancels). `items` are strings or tables with a `label` field (other fields are allowed and come back unchanged). Returns `item, index` (the chosen element itself and its position from 1), or a single `nil` when cancelled; `opts.title` |
| `ctx:editor_set(text)` | replaces the text of the current scratch tab; if the current tab is not a scratch, that of the first scratch, otherwise of a new one |
| `ctx:editor_open(title, text, opts)` | with `opts.object` (from `ctx:resolve`): opens or focuses an object tab bound to the object, titled `title` (as `edit` does); without an object: a new scratch with the text (`title` is not used then) |
| `ctx:about()` | shows the About page |
| `ctx:history(n)` | the last `n` entries of the statement history (default 100), oldest first: `{time="2026-11-22 00:38:48", profile=, path=, duration_ms=, ok=, outcome=, stmt=}` |

### Parameters and placeholders

Write parameters as `?` in every dialect and pass the values after the SQL text (for `ctx:grid` after `opts`):

```lua
ctx:query("SELECT value FROM settings WHERE name = ?", args[1])
ctx:exec("DELETE FROM logs WHERE created < ? AND level = ?", before, "info")
ctx:grid("SELECT * FROM logs WHERE level = ?", { title = "warnings" }, "warn")
```

- When arguments are passed, dbcc replaces every `?` outside strings, comments and quoted identifiers with the dialect's placeholder: `?` stays on MySQL/MariaDB, `$1, $2 …` on PostgreSQL, `@p1, @p2 …` on SQL Server.
- **Without arguments the statement is left unchanged** – on PostgreSQL `?` is also a jsonb operator (so `'{"a":1}'::jsonb ? 'a'` works without parameters).
- Native placeholders work as well (`$1` on PostgreSQL, `@p1` on SQL Server); mixing `?` and native placeholders in one statement does not.
- Allowed values: `nil` (NULL), booleans, numbers (whole numbers are passed as integers), strings. Tables are an error.
- PostgreSQL infers a parameter's type from its context. Where it cannot (`SELECT ? AS a`), the type is `text`, and a Lua number fails with "unable to encode 5 into text format": write `?::int` or pass a string. `WHERE id = ?` with a number works.
- Identifiers (table and column names) cannot be parameters: use `obj.sql` from `ctx:resolve` or `ctx:quote` for them.

### Behaviour without UI

Every primitive works headless too (`dbcc -p … -c "<command>"`):

| Primitive | UI | headless |
|---|---|---|
| `ctx:grid` | result tab, page by page | all rows on stdout in the `--format` format (no `max_rows`); with `follow` on until SIGINT/SIGTERM |
| `ctx:rows` | result tab | stdout in the `--format` format |
| `ctx:text` | text tab with highlighting | the text unchanged on stdout (no colour, no title) |
| `ctx:message`, `print` | message log | stderr |
| `ctx:confirm`, confirmation of `ctx:exec` | yes/no dialog | without `--yes`: error `confirmation required; run with --yes:` + the question; with `--yes`: `true` |
| `ctx:pick` | list | error `ctx:pick: choosing from a list is not available without UI` |
| `ctx:editor_set` | scratch tab | error `ctx:editor_set: the editor is not available without UI` |
| `ctx:editor_open` | editor tab | the text on stdout |
| `ctx:about` | About page | the About text on stdout |
| `ctx:export` | current result tab | the result printed last – which is already streamed to stdout then; headless, use `--format csv`/`json` instead |
| `ctx:reload_commands` | reloads | error (headless loads afresh on every invocation) |

The errors of `ctx:pick` and `ctx:editor_set` can be caught with `pcall` to offer an alternative without UI – as `history.lua` does, and as [example g](#g-interaction-list-and-editor) does.

### Metadata table of `ctx:meta`

```text
{ name=, kind=, sql=, conn=, parts={…}, comment=, view_definition=, options={key=value},
  columns = { {name=, position=, data_type=, nullable=, default=(nil: none), default_expr=,
               auto_increment=, generated={expression=, stored=}|nil, on_update=,
               enum_values={…}, collation=, comment=}, … },
  primary_key = {name=, columns={…}} | nil,
  unique_keys = { {name=, columns={…}}, … },
  foreign_keys = { {name=, columns=, ref_table={conn=, database=, schema=, object=, sql=},
                    ref_columns=, on_update=, on_delete=}, … },
  checks = { {name=, expression=, not_enforced=}, … },
  indexes = { {name=, columns={ {name=, descending=, sub_part=, expression=}, … },
               unique=, type=, invisible=, comment=, include={…}, filter=}, … },
  triggers = { {name=, timing=, event=}, … } }
```

The first fields are those of `ctx:resolve`. Real values for `logs` on MariaDB: `m.primary_key.name` = `PRIMARY`, `m.primary_key.columns` = `{ "id" }`, `#m.columns` = 4, `m.columns[1].data_type` = `int(11)`, `m.columns[1].auto_increment` = `true`, `m.columns[3].nullable` = `true`. `cat.lua` shows how to print every field.

### `dbcc.commands()` and `dbcc.fmt`

| Function | Returns |
|---|---|
| `dbcc.commands()` | list of all registered commands, sorted by name: `{name=, usage=, help=, args={"object", "int?"}, flags={name=type}, write=, source=, default=}`; `default` is `true` for built-in commands (`help.lua` builds the help from it) |
| `dbcc.fmt.table(headers, rows)` | an aligned text table as a string; `nil` = `NULL`; purely numeric columns right-aligned |
| `dbcc.fmt.size(bytes)` | `"512 B"`, `"1.5 KiB"`, `"16.0 MiB"` … (`nil` → `""`) |
| `dbcc.fmt.count(n)` | `"1,234,567"` |

### Errors and exit codes

- **Your own errors:** `error("text", 0)`. The `0` drops the position; `error("text")` gives `<command>: /path/to/file.lua:14: text`. Pass strings only – a table shows as `table: 0x…`.
- **Errors of primitives** (SQL errors, objects not found …) become Lua errors with the error's text. Uncaught, they end the command.
- Every error of a command is prefixed with the command name: `setting: no setting named nope`, `probe: Error 1064 (42000): You have an error in your SQL syntax …`. In the UI it appears in the message log and the status line, headless as `ERROR: …` on stderr.
- **Headless exit codes:** `2` for invocation errors (wrong number of arguments, unknown flag, not an integer), `1` for every error while running (`error(…)`, SQL errors, a declined confirmation), `0` on success.
- `pcall(f, …)` catches errors, those of primitives included: `local ok, err = pcall(ctx.query, ctx, sql)`.

### Sandbox

- Lua 5.1 (gopher-lua). There is **no** integer division `//`, no bitwise operators and no `goto` – use `math.floor(a / b)`.
- Only the `base`, `table`, `string` and `math` libraries are loaded. **No `os`, no `io`, no `debug`, no `package`, no `coroutine`**; `dofile`, `loadfile`, `require`, `module` and `newproxy` are removed. `load`/`loadstring` remain (code from strings only). So there is no file access (except through `ctx:export`), no clock and no environment variables – ask the database for date and time (`SELECT NOW()`).
- Every script has **its own global environment**: one script's globals are not visible in others; reads fall back to the shared standard functions. There is no shared code (no `require`); helper functions belong in the file that uses them.
- The tables `dbcc` and `dbcc.fmt` are read-only (`dbcc is read-only`).
- Top-level state of a script lives as long as the Lua runtime: in the UI until `reload` or quitting, headless only for the one command.
- Loading is limited to 5 seconds, the `run` function is not. `F8` cancels only a running SQL statement of the command, **not pure Lua code**: an endless loop in `run` blocks all further commands until dbcc is quit (`F10`). Headless, `Ctrl+C` does not stop such a loop either; only `kill` (SIGTERM) does. So write loops with an exit condition.

### Examples

All examples are complete: save the file in `~/.config/dbcc/commands/`, run `reload` in the UI – done. They were tested with dbcc against MariaDB 11.8 (examples b and c also against PostgreSQL 17 and SQL Server 2022, e against all four dialects); the output shown is real. The test database `dbcc_doc` holds `logs (id, level, msg, created)`, `logs_archive` with the same structure, `settings (name, value)` and the view `recent_errors`; the profile `doc` points to it.

#### a) Reading a value: `setting`

```lua
-- setting.lua: read one value from an application settings table
dbcc.command{
  name  = "setting",
  usage = "setting <name>",
  help  = "Show the value of a row in the settings table of the current database",
  args  = { "string" },
  run = function(ctx, args)
    local res = ctx:query("SELECT value FROM settings WHERE name = ?", args[1])
    if #res == 0 then
      error("no setting named " .. args[1], 0)
    end
    local v = res[1].value            -- or res[1][1]; NULL arrives as nil
    ctx:message(args[1] .. " = " .. (v == nil and "NULL" or v))
  end,
}
```

The name goes into the query as a parameter – no SQL is assembled from user input. `#res` is the number of rows; NULL arrives as `nil`.

```text
$ dbcc -p doc -c "setting retention_days"
retention_days = 90
$ dbcc -p doc -c "setting owner"
owner = NULL
$ dbcc -p doc -c "setting nope"; echo $?
ERROR: setting: no setting named nope
1
```

#### b) A table-origin grid: `latest`

The newest rows of a table, by primary key descending – the counterpart of `head`.

```lua
-- latest.lua: the newest rows of a table, by primary key
dbcc.command{
  name  = "latest",
  usage = "latest <table> [n]",
  help  = "Show the n newest rows (default 10), ordered by the primary key descending",
  args  = { "object", "int?" },
  run = function(ctx, args)
    local n = tonumber(args[2]) or 10
    if n < 1 then error("n must be at least 1", 0) end
    local obj = ctx:resolve(args[1], "table")   -- tables only
    local on  = ctx:on(obj)                     -- the table's connection
    local m   = on:meta(obj)
    if not m.primary_key then
      error(obj.name .. " has no primary key", 0)
    end
    local order = {}
    for _, col in ipairs(m.primary_key.columns) do
      order[#order + 1] = on:quote(col) .. " DESC"
    end
    local sql = "SELECT * FROM " .. obj.sql .. " ORDER BY " .. table.concat(order, ", ")
    on:grid(on:limit(sql, n), { origin = obj, title = "latest " .. obj.name })
  end,
}
```

- `args = { "object", "int?" }`: completion offers tables, dbcc checks that `n` is an integer.
- `ctx:resolve(args[1], "table")` accepts `orders`, `shop.orders` or `/doc/shop/orders`; `obj.sql` is the quoted, qualified name.
- `ctx:on(obj)`: the object may live on another connection – the SQL helpers and `grid` must run there.
- `ctx:meta` provides the primary key (composite ones too), `on:quote` quotes the column names, `on:limit` adds the limit for the dialect.
- `origin = obj` makes the result table-origin: in the UI `F4` (row form), `w` (free `WHERE`), and sorting and filtering on the server work – just as with `head`. A filter rebuilds the query from the table; the order and limit of `latest` no longer apply after that.

```text
$ dbcc -p doc -c "latest shop.orders 3"
id  | customer_id | total  | status | created
----+-------------+--------+--------+--------------------
512 |          77 |  42.50 | new    | 2026-11-21 19:52:48
400 |         400 | 600.00 | paid   | 2026-11-21 19:45:26
399 |         399 | 598.50 | new    | 2026-11-21 19:45:26
$ dbcc -p doc -c "latest /pgdoc/postgres/dbcc_doc/logs 2"
id | level | created
---+-------+--------------------
 3 | error | 2026-03-01 00:00:00
 2 | warn  | 2026-02-01 00:00:00
$ dbcc -p doc -c "latest recent_errors"
ERROR: latest: no such object: recent_errors in /doc/dbcc_doc
```

The second call reads – from the MariaDB profile – a PostgreSQL table by its path (`LIMIT` appended, `"…"` quoting); against SQL Server (`/msdoc/dbcc_doc/dbo/logs`) it becomes `SELECT TOP (2) …`. The third shows the restriction to tables: `recent_errors` is a view. In the UI a tab "latest orders" appears; in the test, `w` with `status = 'paid'` filtered on the server (status line `134 rows | filter: (status = 'paid')`) and `F4` opened the row form.

#### c) Computed rows: `counts`

The row count of every table of a database or schema, optionally only from a minimum count.

```lua
-- counts.lua: row counts of all tables of a database or schema
dbcc.command{
  name  = "counts",
  usage = "counts [-m min] [path]",
  help  = "Row count of every table of a database or schema; -m shows only tables with at least min rows",
  args  = { "path?" },
  flags = { m = "int" },
  run = function(ctx, args, flags)
    local p = ctx:abspath(args[1] or ".")
    if not p.database then
      error("counts needs a database: cd into one or give a path", 0)
    end
    local on = ctx:on(p)                     -- the connection of the path
    if on.has_schemas and not p.schema then
      error("counts needs a schema on " .. on.dialect .. ": " .. p.str .. "/<schema>", 0)
    end
    local min = tonumber(flags.m) or 0
    local rows, total = {}, 0
    for _, o in ipairs(ctx:objects(p, "table")) do
      local obj = on:resolve(p.str .. "/" .. o.name, "table")
      local n = tonumber(on:query("SELECT COUNT(*) AS n FROM " .. obj.sql)[1].n)
      if n >= min then
        rows[#rows + 1] = { o.name, n }
        total = total + n
      end
    end
    table.sort(rows, function(a, b) return a[2] > b[2] end)
    ctx:rows({ "table", "rows" }, rows, { title = "counts " .. p.str })
    ctx:message("counts: " .. #rows .. " tables, " .. dbcc.fmt.count(total) .. " rows")
  end,
}
```

- `ctx:abspath` turns `.`, `..`, relative and absolute paths into a path table; `p.database`/`p.schema` tell whether the level is right.
- `ctx:objects(p, "table")` returns tables only; `on:resolve(path)` turns each into an object with a quoted `sql`.
- `tonumber` turns the counts into Lua numbers – that makes the column numeric in `ctx:rows` (right-aligned, sorted numerically).
- The flag `-m` is declared `int`; `-m 3`, `-m=3`, `--m=3` are the same.

```text
$ dbcc -p doc -c "counts"
table        | rows
-------------+-----
logs         |    6
settings     |    2
logs_archive |    0
counts: 3 tables, 8 rows
$ dbcc -p doc -c "counts --m=400 /doc/shop"
table     | rows
----------+-------
big       | 400000
customers |   3000
orders    |    401
counts: 3 tables, 403,401 rows
$ dbcc -p doc --format csv -c "counts /doc/shop" 2>/dev/null
table,rows
big,400000
customers,3000
orders,401
events,33
$ dbcc -p doc -c "counts /pgdoc/postgres"
ERROR: counts: counts needs a schema on postgres: /pgdoc/postgres/<schema>
```

The line `counts: …` is a message (stderr); `2>/dev/null` leaves only the data.

#### d) A write command with parameters and confirmation

Move old rows of `logs` into `logs_archive`; `-n` only counts.

```lua
-- archive-logs.lua: move old rows of logs into logs_archive
dbcc.command{
  name  = "archive-logs",
  usage = "archive-logs [-n] <before-date>",
  help  = "Move rows of logs created before a date (YYYY-MM-DD) into logs_archive; -n only counts",
  args  = { "string" },
  flags = { n = "bool" },
  write = true,
  run = function(ctx, args, flags)
    local before = args[1]
    if not before:match("^%d%d%d%d%-%d%d%-%d%d$") then
      error("the date must look like 2026-06-01, got " .. before, 0)
    end
    local n = tonumber(ctx:query("SELECT COUNT(*) AS n FROM logs WHERE created < ?", before)[1].n)
    if flags.n or n == 0 then
      ctx:message("archive-logs: " .. n .. " rows before " .. before .. (flags.n and " (dry run)" or ""))
      return
    end
    local copy = "INSERT INTO logs_archive SELECT * FROM logs WHERE created < ?"
    local del  = "DELETE FROM logs WHERE created < ?"
    -- One question for both statements. Without it, every ctx:exec of a
    -- write command asks on its own (and "No" to the DELETE would leave
    -- the copied rows in both tables).
    if not ctx:confirm(string.format("Move %d rows created before %s from logs to logs_archive?\n\n%s;\n%s;",
                                     n, before, copy, del)) then
      ctx:message("archive-logs: nothing changed")
      return
    end
    local copied  = ctx:exec(copy, before)   -- covered by the "yes" above
    local deleted = ctx:exec(del, before)
    ctx:message(string.format("archive-logs: copied %d, deleted %d rows", copied, deleted))
  end,
}
```

- `write = true`: blocked in read-only profiles, every `ctx:exec` is confirmed – unless a `ctx:confirm` was answered with yes. The one question shows the count, the date and both statements.
- The date is checked and passed as a parameter, never inserted into the SQL text.
- `ctx:exec` returns the affected rows.

```text
$ dbcc -p doc -c "archive-logs -n 2026-06-01"
archive-logs: 3 rows before 2026-06-01 (dry run)
$ dbcc -p doc -c "archive-logs 2026-06-01"; echo $?
ERROR: archive-logs: confirmation required; run with --yes:
Move 3 rows created before 2026-06-01 from logs to logs_archive?

INSERT INTO logs_archive SELECT * FROM logs WHERE created < ?;
DELETE FROM logs WHERE created < ?;
1
$ dbcc -p doc --yes -c "archive-logs 2026-06-01"
archive-logs: copied 3, deleted 3 rows
$ dbcc -p docro -c "archive-logs 2026-06-01"
ERROR: archive-logs: writes and is blocked in the read-only profile docro
```

In the UI a dialog with this text and "Yes"/"No" appears. **Without** the `ctx:confirm`, dbcc asks before each of the two statements separately ("archive-logs will run on doc:" plus the statement, placeholder `?` instead of the value). In the test the `INSERT` was confirmed and the `DELETE` declined: the command ended with "archive-logs: cancelled by user", and the copied rows were then in both tables – hence the single question.

#### e) Dialect-specific: `server`

Server version and uptime. `ctx.dialect` selects the query.

```lua
-- server.lua: version and uptime of the server, per dialect
local queries = {
  mysql = [[SELECT VERSION() AS version,
                   (SELECT VARIABLE_VALUE FROM performance_schema.global_status
                     WHERE VARIABLE_NAME = 'Uptime') AS uptime_s]],
  postgres = [[SELECT version() AS version,
                      CAST(EXTRACT(EPOCH FROM now() - pg_postmaster_start_time()) AS bigint) AS uptime_s]],
  mssql = [[SELECT @@VERSION AS version,
                   DATEDIFF(second, sqlserver_start_time, SYSDATETIME()) AS uptime_s
              FROM sys.dm_os_sys_info]],
}
queries.mariadb = [[SELECT VERSION() AS version,
                           (SELECT VARIABLE_VALUE FROM information_schema.GLOBAL_STATUS
                             WHERE VARIABLE_NAME = 'UPTIME') AS uptime_s]]

local function duration(s)
  local d = math.floor(s / 86400)
  local h = math.floor(s % 86400 / 3600)
  local m = math.floor(s % 3600 / 60)
  return string.format("%dd %02dh %02dm", d, h, m)
end

dbcc.command{
  name  = "server",
  help  = "Show the server version and uptime",
  run = function(ctx)
    local q = ctx.dialect and queries[ctx.dialect]
    if not q then error("not connected: cd /<profile> first", 0) end
    local r = ctx:query(q)[1]
    local version = r.version:match("^[^\n]*")      -- first line (SQL Server: 4 lines)
    ctx:rows({ "profile", "dialect", "version", "uptime" },
             { { ctx.profile.name, ctx.dialect, version, duration(tonumber(r.uptime_s)) } })
  end,
}
```

- Top-level code (the `queries` table, the `duration` function) runs once while loading; `local` keeps it private.
- MariaDB reports itself as `mariadb` although the profile says `driver = "mysql"` – hence its own entry. MySQL 8 no longer has `information_schema.GLOBAL_STATUS`; MariaDB still uses it.
- No integer division in Lua 5.1: `math.floor(a / b)`.
- On SQL Server, `sys.dm_os_sys_info` needs the `VIEW SERVER STATE` permission.

Tested against all four dialects:

```text
$ dbcc -p doc -c server
profile | dialect | version                | uptime
--------+---------+------------------------+-----------
doc     | mariadb | 11.8.9-MariaDB-ubu2404 | 0d 03h 04m
$ dbcc -p mydoc -c server
mydoc   | mysql   | 8.4.10  | 0d 03h 04m
$ dbcc -p pgdoc -c server
pgdoc   | postgres | PostgreSQL 17.11 (Debian 17.11-1.pgdg13+2) on x86_64-pc-linux-gnu, compiled by gcc … | 0d 03h 04m
$ dbcc -p msdoc -c server
msdoc   | mssql   | Microsoft SQL Server 2022 (RTM-CU24-GDR) (KB5083252) - 16.0.4250.1 (X64)  | 0d 03h 04m
```

(For the last three only the data row is shown.)

#### f) Replacing a built-in command: `head` with 20 rows

```sh
dbcc --export-commands /tmp/dbcc-defaults
cp /tmp/dbcc-defaults/head.lua ~/.config/dbcc/commands/head.lua
```

Then change two places in the copy (help and default):

```lua
-- head.lua (user override): like the built-in head, but 20 rows by default
local data_kinds = { table = true, view = true, ["materialized view"] = true }

dbcc.command{
  name  = "head",
  usage = "head <table|view> [n]",
  help  = "Show the first n rows (default 20)",
  args  = { "object", "int?" },
  run = function(ctx, args)
    local n = tonumber(args[2]) or 20
    if n < 0 then error("n must not be negative", 0) end
    local obj = ctx:resolve(args[1])
    if not data_kinds[obj.kind] then
      error(obj.name .. " is a " .. obj.kind .. ", not a table or view", 0)
    end
    -- The object may live on another connection (object tree, absolute path).
    local on = ctx:on(obj)
    on:grid(on:limit("SELECT * FROM " .. obj.sql, n), { origin = obj })
  end,
}
```

After `reload` (or a restart) the new `head` applies wherever `head` is invoked – on the command line and headless:

```text
$ dbcc -p doc -c "help head" | tail -1
source: /home/anna/.config/dbcc/commands/head.lua
$ dbcc -p doc -c "head /doc/shop/orders" | wc -l
22                                   # header + separator + 20 rows
$ dbcc --no-user-commands -p doc -c "head /doc/shop/orders" | wc -l
12                                   # the built-in head: 10 rows
```

If the replacement is broken, the built-in `head` stays active (see [Loading, replacing, fallback](#loading-replacing-fallback)). Deleting the file and running `reload` restores the original.

#### g) Interaction: list and editor

`tpl` writes a `SELECT` with all columns of a table into the scratch tab; without an argument you choose the table from a list.

```lua
-- tpl.lua: choose a table and put a SELECT template into the scratch tab
dbcc.command{
  name  = "tpl",
  usage = "tpl [table]",
  help  = "Write a SELECT with all columns of a table into the current scratch tab; without argument choose the table from a list",
  args  = { "object?" },
  run = function(ctx, args)
    local name = args[1]
    if not name then
      local items = {}
      for _, o in ipairs(ctx:objects(".", { "table", "view" })) do
        items[#items + 1] = { label = o.name .. "  (" .. o.kind .. ")", name = o.name }
      end
      if #items == 0 then error("no tables or views here", 0) end
      local item = ctx:pick(items, { title = "tpl: choose a table" })
      if not item then return end              -- Esc: cancelled
      name = item.name
    end
    local obj = ctx:resolve(name, { "table", "view" })
    local m = ctx:on(obj):meta(obj)
    local cols = {}
    for _, c in ipairs(m.columns) do
      cols[#cols + 1] = "  " .. ctx:on(obj):quote(c.name)
    end
    local sql = "SELECT\n" .. table.concat(cols, ",\n") .. "\nFROM " .. obj.sql .. "\nWHERE 1 = 1;\n"
    -- Without UI ctx:editor_set raises "not available without UI":
    -- print the template instead.
    if pcall(ctx.editor_set, ctx, sql) then
      ctx:message("tpl: template for " .. obj.name .. " in the editor")
    else
      ctx:text(sql, "sql", { title = "tpl " .. obj.name })
    end
  end,
}
```

- The items of `ctx:pick` are tables with `label` (shown) and an extra field `name`; the chosen table itself comes back. `Esc` returns `nil`.
- `pcall(ctx.editor_set, ctx, sql)` – the method with `ctx` as first argument – catches the headless error.

In the UI (`F9`, `tpl`) the list "tpl: choose a table" appears with `logs  (table)`, `logs_archive  (table)`, `recent_errors  (view)`, `settings  (table)`; typing `sett` filters, `Enter` writes into the scratch:

```sql
SELECT
  `name`,
  `value`
FROM `dbcc_doc`.`settings`
WHERE 1 = 1;
```

and the log says `tpl: template for settings in the editor`. Headless:

```text
$ dbcc -p doc -c "tpl logs"
SELECT
  `id`,
  `level`,
  `msg`,
  `created`
FROM `dbcc_doc`.`logs`
WHERE 1 = 1;
$ dbcc -p doc -c "tpl"; echo $?
ERROR: tpl: ctx:pick: choosing from a list is not available without UI
1
```

Without the `pcall`, `ctx:editor_set` fails headless with `tpl: ctx:editor_set: the editor is not available without UI`.

### Tips: testing, debugging, quoting, safety

- **Testing without UI:** `dbcc -p <profile> -c "<command> <arguments>"` loads the scripts afresh on every call – save the file, repeat the command. Results go to stdout (`--format csv`/`json` for tests with `diff` or `jq`), messages to stderr, and the exit code tells success (`0`), run-time errors (`1`) and invocation errors (`2`). Write commands need `--yes`; `-v` shows every statement. Use a separate profile or a test database for tests.
- **Finding load errors:** headless they appear as `warning: user command script skipped: …` with file, line and column before any output; in the UI in the message log after the start or after `reload`.
- **Debugging:** `print(...)` and `ctx:message(...)` write to the message log or stderr. To print a table: `print(dbcc.fmt.table(res.columns, res))` shows a whole `ctx:query` result. `error("…")` without `0` names file and line.
- **Read the built-in scripts:** use `--export-commands` and take the scripts as templates – `tail.lua` (following, key choice), `cp.lua` (one confirmation with all statements), `cat.lua` (printing metadata), `history.lua` (`pcall` around `ctx:pick`).
- **Quote identifiers:** never assemble object names yourself; use `obj.sql` from `ctx:resolve` (qualified and quoted), and quote column names with `ctx:quote` or `ctx:on(obj):quote` – the dialect of the connection the SQL runs on.
- **Avoid SQL injection:** always pass values as parameters (`?`), never concatenate them into the SQL text with `..`. Identifiers cannot be parameters: they come from `ctx:resolve`/`ctx:meta` (so they exist) and are quoted. Where an argument may take only certain values, check it first (`string.match`, a list of allowed values).
- **Connections:** `ctx:on(obj)` for everything that concerns an object; `ctx.dialect` belongs to the current connection, `ctx:on(obj).dialect` to the object's.
- **Numbers:** `ctx:query` returns strings – `tonumber` before calculating or comparing; pass Lua numbers to `ctx:rows` for numeric columns.
- **Large results:** to display them use `ctx:grid` (loads page by page), not `ctx:query` + `ctx:rows` (buffered and cut off at `max_rows_buffered`, `res.capped`).

---

## Database specifics

### MySQL and MariaDB

- MariaDB is detected at connect from `VERSION()`; `ctx.dialect` is then `mariadb`.
- No schema level; database changes use `USE`.
- Unix sockets (`socket`), TLS through `params.tls`. `multiStatements` is off – dbcc splits statements itself.
- Cancel: `KILL QUERY <id>` on the auxiliary connection.
- Warnings: `SHOW WARNINGS` when `@@warning_count` is non-zero.
- DDL commits implicitly (CREATE, ALTER, DROP, RENAME, TRUNCATE, GRANT, REVOKE, LOCK, … except `CREATE TEMPORARY`); noted in manual-transaction profiles.
- `CALL` may return several result sets; each gets a tab.
- Replace scripts: MariaDB `CREATE OR REPLACE`, MySQL `DROP` + `CREATE` (with the privilege warning, see [Object tabs](#object-tabs-and-replace-scripts)), both with `DELIMITER`.
- Tree categories include Triggers and Events; Sequences on MariaDB.

### PostgreSQL

- Schema level; a connection is bound to one database. `cd` to another database reconnects the session (asking about an open transaction). The tree connects in the background when you expand another database.
- A schema change sets `search_path` to `"schema", public`.
- `du` and `grep` read the catalog of the current database only.
- Cancel through the driver's cancel request; the connection stays usable.
- NOTICE and WARNING messages appear as warnings in the message log.
- DDL is transactional; manual transactions with automatic `BEGIN` (see [Transactions and safety](#transactions-and-safety)).
- A statement returns at most one result set (procedures with OUT parameters return one row).
- Tree: materialized views, sequences (without the internal sequences of identity columns), functions and procedures (overloads merged, extension members omitted), triggers.
- Connects with application name `dbcc`, a 10 s default timeout and without a client-side statement cache (DDL never invalidates cached plans).

### Microsoft SQL Server

- Schema level. Database changes use `USE`. T-SQL has no switchable default schema: `cd` to a schema only checks that it exists; dbcc qualifies names in commands with the schema of the path, while unqualified names in your own SQL resolve against the login's default schema (usually `dbo`).
- Batches with `GO [n]`; a unit of execution is a whole batch. A batch that mixes DML and `SELECT` does not report affected rows.
- No server-side read-only mode: `read_only` works through the client guard and the login's privileges; behind an availability-group listener `params.ApplicationIntent = "ReadOnly"` routes to a readable secondary.
- Manual transactions through `SET IMPLICIT_TRANSACTIONS ON`, state through `@@TRANCOUNT`.
- Cancel through a TDS attention (not `KILL`, which would end the session). `kill -q` is not available.
- Informational messages (`PRINT`, `RAISERROR` with severity ≤ 10) are currently not shown.
- Metadata queries use three-part names (`[db].sys.…`) and work in any database.
- Named instances: `host = "server\\instance"` without a port.
- Replace scripts require the session to be in the object's database (`cd` there first).
- On the product name: T-SQL `DBCC …` statements pass through to the server as SQL.

---

## Files and directories

`XDG_CONFIG_HOME` and `XDG_STATE_HOME` are honoured.

| Path | Content |
|---|---|
| `~/.config/dbcc/config.toml` | settings, profiles, key bindings (0600 recommended; warning when wider) |
| `~/.config/dbcc/credentials.toml` | credential store (0600) |
| `~/.config/dbcc/commands/*.lua` | your own commands (loaded at start and by `reload`, see [Where the scripts are](#where-the-scripts-are)) |
| `~/.local/state/dbcc/history.jsonl` | statement history (JSON Lines: `time`, `profile`, `path`, `duration_ms`, `outcome`, `stmt`) |
| `~/.local/state/dbcc/scratches/<connection>/*.sql` | scratch files (`_none` = no connection) |
| `~/.local/state/dbcc/scratches/open.json` | the scratch tabs that were open, with cursor positions |
| `~/.local/state/dbcc/hang-<time>-<reason>.txt` | diagnostic dumps (see [Troubleshooting](#troubleshooting)) |
| `~/.my.cnf`, `~/.pgpass` (`$PGPASSFILE`) | read for `password = "mycnf"` / `"pgpass"` |

---

## Troubleshooting

### Keys do not arrive

- **F1, F10, F11** are intercepted by some terminal emulators: press `Esc` followed by the digit (within one second) or `Alt`+digit (`Esc 1` = F1, `Esc 0` = F10).
- **Alt+F1/Alt+F2** are taken by many desktops (window manager, launcher). Use `Tab`/`Shift+Tab` or `F9`, or rebind `focus_tree`/`focus_editor`/`focus_results` in `[keys.global]`.
- **Ctrl+Space** is often captured by input methods: use `Ctrl+O` (`completion_key_alt`).
- **Ctrl+Shift+C/X/V** are handled by many terminals themselves (copying the terminal selection, pasting). That is intended; a paste then arrives as bracketed paste. When the keys reach dbcc they act on the editor.
- **Shift+F-keys** on the Linux console (sent as F13–F24) are recognised.
- `Ctrl+Enter` cannot be told apart from `Enter` in most terminals and is therefore not bound.
- **Input seems stuck, keys are swallowed:** if a terminal ever produces a C1 control sequence (such as `ESC P`), input waits for its end. Press `Esc` and then `\` (the string terminator `ESC \`) to end it. dbcc already refuses bindings that would cause this (Alt + upper-case letter).

### Terminal and colours

dbcc needs no external terminfo files for `TERM=xterm-256color`, `screen-256color`, `tmux-256color` and `linux`. With fewer colours it degrades to 256, 16 or monochrome. Copying uses OSC 52; inside tmux enable `set -g set-clipboard on`.

### The UI does not respond

A watchdog checks every 2 seconds whether the UI answers. If it does not answer within 5 seconds, dbcc writes the stacks of all goroutines once per hang to `~/.local/state/dbcc/hang-YYYYMMDD-HHMMSS-ui-unresponsive.txt` (including whether the worker is busy and how many jobs are queued). To write a dump on demand:

```sh
pkill -QUIT dbcc
```

(file `hang-…-signal.txt`); dbcc keeps running. These files help with bug reports.

### Common messages

| Message | Meaning |
|---|---|
| `not connected: cd /<profile> first` | There is no session at `/`; connect first (F2 or `cd /profile`). |
| `no connection: F2 opens the connections` | SQL in the editor without a connection. |
| `read-only profile: statement rejected` | The profile's read-only guard. |
| `cancelled by user` | A confirmation was declined (headless: no `--yes`). |
| `confirmation required; run with --yes` | A write command headless without `--yes`. |
| `previous pages are not available: …` | Previous pages only for read queries. |
| `forms need a table-origin result …` | Forms only for results from `ll`, `head`, `tail` or the tree. |
| `config file … has permissions …` | `chmod 600 ~/.config/dbcc/config.toml`. |
| `passwords must not be stored in the profile file …` | A plain-text password in `config.toml`; use a password source. |
| `settings.page_size = …: must be at least 10` | `page_size` too small. |
| `user command script skipped: …` | Your Lua script has an error; a default command of the same name stays active. |

---

## About

**dbcc — Database Command & Control**
A product by **Datamedrix GmbH**.
Contact: Anton Grünberg · a.gruenberg@datamedrix.com · https://www.datamedrix.com

The About page (F1, then `F1` or `Tab`; or `help about`) shows the logo, version, build date and contact details.

**Third-party licences:** press `L` on the About page for the licence notices of the bundled libraries: a summary table (module, version, licence), then each full licence text. `Up`/`Down`/`PgUp`/`PgDn`/`Home`/`End` scroll, `L` or `Esc` returns to the About page. Without UI, `dbcc --licenses` prints the same text. The text is generated from the module cache by `make licenses` (`internal/branding/THIRD_PARTY_LICENSES.txt`); a test fails when `go.mod` changes and the file was not regenerated. The MySQL driver (`github.com/go-sql-driver/mysql`) is under MPL-2.0; dbcc uses it unmodified, its source code is available at https://github.com/go-sql-driver/mysql.
