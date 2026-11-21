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

*Run all* (`F6`) in an object tab replaces the object. It counts as a write: blocked in read-only profiles, and with `confirm_writes` it asks ("Replace X on prof?"). **On MySQL it always asks**, also without `confirm_writes`, with the note that `DROP` + `CREATE` is not atomic and that privileges granted on the routine are removed while `automatic_sp_privileges = 1`, the default (MariaDB replaces with `CREATE OR REPLACE` and keeps them). After a successful run the tab is marked saved and the log says "replaced <name>". `Alt+R` restores the source as loaded (as an undoable step). Closing a changed object tab, or quitting, asks first.

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

Every dbcc command – `cd`, `ll`, `cp` and `cat` included – is a Lua script. Go only provides primitives. The default scripts are embedded in the binary and double as examples.

### Loading, replacing, fallback

- The embedded scripts load first, then the user scripts `~/.config/dbcc/commands/*.lua` (or `$XDG_CONFIG_HOME/dbcc/commands`) in name order.
- A user command with the name of a default command **replaces** it – everywhere, including keys and tree actions, which invoke commands by name.
- A script that fails to load is reported in the message log and skipped; a default of the same name then stays active, so a broken `cd.lua` cannot lock you out. A script's registrations take effect only if the whole script loads without error.
- Loading a script may take at most 5 seconds.
- `dbcc --no-user-commands` loads the defaults only. `dbcc --export-commands <dir>` writes every embedded script to a directory as a starting point (creates it; never overwrites – if any target file exists, nothing is written).
- `help <command>` shows where a command comes from (`embedded:cat.lua` or the file path).

### Sandbox

Lua 5.1 (gopher-lua). Only the `base`, `table`, `string` and `math` libraries are loaded; `dofile`, `loadfile`, `require`, `module` and `newproxy` are removed. **No `os`, no `io`, no `debug`.** `load`/`loadstring` remain (code from strings only). File access happens only through primitives such as `ctx:export`. `print(...)` writes to the message log (headless: stderr). Every script gets its own global environment; the API tables `dbcc` and `dbcc.fmt` are read-only.

### `dbcc.command{…}`

Registers a command; allowed only while a script loads.

| Field | Type | Meaning |
|---|---|---|
| `name` | string, required | `^[a-z][a-z0-9_-]*$`; `dbcc` is reserved |
| `usage` | string | Usage line; generated from flags and args when omitted |
| `help` | string | Description (help overlay, `help`) |
| `args` | list of strings | Argument types: `object`, `path`, `profile`, `file`, `int`, `string`; suffix `?` = optional, `...` = zero or more (last only). A required argument cannot follow an optional one. Drives completion and the arity/`int` check. |
| `flags` | table name → type | `bool` (switch) or one of the argument types (flag with value; `int` is checked). Names: a letter, then letters, digits, `_`, `-`. |
| `flag_values` | table flag → list of strings | Optional: values offered by completion for a value flag (e.g. `flag_values = { t = { "table", "view" } }` in `cat`). Other values are still accepted. |
| `write` | boolean | `true`: the command writes – blocked in read-only profiles; `ctx:exec` shows each statement and asks |
| `run` | function, required | `run(ctx, args, flags)`: `args` are the positional arguments as strings, `flags` the given flags (switches `true`, values as strings) |

Other fields are an error. Report command errors with `error("text", 0)` (the `0` suppresses the position).

### `dbcc.commands()` and `dbcc.fmt`

| Function | Returns |
|---|---|
| `dbcc.commands()` | every command, sorted: `{name=, usage=, help=, args={…}, flags={name=type}, write=, source=, default=}` |
| `dbcc.fmt.table(headers, rows)` | an aligned text table as a string; `nil` = `NULL`; all-numeric columns right-aligned |
| `dbcc.fmt.size(bytes)` | `"512 B"`, `"16.0 KiB"`, `"1.5 MiB"`, … (`nil` → `""`) |
| `dbcc.fmt.count(n)` | `"1,234,567"` |

### The context `ctx`

`ctx` is valid only while its command runs; always call methods with a colon (`ctx:query(…)`).

Fields:

| Field | Content |
|---|---|
| `ctx.command` | name of the running command |
| `ctx.path` | current path: `{conn=, database=, schema=, str="/c/d/s", level="root"\|"connection"\|"database"\|"schema"}`; absent parts are `nil`; `tostring(ctx.path)` is `str` |
| `ctx.dialect` | `"mysql"`, `"mariadb"`, `"postgres"`, `"mssql"`, or `nil` when not connected |
| `ctx.has_schemas` | `true` on PostgreSQL and SQL Server |
| `ctx.profile` | `{name=, driver=, database=, read_only=, autocommit=, confirm_writes=}` or `nil` |
| `ctx.conn` | only on a context bound with `ctx:on`: the connection name |

**Navigation and metadata:**

| Method | Returns / does |
|---|---|
| `ctx:chdir(path)` | changes the path (like `cd`: `USE` or reconnect) and updates `ctx.path`, `ctx.dialect`, `ctx.profile`; returns the new path |
| `ctx:abspath(path)` | resolves a path without side effects (`"."` = current); returns a path table |
| `ctx:objects(path, kinds)` | at `/conn`: databases; at a database of a schema dialect: schemas; otherwise the objects, filtered by `kinds` (string or list: `database`, `schema`, `table`, `view`, `materialized view`, `procedure`, `function`, `trigger`, `event`, `sequence`). Elements `{name=, kind=, comment=, system=}`. An error at `/` (use `ctx:profiles()`) |
| `ctx:profiles()` | every profile, sorted: `{name=, driver=, host=, port=, socket=, user=, database=, read_only=, autocommit=, confirm_writes=, open=, current=}` |
| `ctx:resolve(name, kinds)` | resolves an object name or path: `{name=, kind=, comment=, conn=, sql=, parts={conn=, database=, schema=, object=}}`; `sql` is the qualified, quoted name; `tostring(obj)` is `sql` |
| `ctx:on(target)` | a context bound to the connection of `target` (profile name, resolved object or path table; opened when needed). Its SQL helpers and execution primitives run there; `ctx.path` stays the current path |
| `ctx:meta(obj)` | metadata of a table, view or materialized view (name or object), see below |
| `ctx:source(obj, kind)` | source of a view, procedure, function, trigger or event (`kind` optional) |

**SQL helpers:**

| Method | Returns |
|---|---|
| `ctx:quote(ident)` | the identifier quoted for the dialect |
| `ctx:limit(sql, n)` | a dialect-specific row limit; with a top-level `ORDER BY` the order decides which rows remain (MySQL/MariaDB/PostgreSQL append `LIMIT n`, SQL Server injects `TOP (n)`), otherwise the query is wrapped |
| `ctx:copy_sql(src, dst, opts)` | `stmts, target`: the statements that copy `src` to `dst`; `opts.structure = true` selects the `cp -a` variant; `target` is an object-like table for the new table |
| `ctx:replace_script(obj, kind)` | a runnable script that re-creates the object from its current source |

**Execution:**

| Method | Returns / does |
|---|---|
| `ctx:query(sql, ...)` | runs a query with parameters; buffered up to `max_rows_buffered`: an array of rows (each row an array of strings, `nil` for NULL; `row.colname` works too), plus `res.columns` (list of names) and `res.capped` |
| `ctx:exec(sql, ...)` | runs a statement; returns the affected rows. In `write` commands each statement is shown and confirmed first – unless `ctx:confirm` was already answered yes in this invocation |

Parameters are written as `?` in every dialect: when arguments are passed, dbcc rewrites each `?` outside strings, comments and quoted identifiers into the dialect's placeholder (`$1…` on PostgreSQL, `@p1…` on SQL Server). Without arguments the statement is sent unchanged (`?` is also a jsonb operator on PostgreSQL). Native placeholders work as well; mixing both in one statement does not. Parameter values: `nil`, booleans, numbers, strings. The profile's read-only guard and `confirm_writes` apply to `ctx:query` and `ctx:exec`.

**Output:**

| Method | Does |
|---|---|
| `ctx:grid(sql, opts, ...)` | opens a lazily loaded result (parameters after `opts`). `opts.origin` (an object from `ctx:resolve`) makes it table-origin and runs it on the object's connection; `opts.title` names the tab; `opts.follow = {sql=, key=, interval=}` makes it a following result (below) |
| `ctx:rows(headers, rows, opts)` | shows rows computed in Lua in a result tab; `nil` = NULL; numeric columns right-aligned; `opts.title` |
| `ctx:text(str, lexer, opts)` | shows text in a tab; `lexer = "sql"` = the dialect's SQL lexer, otherwise a chroma lexer name, `nil` = plain; `opts.title` |
| `ctx:message(msg)` | writes to the message log |
| `ctx:export(file, opts)` | writes the active result to `.csv`/`.json` (like `export`); `opts.force` overwrites, `opts.progress` = row interval of progress messages (0 = none); returns the number of rows |

`opts.follow`: `follow.sql` is a query with exactly one `?` for the last seen value of the result column `follow.key`, returning the newer rows ordered by it ascending. It runs every `interval` seconds (default 2, at least 0.5) in the background on the auxiliary connection; its rows are appended. `ctx:grid` returns at once.

**Interaction:**

| Method | Does |
|---|---|
| `ctx:confirm(msg)` | asks yes or no; returns a boolean |
| `ctx:pick(items, opts)` | a filterable pick list; `items` are strings or tables with a `label`; returns `item, index`, or `nil` when cancelled; `opts.title` |
| `ctx:editor_set(text)` | replaces the text of the current scratch tab (or of the first scratch, or a new one) |
| `ctx:editor_open(title, text, opts)` | opens or focuses an editor tab; `opts.object` (from `ctx:resolve`) binds it as an object tab; without an object a new scratch with the text is created |
| `ctx:about()` | shows the About page |
| `ctx:history(n)` | the last `n` history entries (default 100), oldest first: `{time=, profile=, path=, duration_ms=, ok=, outcome=, stmt=}` |

**Headless:** `ctx:grid`, `ctx:rows`, `ctx:text`, `ctx:about` and `ctx:editor_open` print to stdout; `ctx:confirm` fails unless `--yes` is given; `ctx:pick` and `ctx:editor_set` fail with "not available without UI" (catchable with `pcall`, as `history.lua` does).

**Metadata table returned by `ctx:meta`:**

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

### A complete example

A command `counts` that counts the rows of every table of a database (or schema), optionally only tables with at least a minimum number of rows:

```lua
-- ~/.config/dbcc/commands/counts.lua
dbcc.command{
  name  = "counts",
  usage = "counts [-m min] [path]",
  help  = "Row count of every table of a database or schema; -m shows only tables with at least min rows",
  args  = { "path?" },
  flags = { m = "int" },
  run = function(ctx, args, flags)
    local p = ctx:abspath(args[1] or ".")
    if not p.database then
      error("counts needs a database: cd into one or give its path", 0)
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

Use it as `counts`, `counts -m 1000`, `counts /pg/app/public`, or headless `dbcc -p prod -c "counts -m 1" --format csv`. Argument completion offers paths for `[path]` because the argument is declared as `path`.

To change a default command, export it with `--export-commands`, copy the file to `~/.config/dbcc/commands/` and edit it – for example the layout of `cat.lua`.

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
| `~/.config/dbcc/commands/*.lua` | your own commands |
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
