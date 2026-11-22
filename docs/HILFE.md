# dbcc — Database Command & Control: Benutzerhandbuch

[English version: README.md](../README.md) · [Spezifikation: SPEC.md](SPEC.md)

dbcc ist ein tastaturgesteuerter Vollbild-Datenbankclient für das Terminal. Er besteht aus einer einzigen statisch gelinkten Datei, läuft in jeder SSH-Sitzung und unterstützt MySQL, MariaDB, PostgreSQL und Microsoft SQL Server. Ein Produkt der Datamedrix GmbH.

Dieses Handbuch beschreibt den gesamten Funktionsumfang so, wie er im Code umgesetzt ist. Tastennamen sind so geschrieben, wie dbcc sie in der Hilfe anzeigt (`Alt+F1`, `Shift+F6`, `Ctrl+Space`).

## Inhalt

- [1. Überblick](#1-überblick)
- [2. Installation und Start](#2-installation-und-start)
- [3. Bildschirmaufbau](#3-bildschirmaufbau)
- [4. Verbindungen](#4-verbindungen)
- [5. Pfade und Navigation](#5-pfade-und-navigation)
- [6. Objektbaum](#6-objektbaum)
- [7. Editor und Scratches](#7-editor-und-scratches)
- [8. SQL ausführen](#8-sql-ausführen)
- [9. Ergebnisse](#9-ergebnisse)
- [10. Zeilenformular](#10-zeilenformular)
- [11. Kommandozeile und Befehle](#11-kommandozeile-und-befehle)
- [12. Vervollständigung](#12-vervollständigung)
- [13. Transaktionen und Sicherheit](#13-transaktionen-und-sicherheit)
- [14. Headless-Betrieb und Skripte](#14-headless-betrieb-und-skripte)
- [15. Konfiguration](#15-konfiguration)
- [16. Tastenreferenz](#16-tastenreferenz)
- [17. Eigene Befehle in Lua](#17-eigene-befehle-in-lua)
- [18. Datenbankspezifisches](#18-datenbankspezifisches)
- [19. Dateien und Verzeichnisse](#19-dateien-und-verzeichnisse)
- [20. Fehlerbehebung](#20-fehlerbehebung)
- [21. Über dbcc](#21-über-dbcc)

---

## 1. Überblick

dbcc vereint in einem Programm:

- **Shell-artige Befehle** für Datenbankobjekte: `cd`, `ls`, `pwd`, `ll`, `head`, `tail` (auch `tail -f`), `wc`, `du`, `cat`, `edit`, `cp`, `mv`, `grep`, `ps`, `kill`, `export`, `history`, `help`.
- Einen **SQL-Editor** mit Syntaxhervorhebung je Dialekt, Tabs, automatisch gespeicherten Scratch-Dateien und einer Vervollständigung, die nur auf Tastendruck erscheint.
- Einen **Skript-Runner** mit Statement-Zerlegung (MySQL `DELIMITER`, PostgreSQL Dollar-Quoting, SQL Server `GO n`), Abbruch beim ersten Fehler mit Zeilennummer.
- Ein **Ergebnis-Grid** mit seitenweisem Nachladen, horizontalem Scrollen (nie Zeilenumbruch), Sortieren, drei Filterarten, Pivot-Ansicht, Zellendetail und Kopieren über die Zwischenablage (OSC 52).
- Ein **Zeilenformular** zum Einfügen und Bearbeiten von Datensätzen mit NULL-Behandlung, Fremdschlüssel-Suchfenster und Zuordnung von Serverfehlern zum betroffenen Feld.
- Eine **Befehlsschicht, die vollständig in Lua geschrieben ist**. Jeder Befehl ist ein Lua-Skript; eigene Skripte ergänzen oder ersetzen die eingebauten.
- Einen **Headless-Modus** für Skripte und einzelne Befehle ohne Oberfläche, mit Exit-Code.

Unterstützte Datenbanken: **MySQL**, **MariaDB** (wird beim Verbinden automatisch erkannt), **PostgreSQL** und **Microsoft SQL Server**.

Bewusst nicht enthalten: kein `rm`- oder Drop-Befehl (Löschen nur per SQL), kein Löschen von Zeilen aus dem Grid, kein Vim-Modus, keine `$EDITOR`-Übergabe, kein eingebauter SSH-Tunnel, keine BLOB-Bearbeitung im Formular.

---

## 2. Installation und Start

### Bauen

Voraussetzungen: Go in der Version aus `go.mod` (derzeit `go 1.27.3`), Ziel ist Linux amd64.

```sh
make build          # erzeugt bin/dbcc (CGO_ENABLED=0, GOOS=linux, GOARCH=amd64, -trimpath, -s -w)
make static-check   # prüft, dass bin/dbcc statisch gelinkt ist
make test           # Unit-Tests
make itest          # Integrationstests gegen echte Server (siehe make test-servers)
```

`make build` setzt Version (`git describe`) und Build-Datum in das Binary. Das Ergebnis ist eine einzelne statische Datei ohne Laufzeitabhängigkeiten; sie kann einfach auf den Zielrechner kopiert werden.

### Aufruf

```text
dbcc [profile]                          UI starten, optional gleich verbinden
dbcc -p <profile> -f <file.sql>         Skript ohne UI ausführen
dbcc -p <profile> -c "<command|SQL>"    einen Befehl oder ein Statement ohne UI ausführen
dbcc -p <profile> < file.sql            stdin ist kein Terminal: ohne UI ausführen
```

| Option | Bedeutung |
|---|---|
| `-p <profile>` | Verbindungsprofil. Alternativ als einziges Positionsargument (`dbcc prod`). Beides gleichzeitig mit verschiedenen Namen ist ein Fehler. |
| `-f <file>` | SQL-Skript ohne UI ausführen. Nicht mit `-c` kombinierbar. |
| `-c "<text>"` | Einen Befehl (erstes Wort ist ein registrierter Befehl) oder SQL ohne UI ausführen. |
| `--format table\|csv\|tsv\|json` | Ausgabeformat für Ergebnisse im Headless-Modus (Standard `table`). |
| `--yes` | Rückfragen im Headless-Modus mit Ja beantworten. |
| `-v` | Headless: jedes Statement mit Zeile, Zeilenzahl und Dauer auf stderr protokollieren. |
| `--no-splash` | Startbildschirm überspringen. |
| `--no-user-commands` | Nur die eingebauten Befehle laden (keine Skripte aus `~/.config/dbcc/commands`). |
| `--export-commands <dir>` | Die eingebauten Befehlsskripte in ein Verzeichnis schreiben und beenden. |
| `--config <file>` | Andere Konfigurationsdatei statt `~/.config/dbcc/config.toml`. |
| `--version` | Version ausgeben (`dbcc <version>`). |
| `--licenses` | Lizenzhinweise der Go-Standardbibliothek und aller mitgelieferten Fremdmodule ausgeben und beenden. |
| `-h`, `--help` | Aufrufhilfe. |

Der Headless-Modus wird gewählt, sobald `-f` oder `-c` angegeben ist **oder stdin kein Terminal ist**. Details und Exit-Codes: [Abschnitt 14](#14-headless-betrieb-und-skripte).

### Startbildschirm

Beim Start der UI erscheint für 3 Sekunden der Startbildschirm (Datamedrix-Logo als Textgrafik, Titel `dbcc`, „Database Command & Control“, Version und Build-Datum, „A product by Datamedrix GmbH“, Kontakt, E-Mail und Webadresse). Jede Taste schließt ihn sofort. Konfiguration, Lua-Befehle und der Verbindungsaufbau laufen bereits dahinter; eine Passwortabfrage erscheint erst nach dem Startbildschirm. Abschalten mit `splash = false` oder `--no-splash`. Im Headless-Modus gibt es keinen Startbildschirm.

---

## 3. Bildschirmaufbau

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
| /prod/shop > _                                                 Kommandozeile |
|  prod | autocommit | rows 1–3,000 · page 1 + | 38 ms | sort: id ▲           |
| 1Help 2Conn 3View 4Edit 5Reload 7New 8Cancel 9Cmd 10Quit                    |
+-----------------------------------------------------------------------------+
```

- **Objektbaum** links (32 Spalten breit). Der Titel zeigt die aktuelle Verbindung, beim Filtern zusätzlich den Filtertext.
- **Editor** rechts oben mit Tab-Leiste. Ein `*` hinter dem Titel markiert ungespeicherte Änderungen.
- **Ergebnisbereich** rechts unten mit Tabs: je Ergebnis ein Tab `N Titel`, dazu immer der Tab **Messages** (ganz rechts). Ein `*` hinter einem Ergebnistitel bedeutet *partial* oder *capped* (siehe [Abschnitt 9](#9-ergebnisse)).
- **Kommandozeile**: Die Eingabeaufforderung zeigt den aktuellen Pfad (`/prod/shop > `).
- **Statuszeile**, Felder durch `|` getrennt: Profil (oder `not connected`), `READ ONLY` bei schreibgeschützten Profilen, Transaktionszustand (`autocommit`, `manual txn` oder fett `TXN OPEN`), `running… (F8 cancels)` während ein Statement läuft, Zeilenzahl des aktuellen Ergebnisses, Dauer, aktive Sortierung (`sort: …`), aktive Filter (`filter: …`), Hinweise (z. B. `sorted within 3,000 loaded rows`, `skipping 6,000 rows…`, `last page`) und die letzte Fehlermeldung.
- **Fußzeile** mit den F-Tasten-Beschriftungen **des fokussierten Bereichs**; sie wechselt mit dem Fokus.

Fokus wechseln: `Tab` / `Shift+Tab` zyklisch Baum → Editor → Ergebnis → Kommandozeile; direkt mit `Alt+F1` (Baum), `Alt+F2` (Editor), `Alt+F3` (Ergebnis) und `F9` (Kommandozeile).

**Volle Höhe (`F11`):** `F11` gibt dem Bereich mit dem Fokus – Editor oder Ergebnis – die volle Höhe der rechten Spalte; der andere Bereich wird ausgeblendet. Nochmals `F11` stellt die ursprüngliche Aufteilung wieder her. Wechselst du im maximierten Zustand in den ausgeblendeten Bereich (`Tab`, `Alt+F2`, `Alt+F3`), wird die Aufteilung automatisch wiederhergestellt. Im Baum oder in der Kommandozeile meldet `F11` einen Hinweis. Viele Terminals (z. B. GNOME Terminal) belegen `F11` selbst mit „Vollbild“; dann die Taste unter `[keys.global]` umlegen, etwa `maximize = "alt+m"`.

Ein neues Ergebnis bekommt den Fokus nur, wenn die Kommandozeile oder das Grid fokussiert war. Wer im Baum oder im Editor arbeitet, behält den Fokus dort.

Farben: Auf Terminals mit 256 oder mehr Farben verwendet dbcc das Datamedrix-Blau (#0069B4) für Rahmen und Statuszeile, darunter die 16 Standardfarben, unter 16 Farben monochrom.

---

## 4. Verbindungen

### Verbindungsmanager (F2)

`F2` öffnet die Liste der gespeicherten Verbindungen. Jede Zeile zeigt Name, Datenbanktyp, `user@host:port/datenbank` und Merkmale (`read-only`, `manual txn`, `confirm writes`). Offene Verbindungen sind mit `● open`, die aktuelle mit `● current` markiert. Unter der Liste stehen die Pfade der Konfigurationsdatei und des Passwortspeichers.

| Taste | Wirkung |
|---|---|
| `Enter` | Verbinden (entspricht `cd /<name>`). Auf „+ New connection…“: neue Verbindung anlegen. |
| `n` | Neue Verbindung anlegen. |
| `e` | Ausgewählte Verbindung bearbeiten. |
| `c` | Ausgewählte Verbindung kopieren (Vorschlag: `<name>-copy`). |
| `d` | Verbindung und ihr gespeichertes Passwort löschen (mit Rückfrage; eine offene Verbindung wird vorher geschlossen). |
| `x` | Offene Verbindung trennen. Bei offener Transaktion wird gefragt: Commit, Roll back oder Stay. |
| `Esc` | Schließen. |

### Verbindung anlegen und bearbeiten

Das Formular enthält:

| Feld | Bedeutung |
|---|---|
| Name | Wird zum ersten Pfadelement `/<name>`. Erlaubt: Buchstaben, Ziffern, `-`, `_`, `.`; beginnt mit Buchstabe oder Ziffer; höchstens 64 Zeichen. Muss eindeutig sein. |
| Database | MySQL / MariaDB, PostgreSQL oder SQL Server. Beim Wechsel wird ein Standardport (3306, 5432, 1433) übernommen, solange der Port noch ein Standardport oder leer ist. |
| Host, Port | Host oder Socket ist Pflicht. |
| Socket (optional) | Unix-Socket (MySQL/MariaDB: Socketdatei; PostgreSQL: Socketverzeichnis oder Socketdatei `…/.s.PGSQL.5432`). |
| User | Benutzername. |
| Password | Maskierte Eingabe. Bei einem gespeicherten Passwort: leer lassen, um es zu behalten. |
| Password storage | `save with the connection` (= `stored`, Passwortspeicher), `ask when connecting` (= `prompt`), `environment variable` (= `env:NAME`), `~/.my.cnf` (= `mycnf`), `~/.pgpass` (= `pgpass`). |
| Env variable | Name der Umgebungsvariable, nur bei „environment variable“. |
| Default database | Datenbank, auf der die Verbindung landet. |
| Read only, Autocommit, Confirm writes | Siehe [Abschnitt 13](#13-transaktionen-und-sicherheit). |
| Driver options | Treiberparameter als `key=value, key=value`, z. B. `tls=skip-verify` oder `sslmode=require`. Siehe [Abschnitt 15](#15-konfiguration). |

Schaltflächen: **Test** (baut eine Probeverbindung auf und meldet „Connection works (mysql)“ bzw. den Fehler), **Save**, **Save & connect**, **Cancel**. `Tab`/`Shift+Tab` bewegen zwischen den Feldern, `Enter` auf einer Auswahlliste öffnet sie, `Esc` bricht ab.

Gespeichert wird in `config.toml` (Abschnitt `[profiles.<name>]`, übrige Inhalte und Kommentare der Datei bleiben erhalten), das Passwort getrennt im Passwortspeicher `credentials.toml` (Modus 0600). Wählt man eine andere Passwortquelle als `stored`, wird ein gespeichertes Passwort entfernt. Änderungen an einer offenen Verbindung wirken erst nach dem Neuverbinden (F2, `x`, dann wieder verbinden).

### Passwortquellen

| `password =` | Verhalten |
|---|---|
| `"prompt"` (oder leer) | Abfrage beim Verbinden (UI: maskiertes Eingabefenster; headless: auf `/dev/tty`). |
| `"stored"` | Passwort aus `credentials.toml`. Ist noch keines gespeichert, wird gefragt und das Passwort nach erfolgreichem Verbinden automatisch gespeichert. |
| `"env:NAME"` | Inhalt der Umgebungsvariable `NAME`; Fehler, wenn sie nicht gesetzt ist. |
| `"mycnf"` | Abschnitt `[client]` aus `~/.my.cnf`: `password`, und – soweit im Profil nicht gesetzt – `user`, `host`, `port`, `socket`. |
| `"pgpass"` | Erste passende Zeile aus `$PGPASSFILE` bzw. `~/.pgpass` (Host – Standard `localhost` –, Port, Datenbank, Benutzer; `*` als Platzhalter). |

Ein Passwort im Klartext in `config.toml` wird abgelehnt: Die Datei lädt dann nicht.

---

## 5. Pfade und Navigation

Jeder Ort hat einen Pfad der Form `/verbindung/datenbank[/schema]`:

| Ebene | Beispiel | `ls` zeigt |
|---|---|---|
| Wurzel | `/` | die Verbindungsprofile (mit Treiber, Host, Datenbank, `(current)`/`(open)`) |
| Verbindung | `/prod` | die Datenbanken |
| Datenbank | `/prod/shop` | MySQL/MariaDB: die Objekte; PostgreSQL/SQL Server: die Schemata |
| Schema | `/pg/app/public` | die Objekte (nur PostgreSQL und SQL Server) |

MySQL und MariaDB haben keine Schemaebene.

`cd` (siehe [Befehle](#cd)) ändert den Pfad: `cd shop`, `cd ..`, `cd /prod/shop`, `cd -` (vorheriger Pfad), `cd` allein (Standarddatenbank des aktuellen Profils). Mehrere Verbindungen können gleichzeitig offen sein; `cd /andere/db` wechselt zwischen ihnen. Der Objektbaum zeigt immer die aktuelle Verbindung und folgt dem Pfad.

Beim Wechsel auf eine andere Verbindung landet man auf deren aktueller Datenbank (und bei Schema-Dialekten auf deren aktuellem Schema). `cd ..` innerhalb derselben Verbindung bleibt auf der jeweils höheren Ebene.

Objektnamen in Befehlen werden gegen den aktuellen Pfad aufgelöst: `obj`, `db.obj` (MySQL/MariaDB), `schema.obj` und `db.schema.obj` (PostgreSQL/SQL Server), mit SQL-Quoting der Teile (`"…"`, `` `…` ``, `[…]`). Außerdem sind absolute oder relative Pfade möglich, deren letztes Element das Objekt ist: `/prod/shop/customers`, `/pg/app/public/orders`, `../crm/contacts` – auch auf einer anderen Verbindung, die dann bei Bedarf geöffnet wird. Wird ein Name nicht exakt gefunden, gilt ein eindeutiger Treffer ohne Beachtung der Groß-/Kleinschreibung.

---

## 6. Objektbaum

Der Baum ist dem Datenbank-Explorer von DataGrip nachempfunden.

- **Oberste Ebene:** alle Datenbanken, die das Konto der aktuellen Verbindung sehen darf. Systemdatenbanken (MySQL: `information_schema`, `mysql`, `performance_schema`, `sys`; SQL Server: `master`, `model`, `msdb`, `tempdb`; PostgreSQL: Template-Datenbanken) stehen am Ende und sind abgedimmt.
- **PostgreSQL und SQL Server** haben darunter die Schemaebene (Systemschemata zuletzt, abgedimmt). Auf PostgreSQL baut das Aufklappen einer anderen Datenbank im Hintergrund eine Verbindung zu ihr auf.
- **Kategorien** mit Objektanzahl: Tables, Views, Materialized views (PostgreSQL), Procedures, Functions, Triggers, Events (MySQL/MariaDB), Sequences (MariaDB, PostgreSQL, SQL Server). Leere Kategorien werden ausgeblendet.
- **Unter einer Tabelle:** Columns (Name, Typ, Markierungen `PK`, `FK`, `not null`, `auto`, `generated`), Keys, Foreign keys (mit Ziel `→ tabelle (spalten)`), Indexes, Triggers. Eine View klappt in ihre Spalten auf.
- Knoten laden erst beim Aufklappen, über die Hilfsverbindung. Baum und Vervollständigung funktionieren deshalb auch, während auf der Hauptsitzung ein Statement läuft.

| Taste | Wirkung |
|---|---|
| `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` | Bewegen |
| `Right` | Aufklappen, bzw. zum ersten Kind springen |
| `Left` | Zuklappen, bzw. zum Elternknoten springen |
| `Enter` | Standardaktion (siehe Tabelle unten) |
| `F3` | `cat` des Objekts |
| `F4` | `edit`: Quelltext in einem Editor-Tab (nicht für Tabellen) |
| `F5` | Knoten neu laden (Metadaten-Cache für den Knoten verwerfen) |
| `F7` | Neue Zeile für die ausgewählte Tabelle (Zeilenformular) |
| `/` | Baum nach Namen filtern; `Esc` im Filterfeld löscht den Filter, `Enter` behält ihn |
| `c` | Aktuellen Pfad auf den ausgewählten Knoten setzen (`cd`) |

Aktionen je Knotenart:

| Knoten | Enter | F3 | F4 | F7 |
|---|---|---|---|---|
| Datenbank, Schema, Kategorie, Gruppe | auf-/zuklappen | – | – | – |
| Tabelle | `ll`: Daten im Grid | `cat`: Struktur | – | neue Zeile |
| View, Materialized view | `ll`: Daten im Grid | `cat` | `edit` | – |
| Prozedur, Funktion, Trigger, Event | `edit` | `cat`: Quelltext | `edit` | – |
| Spalte, Schlüssel, Fremdschlüssel, Index | – | `cat` der Tabelle | – | – |

Baum und Pfad sind unabhängig: Ein Objekt aus dem Baum zu öffnen verwendet seinen vollständigen Pfad und ändert den aktuellen Pfad nicht. Nur `c` setzt den Pfad; umgekehrt bewegt `cd` die Auswahl im Baum auf den neuen Pfad. Alle Baumaktionen rufen die Lua-Befehle über ihren Namen auf – ein ersetzter Befehl ändert also auch das Verhalten des Baums.

Mit `mouse = true`: Klick wählt aus, Doppelklick wirkt wie Enter, das Mausrad scrollt. Zum Markieren von Text im Terminal dann `Shift` gedrückt halten.

---

## 7. Editor und Scratches

### Der Editor

Ein einfacher, nicht-modaler Mehrzeilen-Editor mit Readline-artigen Tasten, Zeilennummern, Auswahl, Undo/Redo und Bracketed Paste (eingefügter Text wird als ein Undo-Schritt eingefügt und nie als Tasten interpretiert). Die Hervorhebung folgt dem Dialekt der aktuellen Verbindung (Lexer `mysql`, `postgresql` oder `tsql`). Das Statement unter dem Cursor – das, was `F5` ausführen würde – ist dezent markiert.

Bearbeitungstasten (wie sie die F1-Hilfe im Editor anzeigt):

| Taste | Wirkung |
|---|---|
| `Left`/`Right`, `Ctrl+B`/`Ctrl+F` | Zeichen links / rechts |
| `Alt+B`/`Alt+F`, `Ctrl+Left`/`Ctrl+Right` | Wort links / rechts |
| `Up`/`Down`, `Ctrl+P`/`Ctrl+N` | Zeile hoch / runter |
| `Home`/`End`, `Ctrl+A`/`Ctrl+E` | Zeilenanfang / -ende (`Home` wechselt zwischen erstem Nicht-Leerzeichen und Spalte 0) |
| `PgUp`/`PgDn` | Seite hoch / runter |
| `Ctrl+Home`/`Ctrl+End`, `Alt+<`/`Alt+>` | Pufferanfang / -ende |
| `Shift`+Bewegung | Auswahl erweitern |
| `Alt+A` | Alles auswählen |
| `Ctrl+Shift+C` (auch `Ctrl+C`) | Auswahl in die Zwischenablage kopieren |
| `Ctrl+Shift+X` (auch `Ctrl+X`) | Auswahl ausschneiden, in die Zwischenablage |
| `Ctrl+Shift+V` | Einfügen: das Einfügen des Terminals; sonst der zuletzt kopierte Text |
| `Ctrl+W` | Wort rückwärts löschen, bzw. Auswahl ausschneiden |
| `Ctrl+K` | Bis Zeilenende löschen (am Zeilenende: Zeilen verbinden) |
| `Ctrl+U` | Bis Zeilenanfang löschen |
| `Alt+D` | Wort vorwärts löschen |
| `Alt+Backspace` | Wort rückwärts löschen |
| `Ctrl+Z`, `Ctrl+_` | Rückgängig |
| `Alt+Z` | Wiederholen |
| `Ctrl+D`, `Delete` | Zeichen löschen |
| `Backspace` | Zeichen rückwärts löschen |
| `Enter` | Neue Zeile mit Einrückung |
| `Alt+I` | Einrückung einfügen |

Gelöschter („gekillter“), kopierter und ausgeschnittener Text kommt in einen internen Puffer und zusätzlich per OSC 52 in die Zwischenablage des Terminals. `Tab` fügt keine Einrückung ein, sondern wechselt den Bereich.

Viele Terminals verarbeiten `Ctrl+Shift+C`/`Ctrl+Shift+V` selbst (Kopieren der Terminal-Markierung, Einfügen per Bracketed Paste). Erreichen die Tasten dbcc, wirken sie auf den Editor. Ältere Terminals senden `Ctrl+Shift+X` als `Ctrl+X`, das ebenfalls ausschneidet. **`Ctrl+C` beendet dbcc nie**: Im Editor kopiert es, anderswo zeigt die Statuszeile „Ctrl+C does not quit dbcc — F10 quits“.

### Editor-Tabs

| Taste | Wirkung |
|---|---|
| `Alt+,` / `Alt+.` | Vorheriger / nächster Tab |
| `Alt+N` | Neuer Scratch |
| `Alt+O` | Scratch öffnen (Liste aller Scratches) |
| `Alt+E` | Aktuellen Scratch umbenennen |
| `Alt+X` | Tab schließen |
| `Alt+R` | Objekt-Tab: Quelltext wie geladen wiederherstellen |

Es gibt zwei Arten von Tabs:

- **Scratch-Tabs** für eigenes SQL (siehe unten).
- **Objekt-Tabs**, geöffnet mit `edit` (Baum `Enter`/`F4` oder Kommandozeile): einer pro Objekt, mit dem Objektnamen als Titel. Ist das Objekt schon offen, wird sein Tab fokussiert.

### Scratches

Scratches funktionieren wie die Scratch-Dateien von DataGrip:

- Jeder Scratch ist eine Datei unter `~/.local/state/dbcc/scratches/<verbindung>/`, benannt nach dem Erstellungszeitpunkt: `scratch_YYYY-MM-DD_HH-MM-SS.sql` (bei Gleichstand mit `-2`, `-3` …). Scratches ohne Verbindung liegen im Ordner `_none`. Ein Scratch gehört zu der Verbindung, die beim Anlegen aktuell war.
- Der Tab-Titel lautet `scratch 2026-11-21 09:14`, nach dem Umbenennen der gewählte Name.
- Gespeichert wird automatisch etwa 1 Sekunde nach der letzten Änderung sowie beim Beenden.
- `Alt+X` speichert vor dem Schließen; schlägt das Speichern fehl, bleibt der Tab offen und eine Meldung erklärt den Grund. Wird der letzte Tab geschlossen, legt dbcc einen neuen leeren Scratch an – es gibt immer mindestens einen Tab.
- Beim nächsten Start werden die zuletzt offenen Scratch-Tabs mit Cursorposition wiederhergestellt (Index `open.json`).
- **Umbenennen** (`Alt+E`, oder `Alt+R` in der Scratch-Liste): Name 1–100 Zeichen, ohne `/`, `\`, Steuerzeichen, nicht mit `.` beginnend; `.sql` wird angehängt. Namen sind je Verbindung eindeutig, ohne Beachtung der Groß-/Kleinschreibung. Bei einem Fehler bleibt der Dialog offen.

**Scratch-Liste (`Alt+O`):** alle Scratches aller Verbindungen, neueste zuerst, mit Verbindung, Änderungszeit, `● open` und einer Vorschau der ersten Zeile. Das Suchfeld filtert nach Verbindung, Name und Inhalt.

| Taste | Wirkung |
|---|---|
| Tippen | Filtern |
| `Up`/`Down`/`PgUp`/`PgDn` | Auswahl bewegen |
| `Enter` | Öffnen |
| `Alt+R` | Umbenennen |
| `Alt+D` | Löschen (mit Rückfrage, nicht rückgängig zu machen) |
| `Esc` | Schließen |

### Objekt-Tabs und Replace-Skripte

`edit` lädt den aktuellen Quelltext einer View, Prozedur, Funktion, eines Triggers, Events oder einer Materialized view als **ausführbares Skript, das das Objekt ersetzt**:

| Dialekt | Form des Skripts |
|---|---|
| MariaDB | `CREATE OR REPLACE …` |
| MySQL | View: `CREATE OR REPLACE VIEW`; Routine, Trigger, Event: `DROP … IF EXISTS` und danach `CREATE …` |
| MySQL, MariaDB | Körper von Routinen, Triggern und Events in `DELIMITER`-Zeilen eingeschlossen, damit *Run all* das Skript unverändert ausführen kann |
| PostgreSQL | View, Funktion, Prozedur (jede Überladung): `CREATE OR REPLACE`; Trigger: `CREATE OR REPLACE TRIGGER` ab PostgreSQL 14, sonst (und für Constraint-Trigger) `DROP TRIGGER IF EXISTS … ON tabelle` plus `CREATE`; Materialized view: `DROP MATERIALIZED VIEW IF EXISTS`, `CREATE`, ihre Indizes und ihr Kommentar |
| SQL Server | GO-Batches: ein Schutz-Batch, der mit `THROW` abbricht, wenn die Sitzung nicht in der Datenbank des Objekts ist; ggf. `SET ANSI_NULLS`/`QUOTED_IDENTIFIER OFF` wie beim Anlegen; der Modultext mit `CREATE OR ALTER` und `[schema].[name]` (vor SQL Server 2016: Stub plus `ALTER`); am Ende die Optionen wieder `ON` |

*Run all* (`F6`) in einem Objekt-Tab ersetzt das Objekt. Das zählt als Schreibvorgang: In schreibgeschützten Profilen ist es gesperrt, bei `confirm_writes` wird gefragt („Replace X on prof?“). **Auf MySQL wird immer gefragt**, auch ohne `confirm_writes`, mit dem Hinweis, dass `DROP` + `CREATE` nicht atomar ist und Rechte auf der Routine bei `automatic_sp_privileges = 1` – dem Standard – verloren gehen (MariaDB ersetzt mit `CREATE OR REPLACE` und behält die Rechte). Nach erfolgreichem Lauf gilt der Tab als gespeichert und die Meldung „replaced \<name>“ erscheint. `Alt+R` stellt den Quelltext wie geladen wieder her (als rückgängig machbarer Schritt). Ein Objekt-Tab mit Änderungen fragt beim Schließen und beim Beenden nach.

---

## 8. SQL ausführen

### Statement und Skript

| Taste (Editor) | Wirkung |
|---|---|
| `F5` | **Run statement**: das Statement unter dem Cursor |
| `F6` | **Run all**: die Auswahl, oder – ohne Auswahl – den ganzen Puffer des aktuellen Tabs |
| `F8` | Laufendes Statement abbrechen |

SQL kann auch in der Kommandozeile eingegeben werden: Ist das erste Wort kein Befehl, wird die ganze Zeile als SQL-Skript ausgeführt (mehrere Statements mit `;` sind möglich).

Ablauf:

- Statements laufen nacheinander auf der **Hauptsitzung** der aktuellen Verbindung (einer fest zugeordneten Verbindung, nie einem Pool). Sitzungszustand wie aktuelle Datenbank, Variablen, temporäre Tabellen und Transaktionen bleibt erhalten.
- **Beim ersten Fehler stoppt der Lauf.** Messages-Log und Statuszeile zeigen Fehler, Statement und Zeilennummer (`line 12: …`), der Editor-Cursor springt auf diese Zeile und der Messages-Tab wird angezeigt. Wird eine Auswahl ausgeführt, beziehen sich die Zeilennummern auf den ganzen Puffer.
- Jedes Statement erzeugt einen Eintrag im **Messages-Log**: Uhrzeit, Zeile, gekürzter Text, `N rows` bzw. `N rows affected` bzw. `ok` oder `ERROR …`, Dauer, Hinweise und Serverwarnungen (MySQL/MariaDB: `SHOW WARNINGS` bei Warnungszahl größer 0; PostgreSQL: NOTICE/WARNING-Meldungen). Das Log hält die letzten 5000 Zeilen und scrollt horizontal.
- **Jede Ergebnismenge bekommt einen eigenen Tab.** Prozeduren mit mehreren Ergebnismengen (MySQL `CALL`, SQL-Server-Batches mit `EXEC` oder mehreren `SELECT`) erzeugen mehrere Tabs.
- Das letzte Statement eines Laufs mit genau einer Ergebnismenge bleibt **lazy** geöffnet (seitenweises Nachladen, siehe [Abschnitt 9](#9-ergebnisse)). Alle anderen Ergebnisse werden bis `max_rows_buffered` (Standard 10.000) gepuffert; wird die Grenze erreicht, ist das Ergebnis *capped*.
- Ein Rollback des ganzen Skripts wird nicht versprochen; auf MySQL/MariaDB committet DDL implizit.

### Statement-Zerlegung

| Dialekt | Regeln |
|---|---|
| MySQL / MariaDB | Trennzeichen `;`. `DELIMITER <token>`-Zeilen wie im `mysql`-Client: nur am Zeilenanfang und nur, solange das aktuelle Statement noch keinen Code enthält; die Zeile selbst wird nie an den Server gesendet; Argument unquoted bis zum ersten Leerzeichen oder in `'…'`, `"…"`, `` `…` ``. Strings `'…'` und `"…"` mit Backslash-Escapes und verdoppelten Quotes, `X'…'`, `B'…'`, `N'…'`; Bezeichner `` `…` ``. Kommentare `#`, `-- ` (mit Leerzeichen danach), `/* */`. Ausführbare Kommentare `/*! … */`, `/*!80000 … */`, `/*M! … */` werden durchgereicht und nie zerlegt. |
| PostgreSQL | Trennzeichen `;`, aber nicht innerhalb von Klammern (z. B. `CREATE RULE … DO (…; …)`) und nicht im Körper von `BEGIN ATOMIC … END`. Dollar-Quoting `$tag$ … $tag$` und `$$ … $$`, `E'…'` mit Backslash-Escapes, `'…'` ohne Backslash-Escapes, `B'…'`, `X'…'`, `N'…'`, `U&'…'`; Bezeichner `"…"`; Kommentare `--` und verschachtelte `/* */`. psql-Metabefehle (`\set` usw.) werden nicht unterstützt. |
| SQL Server | Batches werden durch eine Zeile `GO` (Groß-/Kleinschreibung egal) mit optionalem Wiederholungszähler getrennt: `GO 5` führt den Batch fünfmal aus. `;` trennt keine Batches. `GO 0` ist ein Fehler. Strings `'…'`/`N'…'`, Bezeichner `[…]` und `"…"`, Kommentare `--` und verschachtelte `/* */`. |

Kommentare auf derselben Zeile wie das vorige Trennzeichen (`SELECT 1; -- Notiz`) gehören zum vorigen Statement, damit die Zeilennummer des nächsten auf echten Code zeigt.

**Statement unter dem Cursor auf SQL Server:** Innerhalb des GO-Batches unter dem Cursor begrenzen `;` und Leerzeilen die Statements – außerhalb von Strings, Kommentaren, Klammern und `BEGIN … END`/`CASE … END`. Ein Batch, der ein Modul definiert (`CREATE`/`ALTER PROCEDURE`, `FUNCTION`, `TRIGGER`, `VIEW`, `SCHEMA`, `DEFAULT`, `RULE`), wird immer ganz ausgeführt. Ein Statement, das mit `ELSE` beginnt, wird mit dem vorigen verbunden.

### Abbrechen

`F8` bricht das laufende Statement ab: MySQL/MariaDB senden `KILL QUERY <id>` über die Hilfsverbindung, PostgreSQL und SQL Server brechen über den Treiber ab (Cancel-Request bzw. TDS-Attention). Die Hauptsitzung und ihre Transaktion bleiben in beiden Fällen nutzbar. `F8` unterbricht außerdem den Lua-Code eines laufenden Befehls, auch eine Endlosschleife in einem Skript („<befehl>: interrupted“), siehe [Sandbox](#sandbox). Auf einem folgenden Ergebnis (`tail -f`) beendet `F8` außerdem das Folgen.

### Open-Cursor-Regel

Ein lazy geladenes Ergebnis hält die Hauptsitzung. Jedes neue Statement schließt diesen Cursor zuerst; sein Tab behält die bis dahin geladenen Zeilen (die aktuelle Seite) und wird als *partial* markiert. Blättern in so einem Tab führt das Statement erneut aus (siehe [Seiten](#seiten-und-nachladen)).

---

## 9. Ergebnisse

### Grundlagen

- Kopfzeile fest, Zellcursor, **niemals Zeilenumbruch**. Breite Ergebnisse scrollen horizontal; `Left`/`Right` bewegen spaltenweise.
- Spaltenbreite aus Kopf und den ersten 200 geladenen Zeilen, begrenzt durch `max_column_width` (Standard 40). Längere Werte werden mit `…` abgeschnitten; Zeilenumbrüche im Wert erscheinen als `↵`, Tabs als Leerzeichen.
- `NULL` wird abgedimmt als `NULL` dargestellt, Binärwerte als `<BLOB n bytes>`. Zahlen sind rechtsbündig.
- Navigation: Pfeiltasten, `PgUp`, `PgDn`, `Home`, `End`.

### Herkunft eines Ergebnisses

- **table-origin**: erzeugt von `ll`, `head`, `tail` oder durch Öffnen einer Tabelle oder View aus dem Baum. Das Grid kennt Objekt und Metadaten. Sortieren und Filtern laufen serverseitig, freies `WHERE` und Zeilenbearbeitung sind möglich.
- **free**: alles andere (eigene Abfragen). Sortieren und Filtern wirken lokal auf die geladenen Zeilen der aktuellen Seite.

### Seiten und Nachladen

- Ein Ergebnistab hält höchstens **eine Seite** mit `page_size` Zeilen (Standard 3.000, mindestens 10). Innerhalb der Seite wird in Blöcken von `fetch_size` Zeilen (Standard 500) nachgeladen, wenn der Cursor sich dem Ende der geladenen Zeilen nähert.
- Ist die Seite voll, bleibt der Cursor offen. **`n` oder `F6`** zeigt die nächste Seite: die Zeilen der aktuellen Seite werden verworfen (Speicher freigegeben) und die nächste Seite aus dem offenen Cursor gelesen. Auf der letzten Seite meldet die Statuszeile `last page`.
- **`p` oder `Shift+F6`** zeigt die vorige Seite. Ein Cursor kann nicht rückwärts lesen: Das Statement wird mit denselben Argumenten erneut ausgeführt, die Zeilen davor werden gelesen und verworfen (Hinweis `skipping 6,000 rows…`, `F8` bricht ab), dann wird die Seite geladen. Das geht nur bei Statements, die der Dialekt als Lese-Abfrage einstuft, sonst: `previous pages are not available: the statement is not a read query`. Auf Seite 1 meldet die Statuszeile `first page`.
- Dasselbe erneute Ausführen passiert beim Vorwärtsblättern, nachdem der Cursor durch die Open-Cursor-Regel geschlossen wurde (bei Nicht-Lese-Statements: `further pages are not available: …`).
- Statuszeile: `42 rows` für ein vollständiges Ergebnis auf einer Seite; `1,204+ rows`, solange weiter nachgeladen werden kann; bei Seiten `rows 3,001–6,000 · page 2 +` (`+`: es kann eine nächste Seite geben), mit `· partial` nach Schließen des Cursors; gepufferte Ergebnisse an der Grenze: `10,000 rows capped`.
- Die Headless-Ausgabe kennt keine Seiten und gibt jede Zeile aus.

### Sortieren

| Taste | Wirkung |
|---|---|
| `s` | Aktuelle Spalte zyklisch: aufsteigend → absteigend → aus (ersetzt andere Sortierschlüssel) |
| `S` | Aktuelle Spalte als weiteren Sortierschlüssel hinzufügen (oder zyklisch ändern) |

Der Spaltenkopf zeigt Richtung und Rang: `name ▲1`, `city ▼2`.

- **table-origin:** Das Grid führt `SELECT * FROM obj [WHERE …] ORDER BY …` aus und beginnt wieder bei Seite 1.
- **free:** Die geladenen Zeilen der aktuellen Seite werden lokal sortiert, typbewusst, NULL zuerst. Hinweis in der Statuszeile: `sorted within N loaded rows`. Eine Sortierung über das ganze Ergebnis braucht `ORDER BY` im Statement.

### Filtern

Drei Arten, per AND kombinierbar, jede in der Statuszeile sichtbar (`filter: search "foo" AND city starts with "B" AND (id > 3)`) und einzeln entfernbar:

| Taste | Filter |
|---|---|
| `/` | **Schnellsuche**: ein Text über alle Spalten („enthält“, ohne Groß-/Kleinschreibung). Leere Eingabe entfernt die Suche. |
| `f` | **Spaltenfilter** auf der aktuellen Spalte. Operatoren (per Ziffer direkt wählbar): `1` contains, `2` starts with, `3` `=`, `4` `<>`, `5` `<`, `6` `<=`, `7` `>`, `8` `>=`, `9` is NULL, `0` is not NULL. Für Operatoren mit Wert folgt ein Eingabefeld; bei `=`, `<>`, `<`, `<=`, `>`, `>=` ist es mit dem Textwert der aktuellen Zelle vorbelegt. |
| `w` | **Freies `WHERE`**: ein selbst geschriebener Ausdruck. Nur bei table-origin-Ergebnissen. Leere Eingabe entfernt ihn. `Ctrl+Space` (bzw. die alternative Vervollständigungstaste) öffnet eine Liste mit den Spalten der Tabelle, Schlüsselwörtern und Funktionen; `↑`/`↓` wählen, `Enter`/`Tab` übernehmen, `Esc` schließt nur die Liste. Die Liste öffnet nie von selbst. |
| `x` | Filter und Sortierung entfernen: Ist nur eines aktiv, wird es entfernt; sonst erscheint ein Menü (`1`–`9` für den jeweiligen Eintrag, `a` für alle). |

Ausführung:

- **table-origin:** Alles wird zu einem serverseitigen `WHERE`, das Ergebnis beginnt bei Seite 1. Schnellsuche und Spaltenfilter verwenden gebundene Parameter (contains über das Textsuch-Prädikat des Dialekts, starts with als `LIKE … ESCAPE '!'`, Vergleiche mit Parameter). Der freie Ausdruck wird wörtlich in Klammern eingesetzt; lehnt der Server ihn ab, bleibt das vorige Ergebnis stehen und der Fehler geht ins Messages-Log. Eine Schnellsuche auf großen Tabellen ist ein Full Scan; `F8` bricht ab.
- **free:** Schnellsuche und Spaltenfilter wirken lokal auf die geladenen Zeilen der Seite, Hinweis `filtered within N loaded rows (M match)`. `contains`/`starts with` ignorieren die Groß-/Kleinschreibung; Vergleiche sind typbewusst (numerisch bei Zahlenspalten); `NULL` passt nur zu `is NULL`. Ein freies `WHERE` wird abgelehnt: `free WHERE is only available for table-origin results`.

Solange eine Neuabfrage läuft, werden weitere Sortier- oder Filteränderungen mit `a re-query is still running` abgewiesen.

### Pivot-Ansicht

`F3` schaltet zwischen normaler und transponierter Ansicht um. Transponiert gibt es eine Zeile je Attribut und eine Spalte je Datensatz. Die Attributnamen bleiben links fest stehen; horizontales Scrollen bewegt sich durch die Datensätze der Seite (mit Nachladen), `n`/`F6`/`p` blättern wie gewohnt. Der Spaltenkopf zeigt bei table-origin-Ergebnissen den Primärschlüsselwert, sonst die Zeilennummer im Gesamtergebnis. Datensatzspalten haben die gemeinsame Breite `pivot_column_width` (Standard 30). Der Cursor zeigt nach dem Umschalten auf denselben Datensatz und dasselbe Attribut. Sortieren, Filtern, Detail, Kopieren, Bearbeiten und neue Zeile wirken in beiden Ansichten und beziehen sich immer auf Datensätze.

### Zellendetail und Kopieren

- `Enter` öffnet das **Zellendetail**: der vollständige Wert, umgebrochen und scrollbar. JSON-Objekte und -Arrays werden formatiert, Binärwerte als Größe plus Hex-Dump (höchstens 64 KiB). Im Detailfenster kopiert `c` den Wert, `Esc` oder `Enter` schließt.
- `c` kopiert die aktuelle Zelle (vollständiger Wert wie im Detail), `C` die aktuelle Zeile (Werte durch Tabulator getrennt). Kopiert wird per **OSC 52** – das funktioniert auch über SSH, sofern das Terminal (und ggf. tmux) OSC 52 zulässt.

### Weitere Grid-Aktionen

| Taste | Wirkung |
|---|---|
| `F5` | Abfrage erneut ausführen (table-origin mit aktivem Sortieren/Filter: mit diesem Zustand; sonst das ursprüngliche Statement mit seinen Argumenten) |
| `F4` | Datensatz unter dem Cursor bearbeiten (Zeilenformular; nur table-origin) |
| `F7` | Neue Zeile (Zeilenformular; nur table-origin) |
| `[` / `]` | Vorheriger / nächster Ergebnistab |
| `Alt+X` | Ergebnistab schließen und Speicher freigeben. Der Messages-Tab bleibt immer. |
| `Alt+Q` | Alle Ergebnistabs schließen (Messages bleibt). |

### Folgende Ergebnisse (`tail -f`)

Ein mit `tail -f` geöffnetes Ergebnis hängt neue Zeilen automatisch unten an. Es behält höchstens `page_size` Zeilen; ältere fallen vorne heraus. Die Statuszeile zeigt z. B. `rows 1,201–4,200 · following · 12 new rows · every 2 s`, nach dem Ende `following stopped`. Steht der Cursor auf der letzten Zeile, wandert er mit der neuen letzten Zeile mit. Blättern ist nicht möglich (`n`/`p`/`F6` melden das). Das Folgen endet, wenn der Tab geschlossen wird, mit `F8` bei diesem Tab, wenn Sortieren oder Filtern das Ergebnis neu abfragt, wenn die Verbindung geschlossen oder dbcc beendet wird; ein Fehler (z. B. gelöschte Tabelle) beendet es ebenfalls und landet im Messages-Log.

### Export eines Ergebnisses

`export <datei.csv|datei.json>` schreibt das **ganze** aktive Ergebnis, nicht nur die Seite im Speicher. Details unter [`export`](#export).

---

## 10. Zeilenformular

Ein Formular zum Einfügen und Bearbeiten von Zeilen einer Tabelle.

Öffnen:

- **Neue Zeile:** `F7` im Baum auf einer Tabelle, oder `F7` in einem table-origin-Ergebnis.
- **Bearbeiten:** `F4` in einem table-origin-Ergebnis für den Datensatz unter dem Cursor.

In schreibgeschützten Profilen sind Formulare gesperrt („read-only profile: forms are disabled“). Freie Ergebnisse haben kein Formular („forms need a table-origin result (ll, head, or the tree)“).

**Bearbeiten setzt voraus**, dass der Datensatz eindeutig identifiziert werden kann: Die Tabelle braucht einen Primärschlüssel oder einen eindeutigen Schlüssel, dessen Spalten alle im Ergebnis vorhanden und in dieser Zeile nicht NULL sind. Views können nicht bearbeitet werden. Sonst erklärt ein Hinweis, warum.

### Felder

Ein Feld je Spalte, beschriftet mit Name und deklariertem Typ, Markierungen `PK` (Primärschlüssel) bzw. `UK` (eindeutiger Schlüssel), `NN` (NOT NULL), `FK` (Fremdschlüssel), `DF` (hat Default), dazu eine Beschreibung des Defaults.

| Spaltenart | Feld |
|---|---|
| Auto-Increment, Identity, generiert | schreibgeschützt, als `auto` angezeigt |
| `ENUM` | Auswahlliste |
| `SET` | Kontrollkästchen je Element |
| Boolean (`bool`, `boolean`, `tinyint(1)`, `bit(1)`) | Kontrollkästchen |
| Fremdschlüssel | schreibgeschützte Anzeige von Schlüssel plus erster Textspalte der referenzierten Zeile; `F4`/`Enter` öffnet das Suchfenster |
| `TEXT`, `JSON` und ähnliche (`tinytext`…`longtext`, `ntext`, `jsonb`, `xml`, `citext`, …) | einzeilige Vorschau; `F4`/`Enter` öffnet einen Mehrzeilen-Editor |
| `BLOB`, binäre, räumliche Typen, `bit(n>1)` | nicht bearbeitbar |
| alles andere | Texteingabe |

Feldzustände: **untouched** (unberührt), **value** (ein Wert, auch der leere String) und **NULL**.

### Tasten im Formular

| Taste | Wirkung |
|---|---|
| `F2` | Speichern (und schließen) |
| `F6` | Speichern und neues leeres Formular für dieselbe Tabelle |
| `F4` | Suchfenster (Fremdschlüssel), Mehrzeilen-Editor (Text/JSON und Textfelder) oder Auswahlliste (ENUM) |
| `Ctrl+N` | Feld auf NULL setzen (bei zusammengesetztem Fremdschlüssel alle Spalten) |
| `Ctrl+U` | Feld auf untouched zurücksetzen |
| `Esc` | Abbrechen; bei Änderungen Rückfrage „Discard changes? (y/n)“ |
| `Up`/`Down`, `Tab`/`Shift+Tab` | Vorheriges / nächstes Feld |
| `PgUp`/`PgDn` | 10 Felder zurück / vor |
| `Enter` | Textfeld: nächstes Feld; ENUM: Liste öffnen; FK/Text: Suchfenster bzw. Editor |
| `Space`/`Enter` | Boolean umschalten; SET: Element umschalten |
| `Left`/`Right` | SET: Element wählen; ENUM: Wert durchschalten |

Im Mehrzeilen-Editor übernehmen `F2` oder `F4` den Text, `Esc` verwirft ihn. In der ENUM-Liste wählt `Enter`, `Esc` schließt.

### Erzeugte Statements

- **Insert:** Nur Felder, die nicht untouched sind, erscheinen in der Spaltenliste – für untouched Felder greift also der Default der Datenbank. Sind alle Felder untouched: `INSERT INTO t () VALUES ()` (MySQL/MariaDB) bzw. `INSERT INTO t DEFAULT VALUES`. Durchgehend gebundene Parameter.
- **Update:** Nur geänderte Felder im `SET`, `WHERE` auf die ursprünglichen Schlüsselwerte. Ohne Änderung: „No changes“. Betrifft das Update nicht genau eine Zeile, erscheint eine deutliche Warnung (`UPDATE affected 0 rows, expected 1 …`).
- Jedes Statement samt Parametern geht ins Messages-Log. In Profilen mit manuellen Transaktionen wird es Teil der offenen Transaktion. Es gelten Lese-Schutz und `confirm_writes` wie für jedes Statement. Nach erfolgreichem Speichern wird das Grid neu abgefragt.

### Validierung und Fehler

**Die Datenbank validiert**, das Formular prüft nichts vorab. Bei einem Serverfehler bleibt das Formular offen; dbcc ordnet den Fehler der Spalte bzw. dem Constraint zu, markiert das Feld (bzw. alle Spalten des Constraints), setzt den Cursor darauf und zeigt die Servermeldung. Abgedeckt sind u. a. NOT NULL verletzt, kein Default, Daten zu lang, Wertebereich überschritten, falscher Wert für die Spalte, doppelter Schlüssel, Fremdschlüssel verletzt, Check-Constraint verletzt (MySQL- und MariaDB-Fehlernummern unterschieden), Wert für generierte Spalte.

### Fremdschlüssel-Suchfenster

Ein Grid der referenzierten Tabelle mit allen Spalten und einer Filterzeile: Tippen sucht serverseitig (Schnellsuche über alle Textspalten), höchstens 1.000 Zeilen, lazy geladen. `Up`/`Down`/`PgUp`/`PgDn`/`Left`/`Right` bewegen, `Enter` übernimmt die Zeile, `Esc` schließt. Ein zusammengesetzter Fremdschlüssel füllt alle seine Felder auf einmal. Danach zeigt das Feld den Schlüssel plus die erste Textspalte der referenzierten Zeile.

---

## 11. Kommandozeile und Befehle

### Die Kommandozeile

`F9` fokussiert die Kommandozeile. Das erste Wort wird in der Befehlsliste nachgeschlagen (exakt, mit Groß-/Kleinschreibung). Ist es kein Befehl, wird die ganze Zeile als SQL ausgeführt. So bleibt z. B. `KILL 12` (großgeschrieben) SQL, während `kill 12` den Befehl aufruft; T-SQL-Anweisungen `DBCC …` gehen immer an den Server (ein Befehl namens `dbcc` ist verboten).

| Taste | Wirkung |
|---|---|
| `Enter` | Befehl oder SQL ausführen |
| `Esc` | Zeile leeren und zum Editor wechseln |
| `Up` / `Down` | Vorheriger / nächster Eintrag der Befehlshistorie (als Ring inklusive der leeren Zeile). Die Historie bleibt über Neustarts erhalten: `~/.local/state/dbcc/cmdline_history` (letzte 1000 Zeilen; Zeilen mit `IDENTIFIED BY` oder `PASSWORD` werden nicht auf die Platte geschrieben). |
| `Ctrl+Space` (bzw. `Ctrl+O`) | Vervollständigungsliste; solange sie offen ist, hat sie den Fokus: `Up`/`Down`/`PgUp`/`PgDn` wählen, `Enter`/`Tab` übernehmen, `Esc` schließt nur die Liste, Tippen filtert |

Argumente werden wie in einer Shell zerlegt: `'…'` ist wörtlich, in `"…"` sind `\"` und `\\` Escapes, ein Backslash außerhalb von Anführungszeichen maskiert das nächste Zeichen. Ein abschließendes `;` nach einem Befehl wird toleriert (`pwd;`).

Flags stehen vor den Positionsargumenten: `-a`, `-t wert`, `-t=wert` (auch mit `--`); `--` beendet die Flags; `-` allein und negative Zahlen sind Positionsargumente. Falsche Aufrufe melden die Usage des Befehls.

Befehle, die schreiben (`cp`, `mv`, `kill`), zeigen das SQL vor der Ausführung, fragen nach und sind in schreibgeschützten Profilen gesperrt.

### Befehlsübersicht

| Befehl | Kurzbeschreibung | Schreibt |
|---|---|---|
| [`cd [path]`](#cd) | Pfad wechseln | nein |
| [`ls [path]`](#ls) | Inhalt einer Pfadebene | nein |
| [`pwd`](#pwd) | Aktuellen Pfad zeigen | nein |
| [`ll <table\|view>`](#ll) | Alle Zeilen im Grid | nein |
| [`head <table\|view> [n]`](#head) | Erste n Zeilen | nein |
| [`tail [-f] [-i seconds] [-k column] <table\|view> [n]`](#tail) | Letzte n Zeilen, optional folgen | nein |
| [`wc <table\|view>`](#wc) | Zeilen zählen | nein |
| [`du [table]`](#du) | Daten- und Indexgröße | nein |
| [`cat [-t kind] <object>`](#cat) | Struktur bzw. Quelltext | nein |
| [`edit [-t kind] <object>`](#edit) | Quelltext als Replace-Skript im Editor | nein |
| [`cp [-a] <src> <new>`](#cp) | Tabelle kopieren | ja |
| [`mv <table\|view> <new-name>`](#mv) | Umbenennen | ja |
| [`grep <pattern> [path]`](#grep) | Namen und Quelltexte durchsuchen | nein |
| [`ps`](#ps) | Laufende Sitzungen | nein |
| [`kill [-q] <id>`](#kill) | Sitzung beenden | ja |
| [`export [-f] <file.csv\|file.json>`](#export) | Aktives Ergebnis in Datei | nein |
| [`history [n]`](#history) | Statement-Historie | nein |
| [`reload`](#reload) | Befehle neu laden (eigene Skripte zur Laufzeit installieren) | nein |
| [`help [command\|about]`](#help) | Hilfe, About-Seite | nein |

Alle Befehle akzeptieren Objekte als Name, qualifizierten Namen oder Pfad (siehe [Abschnitt 5](#5-pfade-und-navigation)). Befehle, die Daten zeigen (`ll`, `head`, `tail`, `wc`), akzeptieren Tabellen, Views und Materialized views.

### cd

```text
cd [path]
```

Wechselt den aktuellen Pfad.

| Aufruf | Wirkung |
|---|---|
| `cd shop` | relativ: eine Ebene tiefer |
| `cd ..` | eine Ebene höher |
| `cd /prod/shop`, `cd /pg/app/public` | absolut |
| `cd ../crm` | Kombination |
| `cd -` | vorheriger Pfad |
| `cd /` | Wurzel (Verbindungen bleiben offen) |
| `cd` | Standard des aktuellen Profils: `/<profil>[/<datenbank>]` |

Großzügigkeit: Auf der Datenbankebene von MySQL/MariaDB bedeutet `cd andere` die Nachbardatenbank (`../andere`). Auf der Schemaebene (PostgreSQL, SQL Server) bedeutet `cd andere` zuerst ein Nachbarschema, sonst eine Nachbardatenbank.

Ein Datenbankwechsel verwendet `USE` (MySQL/MariaDB, SQL Server) bzw. verbindet auf PostgreSQL transparent neu. Ist eine manuelle Transaktion offen, fragt dbcc vorher: Commit, Roll back oder Stay. Beim Betreten einer Datenbank auf PostgreSQL/SQL Server landet man auf deren aktuellem Schema. Auf PostgreSQL setzt der Schemawechsel `search_path` auf `schema, public`; auf SQL Server wird das Schema nur geprüft (siehe [Abschnitt 18](#18-datenbankspezifisches)).

### ls

```text
ls [path]
```

Listet die Ebene des Pfads (Standard: aktueller Pfad) mit den Spalten `name`, `kind`, `comment`: an der Wurzel die Profile (Treiber, Host:Port oder Socket, Datenbank, `(current)`/`(open)`), unter einer Verbindung die Datenbanken, unter einer Datenbank die Objekte bzw. bei Schema-Dialekten die Schemata, unter einem Schema die Objekte. Systemobjekte sind mit `(system)` markiert.

```text
ls
ls /prod
ls /pg/app/public
```

### pwd

```text
pwd
```

Zeigt den aktuellen Pfad.

### ll

```text
ll <table|view>
```

`SELECT * FROM <objekt>` im Grid, als table-origin-Ergebnis (serverseitiges Sortieren/Filtern, Formular).

### head

```text
head <table|view> [n]
```

Die ersten `n` Zeilen (Standard 10) als table-origin-Ergebnis. Das Limit ist dialektgerecht (`LIMIT n` bzw. `TOP (n)`).

```text
head customers 50
head /prod/crm/contacts
```

### tail

```text
tail [-f] [-i seconds] [-k column] <table|view> [n]
```

Die letzten `n` Zeilen (Standard 10) nach dem Primärschlüssel (alle seine Spalten) oder nach `-k spalte`, **aufsteigend** angezeigt, als table-origin-Ergebnis. Erzeugt wird `SELECT * FROM (<letzte n nach Schlüssel absteigend>) AS dbcc_tail ORDER BY <schlüssel>`. Eine View oder eine Tabelle ohne Primärschlüssel braucht `-k`.

| Flag | Bedeutung |
|---|---|
| `-k column` | Sortierschlüssel statt Primärschlüssel |
| `-f` | Folgen: Zeilen mit einem Schlüssel größer als der zuletzt gesehene werden angehängt, sobald sie eingefügt werden |
| `-i seconds` | Abfrageintervall für `-f` (Standard 2, mindestens 0.5; nur mit `-f`) |

`tail -f` folgt **nur Einfügungen, in Schlüsselreihenfolge**: Geänderte oder gelöschte Zeilen und Zeilen, die später mit einem kleineren Schlüssel committet werden, erscheinen nicht. Es braucht einen einspaltigen Schlüssel (bei zusammengesetztem Primärschlüssel `-k` verwenden) und `n` von mindestens 1. Die Abfragen laufen im Hintergrund auf der **Hilfsverbindung**, nie auf der Hauptsitzung – die Oberfläche bleibt benutzbar. Verhalten im Grid: siehe [Folgende Ergebnisse](#folgende-ergebnisse-tail--f); headless: siehe [Abschnitt 14](#14-headless-betrieb-und-skripte).

```text
tail orders
tail -k created_at audit_log 100
tail -f -i 0.5 events
```

### wc

```text
wc <table|view>
```

`SELECT COUNT(*)`; Ergebnis mit den Spalten `rows` und `object`.

### du

```text
du [table]
```

Daten- und Indexgröße und geschätzte Zeilenzahl einer Tabelle, oder ohne Argument aller Tabellen der aktuellen Datenbank (bzw. des aktuellen Schemas, auf PostgreSQL/SQL Server ohne Schema: aller Benutzerschemata, Namen dann mit Schema qualifiziert), größte zuerst. Spalten: `table`, `engine`, `rows (estimate)`, `data`, `index`, `total`, `free`, `total bytes`; bei mehreren Tabellen eine Summenzeile `(total N tables)`.

| Dialekt | Quelle |
|---|---|
| MySQL / MariaDB | `information_schema.TABLES` (`DATA_LENGTH`, `INDEX_LENGTH`, `DATA_FREE`, `TABLE_ROWS`); auf MySQL wird vorher `information_schema_stats_expiry = 0` gesetzt, damit die Werte aktuell sind |
| PostgreSQL | `pg_table_size`, `pg_indexes_size`, `reltuples`; Engine = Zugriffsmethode. Liest nur den Katalog der aktuellen Datenbank: für eine andere Datenbank erst `cd` dorthin |
| SQL Server | `sys.partitions` und `sys.allocation_units` (8-KiB-Seiten); Engine `clustered` oder `heap`; jede Datenbank über dreiteilige Namen |

### cat

```text
cat [-t kind] <object>
```

Für **Tabellen** (und Materialized views) eine strukturierte Beschreibung im Stil von MySQL `DESCRIBE`, erweitert um alle Definitionen und Constraints – kein DDL. Abschnitte:

1. **Columns**: `#`, Field, Type, Null, Key (`PRI`, `UNI`, `MUL`), Default, Extra (auto_increment bzw. Identity-Definition, `GENERATED ALWAYS AS (…) STORED|VIRTUAL`, `on update …`, abweichende Collation), Comment.
2. **Primary and unique keys** mit Name und Spalten.
3. **Foreign keys**: Name, Spalten, referenzierte Tabelle und Spalten, On update, On delete.
4. **Check constraints**: Name, Ausdruck, Enforced.
5. **Indexes**: Name, Spalten (mit `DESC`, Präfixlänge, Ausdrücken), ggf. Include und Filter (PostgreSQL, SQL Server), Unique, Type, Visible, Comment.
6. **Triggers**: Name, Timing, Event.
7. **Table options**: engine, charset, collation, rows (estimate), auto_increment, row_format, create_options, partitioning und weitere, comment.

Bei Materialized views entfallen Schlüssel, Fremdschlüssel, Checks und Trigger; am Ende steht die Definition. Für **Views, Prozeduren, Funktionen, Trigger und Events** zeigt `cat` den Quelltext mit Syntaxhervorhebung (schreibgeschützt). Die Ausgabe erscheint in einem horizontal scrollbaren Tab.

`-t kind` wählt die Objektart, wenn Namen mehrdeutig sind: `table`, `view`, `procedure` (`proc`), `function` (`func`, `fn`), `trigger`, `event`, `materialized view` (`mview`). Ohne `-t` meldet dbcc im Messages-Log, wenn gleichnamige Objekte anderer Art existieren („… is also a procedure; use: cat -t procedure …“).

Der Aufbau der Ausgabe stammt aus `cat.lua` und lässt sich ohne Neukompilieren ändern.

### edit

```text
edit [-t kind] <object>
```

Öffnet eine View, Prozedur, Funktion, einen Trigger, ein Event oder eine Materialized view in einem Editor-Tab als Skript, das das Objekt ersetzt (*Run all* speichert es). Siehe [Objekt-Tabs](#objekt-tabs-und-replace-skripte). `-t` wie bei `cat`. Ohne UI wird das Skript ausgegeben (`dbcc -p prod -c "edit calc_total"`).

### cp

```text
cp [-a] <src> <new>
```

Kopiert eine Tabelle auf derselben Verbindung. Der neue Name kann `name`, `db.name` (MySQL/MariaDB), `schema.name`, `db.schema.name` oder ein Pfad sein. dbcc zeigt alle Statements vorab und fragt nach; die gemeldete Zeilenzahl ist die der kopierenden Anweisung.

| Variante | MySQL / MariaDB | PostgreSQL | SQL Server |
|---|---|---|---|
| `cp src new` (nur Daten) | `CREATE TABLE new AS SELECT * FROM src` | ebenso | `SELECT * INTO new FROM src` |
| `cp -a src new` (Struktur + Daten) | `CREATE TABLE new LIKE src`, dann `INSERT … SELECT` | `CREATE TABLE new (LIKE src INCLUDING ALL)`, eigene Sequenzen für serial-Spalten, `INSERT … [OVERRIDING SYSTEM VALUE] SELECT`, `setval` | `CREATE TABLE` aus den Metadaten generiert, `SET IDENTITY_INSERT … ON`, `INSERT … SELECT`, `… OFF`, dann die übrigen Indizes |

`cp -a` übernimmt Spalten, Defaults, Schlüssel, Indizes und Checks, aber **keine Fremdschlüssel und keine Trigger**. Generierte Spalten werden aus der `INSERT`-Spaltenliste ausgelassen. Eine View als Quelle: `cp view new` erzeugt eine Snapshot-Tabelle, `cp -a view new` eine zweite View mit derselben Definition.

Weitere Grenzen von `cp -a`: PostgreSQL übernimmt keine Partitionierung (eine partitionierte Quelle wird eine normale Tabelle). SQL Server übernimmt keine Constraint-Namen von Defaults, keine XML-, räumlichen und Hash-Indizes, keine Filegroups/Partitionierung, keine Kompression, keine Systemversionierung und keine Extended Properties; deaktivierte Checks werden `WITH NOCHECK` angelegt und deaktiviert.

### mv

```text
mv <table|view> <new-name>
```

Benennt eine Tabelle oder View innerhalb ihrer Datenbank bzw. ihres Schemas um; der neue Name muss ein einfacher Name ohne `.` und `/` sein. MySQL/MariaDB: `RENAME TABLE`; PostgreSQL: `ALTER TABLE|VIEW|MATERIALIZED VIEW … RENAME TO`; SQL Server: `[db].sys.sp_rename`.

### grep

```text
grep <pattern> [path]
```

Durchsucht Objektnamen, Spaltennamen und die Quelltexte von Views, Routinen, Triggern und Events in der aktuellen Datenbank bzw. im aktuellen Schema (oder in dem des Pfads). Ohne Beachtung der Groß-/Kleinschreibung, wörtlich (keine Platzhalter). Ergebnis-Spalten: `object`, `kind`, `match` (`name`, `column`, `source`), `detail` (Spalte mit Typ bzw. `line N: …` der Fundstelle).

Auf PostgreSQL wird nur der Katalog der aktuellen Datenbank gelesen (sonst erst `cd`); ohne Schema werden alle Benutzerschemata durchsucht und die Namen qualifiziert. SQL Server durchsucht `sys.objects`, `sys.columns`, `sys.sql_modules` jeder Datenbank.

### ps

```text
ps
```

Laufende Sitzungen und ihre Statements; `*` markiert die eigene Sitzung. Spalten: `me`, `id`, `user`, `host`, `db`, `command`, `time`, `state`, `info`.

| Dialekt | Quelle |
|---|---|
| MySQL | `performance_schema.processlist`, sonst `information_schema.PROCESSLIST` |
| MariaDB | `information_schema.PROCESSLIST` |
| PostgreSQL | `pg_stat_activity` (nur Client-Backends, mit Wait-Events) |
| SQL Server | `sys.dm_exec_sessions` mit laufendem Request und SQL-Text (nur Benutzersitzungen) |

### kill

```text
kill [-q] <id>
```

Beendet die Sitzung mit der Id (siehe `ps`); `-q` bricht nur ihr laufendes Statement ab. Die eigene Sitzung wird abgelehnt. MySQL/MariaDB: `KILL` bzw. `KILL QUERY`; PostgreSQL: `pg_terminate_backend` bzw. `pg_cancel_backend` (prüft, ob die Sitzung existiert); SQL Server: `KILL` – `-q` ist dort nicht möglich.

### export

```text
export [-f] <file.csv|file.json>
```

Schreibt das aktive Ergebnis (den aktuellen Ergebnistab) in eine Datei; das Format folgt der Endung. `~` am Anfang wird zum Home-Verzeichnis. Eine vorhandene Datei wird nur mit `-f` überschrieben. Bei einem Fehler wird die halb geschriebene Datei entfernt.

- Geschrieben wird das **ganze** Ergebnis, nicht nur die Seite im Speicher: Auf der ersten Seite mit noch offenem Cursor werden die geladenen Zeilen geschrieben und der Cursor bis zum Ende fortgesetzt; sonst wird das Statement von vorn erneut ausgeführt (mit seinen Argumenten; nur Lese-Abfragen) und jede Zeile in die Datei gestreamt.
- Ein aktiver **lokaler Filter** wird beim Streamen angewendet. Eine aktive **lokale Sortierung** lädt vorher alle Zeilen in den Speicher (der Speicherbedarf wächst mit dem Ergebnis).
- Fortschrittsmeldungen im Messages-Log alle 10.000 Zeilen.
- CSV: Kopfzeile, NULL als leeres Feld. JSON: ein Array von Objekten, Werte als Strings, NULL als `null`.

### history

```text
history [n]
```

Die letzten `n` ausgeführten Statements (Standard 100). In der UI erscheint eine filterbare Liste (neueste zuerst, mit Zeit, Pfad und `[error]`-Markierung); `Enter` lädt das Statement in den aktuellen Scratch-Tab, `Esc` schließt. Ohne UI wird die Liste ausgegeben (älteste zuerst) mit `#`, `time`, `profile`, `path`, `ms`, `outcome`, `statement`.

### reload

```text
reload
```

Lädt alle Befehle neu – die eingebauten und die Benutzerskripte aus `~/.config/dbcc/commands` (bzw. `$XDG_CONFIG_HOME/dbcc/commands`) –, ohne dbcc neu zu starten; Verbindungen, Transaktionen, Tabs und Ergebnisse bleiben. So installiert man ein neues oder geändertes Skript zur Laufzeit: Datei speichern, `F9`, `reload`. Das Messages-Log meldet `commands reloaded: N commands (user scripts from <verzeichnis>)`; ein fehlerhaftes Skript wird als `user command script skipped: <pfad>: <fehler>` gemeldet und übersprungen (ein ersetzter eingebauter Befehl bleibt dann aktiv). Nur in der UI; headless lädt jeder Aufruf die Skripte ohnehin neu. Details: [Skripte zur Laufzeit installieren](#skripte-zur-laufzeit-installieren-reload).

### help

```text
help [command|about]
```

Ohne Argument: alle Befehle mit Usage und Beschreibung. `help <befehl>`: Usage, Beschreibung, Flags, Argumenttypen, ob der Befehl schreibt, und die Quelle (`embedded:<name>.lua` oder Pfad des Benutzerskripts). `help about` öffnet die About-Seite.

---

## 12. Vervollständigung

Die Vervollständigungsliste öffnet sich **nur** auf `Ctrl+Space` oder die Alternativtaste (`completion_key_alt`, Standard `Ctrl+O`) – nie automatisch, ohne Ghost-Text. Sie filtert beim Weitertippen, `Enter` oder `Tab` übernimmt, `Esc` schließt, `Up`/`Down`/`PgUp`/`PgDn` bewegen. Gibt es genau einen Kandidaten, wird er direkt eingefügt. Bezeichner werden nur gequotet, wenn nötig (keine einfachen Namen, reservierte Wörter; auf PostgreSQL auch Namen mit Großbuchstaben).

Die Kandidaten hängen von den Tokens vor dem Cursor ab (token-basierte Heuristik, kein Parser):

| Kontext | Kandidaten |
|---|---|
| Nach `FROM`, `JOIN`, `INTO`, `UPDATE`, `TABLE`, `DESCRIBE` und in kommagetrennten FROM-Listen | Tabellen und Views der aktuellen Datenbank bzw. des Schemas, dann Datenbanken (und Schemata) |
| Nach `db.` (bzw. `schema.`, `db.schema.`) an einer Tabellenposition | Objekte dieser Datenbank bzw. dieses Schemas |
| Nach `alias.` oder `tabelle.` | Spalten dieser Tabelle; Aliasse werden innerhalb des aktuellen Statements aufgelöst (auch nach dem Cursor stehende) |
| Nach `CALL`, `EXEC`, `EXECUTE` | Prozeduren |
| Sonst innerhalb eines Statements | Spalten der im Statement genannten Tabellen, dann Schlüsselwörter und Funktionen des Dialekts |
| In Strings und Kommentaren | nichts |
| Kommandozeile, erstes Wort | Befehle |
| Kommandozeile, Argumente | nach dem deklarierten Argumenttyp: Objekte, Pfade, Profile, Dateien – auch nach Optionen (`cat -t view ` → Objekte) |
| Kommandozeile, Optionen | nach `-` die Optionsnamen des Befehls (`tail -` → `-f`, `-i`, `-k`); nach einer Option mit Wert deren deklarierte Werte (`cat -t ` → `table`, `view`, …) bzw. Werte ihres Typs; `--` beendet die Optionen |
| Kommandozeile mit SQL | wie im Editor |

Metadaten kommen aus einem Cache, der beim ersten Gebrauch je Schema geladen und nach DDL-Statements sowie mit `F5` im Baum verworfen wird. Hinweis: DDL, das auf PostgreSQL oder SQL Server in einer offenen Transaktion der Hauptsitzung noch nicht committet ist, sehen Baum und Vervollständigung erst nach dem Commit (sie lesen über die Hilfsverbindung).

---

## 13. Transaktionen und Sicherheit

Pro Profil einstellbar:

| Einstellung | Wirkung |
|---|---|
| `autocommit = true` (Standard) | Jedes Statement committet. Statuszeile: `autocommit`. |
| `autocommit = false` | Manuelle Transaktionen. Das erste Statement öffnet eine Transaktion, `COMMIT` und `ROLLBACK` sind normales SQL. Statuszeile: `manual txn`, bei offener Transaktion fett `TXN OPEN`. |
| `read_only = true` | Die Sitzung wird serverseitig schreibgeschützt, soweit der Dialekt das kann (MySQL/MariaDB: `SET SESSION TRANSACTION READ ONLY`; PostgreSQL: `SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY`; SQL Server hat keine solche Einstellung). Zusätzlich weist ein clientseitiger Schutz Statements ab, die keine Lese-Statements sind („read-only profile: statement rejected“). Schreibende Befehle, Formulare und *Run all* in Objekt-Tabs sind gesperrt. Statuszeile dauerhaft `READ ONLY`. |
| `confirm_writes = true` | Jedes Statement, das kein Lese-Statement ist, fragt vorher („Run this statement?“). Auch das Ersetzen eines Objekts fragt. |

Der clientseitige Schutz ist eine Heuristik auf Token-Ebene (erstes Schlüsselwort; `SELECT … INTO`, `WITH … UPDATE`, `FOR UPDATE` und auf SQL Server jedes `EXEC`, `INSERT` usw. im Batch zählen als Schreiben). `SET`, `USE`, `BEGIN`, `COMMIT`, `ROLLBACK` gelten als lesend. **Der eigentliche Schutz ist die Servereinstellung bzw. die Rechte des Kontos.**

Manuelle Transaktionen je Dialekt:

- **MySQL/MariaDB:** `SET autocommit = 0`. DDL committet implizit; dbcc vermerkt das im Messages-Log („this statement committed the open transaction implicitly“) und setzt den Zustand zurück.
- **PostgreSQL:** kein Sitzungsschalter; dbcc sendet vor dem ersten Statement einer Transaktion automatisch `BEGIN` (Vermerk „BEGIN sent first (manual transaction)“). Ausgenommen sind Transaktionssteuerung, `SET`/`RESET`/`SHOW`/`DISCARD`/`LISTEN` u. ä. sowie Statements, die nicht in einem Transaktionsblock laufen dürfen (`VACUUM`, `CREATE/DROP DATABASE`, `… CONCURRENTLY`, `ALTER SYSTEM` …). `SET TRANSACTION`, `SET LOCAL`, `SET CONSTRAINTS` bekommen ein `BEGIN`. Der Zustand wird aus dem Protokoll gelesen, auch eine fehlgeschlagene Transaktion zählt als offen.
- **SQL Server:** `SET IMPLICIT_TRANSACTIONS ON`; der Zustand wird über `@@TRANCOUNT` verfolgt.

**Offene Transaktion:** Beim Beenden (`F10`), beim `cd` in eine andere Datenbank und beim Trennen (`F2`, `x`) fragt dbcc: **Commit**, **Roll back** oder **Stay** (`Esc` = Stay). Eine offene Transaktion überlebt das Navigieren innerhalb derselben Datenbank und das Wechseln zwischen Verbindungen.

**Statement-Historie:** Jedes ausgeführte Statement wird mit Zeitstempel, Profil, Pfad, Dauer und Ergebnis in `~/.local/state/dbcc/history.jsonl` angehängt (Datei 0600). Statements, die `IDENTIFIED BY` oder `PASSWORD` enthalten (Groß-/Kleinschreibung egal), werden **nicht** aufgezeichnet.

---

## 14. Headless-Betrieb und Skripte

Ohne Oberfläche läuft dbcc, wenn `-f` oder `-c` angegeben ist oder stdin kein Terminal ist. Ein Profil (`-p`) ist Pflicht; gestartet wird auf `/<profil>[/<datenbank>]`.

```sh
dbcc -p shop-dev -f migrate.sql                 # Skript
dbcc -p shop-dev -c "ll customers" --format csv # Lua-Befehl
dbcc -p shop-dev -c "SELECT now()"              # SQL
dbcc -p shop-dev < dump.sql                     # stdin
dbcc -p shop-dev -c "cp -a orders orders_bak" --yes
dbcc -p shop-dev -c "tail -f -i 1 events" --format json
```

- `-c`: Ist das erste Wort ein registrierter Befehl, läuft der Lua-Befehl; sonst ist der Text SQL (auch mehrere Statements).
- **Ausgabe:** Ergebnisse auf stdout im Format `--format`: `table` (ausgerichtet, eine Leerzeile zwischen mehreren Ergebnissen), `csv`, `tsv` (Zeilenumbrüche in Werten werden ersetzt), `json` (Array von Objekten). NULL ist in CSV/TSV ein leeres Feld, in JSON `null`. Es gibt keine Seiten; alle Zeilen werden gestreamt. Textausgaben (`cat`, `edit`, `help`, `pwd`) erscheinen unverändert, ohne Farbe.
- **Meldungen** auf stderr: Hinweise von Befehlen, Serverwarnungen (`warning (line N): …`), implizite Commits. Mit `-v` zusätzlich jedes Statement: `-- line N: <text>, 42 rows (12ms)`.
- **Fehler** auf stderr: `ERROR at line N: <fehler>` plus das eingerückte Statement, bzw. `ERROR: <fehler>`.
- **Rückfragen:** Schreibende Befehle scheitern ohne `--yes` mit „confirmation required; run with --yes“. In `confirm_writes`-Profilen werden schreibende Statements ohne `--yes` abgelehnt („cancelled by user“).
- `history` gibt die Liste aus, `edit` das Replace-Skript, `help about` den About-Text (mit Hinweis auf `dbcc --licenses`). Primitive, die eine Auswahl oder den Editor brauchen, scheitern mit „not available without UI“.
- Passwortabfragen laufen über `/dev/tty`.
- `Ctrl+C` (SIGINT) bricht das laufende Statement ab.
- **`tail -f` headless:** gibt die letzten n Zeilen aus und dann jede neue Zeile, sobald sie eintrifft – `table` ohne Wiederholung des Kopfes, `csv`/`tsv` als Datensätze, `json` als **JSON Lines** (ein Objekt je Zeile, kein umschließendes Array) – bis SIGINT oder SIGTERM; dann Exit-Code 0. Ein Fehler, der das Folgen beendet (z. B. gelöschte Tabelle), ergibt Exit-Code 1. Headless behält ein folgendes Ergebnis höchstens 3.000 Zeilen im Speicher.

**Exit-Codes:**

| Code | Bedeutung |
|---|---|
| `0` | Erfolg |
| `1` | SQL-Fehler oder Fehler eines Befehlsskripts |
| `2` | Aufruf-, Konfigurations- oder Verbindungsfehler (auch falsche Befehlsargumente) |

---

## 15. Konfiguration

Datei: `~/.config/dbcc/config.toml` (bzw. `$XDG_CONFIG_HOME/dbcc/config.toml`, oder `--config`). Fehlt sie, gelten die Standardwerte. Ist sie für Gruppe oder andere lesbar/schreibbar (weiter als 0600), warnt dbcc beim Start. dbcc schreibt neue Verbindungen selbst in diese Datei (Modus 0600) und lässt alles andere darin unverändert.

### Beispiel

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
driver         = "mysql"          # mysql | postgres | mssql; MariaDB wird erkannt
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
host     = "sql01\\INST1"         # host\instance: Port weglassen, SQL Browser löst auf
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

| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `fetch_size` | `500` | Zeilen je Nachladeblock |
| `max_rows` | `100000` | Aus Kompatibilitätsgründen vorhanden; das UI-Grid blättert stattdessen in Seiten (`page_size`), headless wird alles gestreamt |
| `page_size` | `3000` | Zeilen je Ergebnisseite im Grid; mindestens 10 (kleinere Werte sind ein Konfigurationsfehler). Auch Obergrenze eines folgenden Ergebnisses (`tail -f`) |
| `max_rows_buffered` | `10000` | Obergrenze gepufferter Ergebnisse: alle außer dem letzten eines Skripts, jede Menge von Mehrfach-Ergebnissen, `ctx:query` in Lua |
| `max_column_width` | `40` | Maximale Spaltenbreite im Grid |
| `pivot_column_width` | `30` | Breite der Datensatzspalten in der Pivot-Ansicht |
| `completion_key_alt` | `"ctrl+o"` | Zweite Taste für die Vervollständigung (Editor und Kommandozeile); `""` schaltet sie ab |
| `mouse` | `false` | Mausbedienung |
| `splash` | `true` | Startbildschirm für 3 Sekunden |

### `[profiles.<name>]`

| Schlüssel | Bedeutung |
|---|---|
| `driver` | `mysql` (auch MariaDB), `postgres` oder `mssql`. Pflicht. |
| `host` | Hostname/IP (Standard 127.0.0.1). SQL Server: auch `host\instanz`. |
| `port` | Standard 3306 / 5432 / 1433, wenn weder Port noch Socket angegeben ist. |
| `socket` | Unix-Socket (MySQL/MariaDB: Datei; PostgreSQL: Verzeichnis oder Datei `.s.PGSQL.<port>`). |
| `user` | Benutzer. |
| `database` | Standarddatenbank (PostgreSQL ohne Angabe: `postgres`). |
| `password` | Quelle des Passworts: `prompt` (Standard), `stored`, `env:NAME`, `mycnf`, `pgpass`. **Niemals das Passwort selbst.** |
| `autocommit` | Standard `true`. |
| `read_only` | Standard `false`. |
| `confirm_writes` | Standard `false`. |
| `params` | Untertabelle mit Treiberparametern (siehe unten). |

Treiberparameter (`[profiles.<name>.params]`):

| Dialekt | Verwendung |
|---|---|
| MySQL / MariaDB | `tls` wird zur TLS-Einstellung des Treibers (z. B. `true`, `false`, `skip-verify`, `preferred`); alle anderen Schlüssel werden als DSN-Parameter des go-sql-driver übergeben. Verbindungs-Timeout 10 s. |
| PostgreSQL | libpq-Schlüsselwörter, z. B. `sslmode`, `sslrootcert`, `connect_timeout` (Standard 10), `application_name` (Standard `dbcc`), `options`, `search_path`; unbekannte Schlüssel werden Laufzeitparameter. Wie bei libpq gelten `PG*`-Umgebungsvariablen für nicht gesetzte Werte. |
| SQL Server | DSN-Parameter von go-mssqldb, z. B. `encrypt = "disable"`, `TrustServerCertificate = "true"`, `ApplicationIntent = "ReadOnly"`, `"connection timeout" = "30"`; `app name` ist standardmäßig `dbcc`, `dial timeout` 10. |

### Passwortspeicher

`~/.config/dbcc/credentials.toml`, Modus 0600 (wird beim Lesen automatisch auf 0600 gesetzt, falls weiter), atomar geschrieben. Inhalt:

```toml
# dbcc credential store — keep private (mode 0600).

[profiles.shop-dev]
password = "…"
```

Profile verwenden ihn mit `password = "stored"`. Der Verbindungsmanager (F2) pflegt die Datei; das Löschen einer Verbindung entfernt auch ihr Passwort.

### Tasten umbelegen: `[keys.<bereich>]`

Jede Belegung lässt sich ändern: `aktion = "taste"`. Bereiche: `global`, `tree`, `editor`, `grid`, `form`, `cmdline`. Die Aktionsnamen stehen in der [Tastenreferenz](#16-tastenreferenz). Unbekannte Bereiche, Aktionen oder Tasten sind ein Startfehler mit Erklärung.

Tastensyntax (Groß-/Kleinschreibung der Präfixe egal):

- Präfixe `ctrl+`, `alt+`, `shift+`, kombinierbar.
- Funktionstasten `f1` … `f24`.
- Namen: `enter`, `tab`, `backtab`, `esc`/`escape`, `backspace`, `delete`, `insert`, `home`, `end`, `pgup`, `pgdn`, `up`, `down`, `left`, `right`, `space`.
- `ctrl+a` … `ctrl+z`, `ctrl+space`.
- Ein einzelnes Zeichen: `s`, `S` (Großbuchstabe = mit Shift), `/`, `[`, `alt+,`.
- **Nicht erlaubt:** `alt+` mit einem Großbuchstaben oder mit `@ [ \ ] ^ _`. Terminals senden Alt+Taste als `ESC` + Taste; `ESC` gefolgt von einem dieser Zeichen ist eine C1-Steuersequenz (z. B. `ESC P` = DCS), die nie als Taste ankommt. dbcc lehnt solche Belegungen mit einer Erklärung ab; stattdessen einen Kleinbuchstaben oder eine andere Taste verwenden.

Hinweise: Belegungen des fokussierten Bereichs haben Vorrang vor den globalen. Eine Aktion mit mehreren Standardtasten (im Grid `next_page` = `n` und `F6`, `prev_page` = `p` und `Shift+F6`) bekommt beim Umbelegen auf allen ihren Einträgen die neue Taste. In der Fußzeile erscheinen nur F1–F10 ohne Modifier. `Esc` gefolgt von einer Ziffer bzw. `Alt`+Ziffer wirkt immer als F1–F10.

---

## 16. Tastenreferenz

Die F1-Hilfe zeigt diese Tabellen aus der aktuellen Belegung (also inklusive eigener Änderungen), dazu alle Befehle; das Suchfeld filtert Tasten und Befehle. `F1` oder `Tab` in der Hilfe öffnet die About-Seite, `Esc` schließt, `Up`/`Down`/`PgUp`/`PgDn`/`Home`/`End` scrollen. Auf der About-Seite öffnet `L` die Lizenzen der Fremdbibliotheken (scrollen mit `Up`/`Down`/`PgUp`/`PgDn`/`Home`/`End`; `L` oder `Esc` zurück); `Esc` schließt die About-Seite.

**Esc+Ziffer und Alt+Ziffer wirken als F1–F10** (`0` = F10), Esc+Ziffer innerhalb einer Sekunde. Modale Fenster (Formular, Suchfenster, Dialoge) übersteuern die globalen Tasten, solange sie offen sind.

### Global (`[keys.global]`)

| Taste | Aktion | Wirkung |
|---|---|---|
| `F1` | `help` | Hilfe für den fokussierten Bereich |
| `F2` | `profiles` | Verbindungsmanager |
| `F8` | `cancel` | Laufendes Statement abbrechen; auf einem folgenden Ergebnis (`tail -f`): Folgen beenden |
| `F9` | `cmdline` | Kommandozeile fokussieren |
| `F10` | `quit` | Beenden (Rückfrage bei ungespeicherten Objekt-Tabs und offenen Transaktionen) |
| `Alt+F1` | `focus_tree` | Zum Objektbaum |
| `Alt+F2` | `focus_editor` | Zum Editor |
| `Alt+F3` | `focus_results` | Zu den Ergebnissen |
| `F11` | `maximize` | Editor oder Ergebnis in voller Höhe; nochmals stellt die Aufteilung wieder her |
| `Tab` | `next_pane` | Nächster Bereich |
| `Shift+Tab` | `prev_pane` | Vorheriger Bereich |

### Objektbaum (`[keys.tree]`)

| Taste | Aktion | Wirkung |
|---|---|---|
| `Enter` | `open` | Standardaktion: aufklappen, Daten öffnen oder Quelltext öffnen |
| `F3` | `cat` | `cat` des Objekts |
| `F4` | `edit` | `edit`: Quelltext einer View oder Routine im Editor-Tab |
| `F5` | `reload` | Knoten neu laden |
| `F7` | `new_row` | Neue Zeile für die Tabelle |
| `/` | `filter` | Baum nach Namen filtern |
| `c` | `chdir` | Aktuellen Pfad auf den Knoten setzen |

Fest: `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` bewegen; `Right`/`Left` auf-/zuklappen bzw. Kind/Elternknoten.

### Editor (`[keys.editor]`)

| Taste | Aktion | Wirkung |
|---|---|---|
| `F5` | `run_statement` | Statement unter dem Cursor ausführen |
| `F6` | `run_all` | Auswahl oder ganzen Puffer ausführen |
| `Ctrl+Space` | `complete` | Vervollständigung (zusätzlich `completion_key_alt`, Standard `Ctrl+O`) |
| `Alt+,` | `prev_tab` | Vorheriger Editor-Tab |
| `Alt+.` | `next_tab` | Nächster Editor-Tab |
| `Alt+N` | `new_tab` | Neuer Scratch |
| `Alt+O` | `open_scratch` | Scratch-Liste |
| `Alt+E` | `rename_scratch` | Scratch umbenennen |
| `Alt+X` | `close_tab` | Tab schließen |
| `Alt+R` | `restore` | Objekt-Tab wiederherstellen |

Die Bearbeitungstasten stehen in [Abschnitt 7](#der-editor); sie sind nicht umbelegbar. Zwischenablage: `Ctrl+Shift+C` kopieren, `Ctrl+Shift+X` ausschneiden, `Ctrl+Shift+V` einfügen.

### Ergebnis-Grid (`[keys.grid]`)

| Taste | Aktion | Wirkung |
|---|---|---|
| `F3` | `pivot` | Pivot-Ansicht umschalten |
| `F4` | `edit_row` | Datensatz bearbeiten |
| `F5` | `rerun` | Abfrage erneut ausführen |
| `F7` | `new_row` | Neue Zeile |
| `Enter` | `detail` | Zellendetail |
| `s` | `sort` | Nach aktueller Spalte sortieren (zyklisch) |
| `S` | `sort_add` | Spalte als weiteren Sortierschlüssel |
| `/` | `search` | Schnellsuche |
| `f` | `filter_column` | Spaltenfilter |
| `w` | `filter_where` | Freies WHERE (nur table-origin) |
| `x` | `clear_filters` | Filter und Sortierung entfernen |
| `n`, `F6` | `next_page` | Nächste Seite |
| `p`, `Shift+F6` | `prev_page` | Vorige Seite |
| `c` | `copy_cell` | Zelle kopieren |
| `C` | `copy_row` | Zeile kopieren |
| `[` | `prev_result` | Vorheriger Ergebnistab |
| `]` | `next_result` | Nächster Ergebnistab |
| `Alt+X` | `close_result` | Ergebnistab schließen (Messages bleibt) |
| `Alt+Q` | `close_all_results` | Alle Ergebnistabs schließen |

Fest: Pfeiltasten, `PgUp`, `PgDn`, `Home`, `End`.

### Formular (`[keys.form]`)

| Taste | Aktion | Wirkung |
|---|---|---|
| `F2` | `save` | Speichern |
| `F4` | `lookup` | Suchfenster oder Mehrzeilen-Editor |
| `F6` | `save_new` | Speichern und neu |
| `Ctrl+N` | `set_null` | NULL setzen |
| `Ctrl+U` | `untouch` | Auf untouched zurücksetzen |
| `Esc` | `cancel` | Abbrechen |

### Kommandozeile (`[keys.cmdline]`)

| Taste | Aktion | Wirkung |
|---|---|---|
| `Ctrl+Space` | `complete` | Vervollständigung (nur auf Anforderung; zusätzlich `completion_key_alt`) |
| `Up` | `history_prev` | Voriger Befehl (ohne offene Liste) |
| `Down` | `history_next` | Nächster Befehl (ohne offene Liste) |
| `Enter` | `execute` | Befehl oder SQL ausführen |

Fest: `Esc` leert die Zeile und wechselt zum Editor.

### Fußzeile je Bereich (Standardbelegung)

| Bereich | Fußzeile |
|---|---|
| Baum | `1Help 2Conn 3View 4Edit 5Reload 7New 8Cancel 9Cmd 10Quit` |
| Editor | `1Help 2Conn 5Run 6RunAll 8Cancel 9Cmd 10Quit` |
| Grid | `1Help 2Conn 3Pivot 4Edit 5Rerun 6NextPg 7New 8Cancel 9Cmd 10Quit` |
| Kommandozeile | `1Help 2Conn 8Cancel 9Cmd 10Quit` |

---

## 17. Eigene Befehle in Lua

Jeder Befehl von dbcc – auch `cd`, `ll`, `cp` und `cat` – ist ein Lua-Skript. Go stellt nur Primitive bereit (Abfragen ausführen, Objekte auflösen, Ergebnisse anzeigen, nachfragen …); was ein Befehl tut, steht vollständig im Skript. Eigene Skripte ergänzen die eingebauten Befehle oder ersetzen sie. Sie verhalten sich wie eingebaute: Vervollständigung, `help`, Headless-Betrieb, Tasten und Baumaktionen funktionieren ohne weiteres Zutun.

Inhalt dieses Abschnitts:

- [Wo die Skripte liegen](#wo-die-skripte-liegen)
- [Laden, Ersetzen, Rückfall](#laden-ersetzen-rückfall)
- [Skripte zur Laufzeit installieren: `reload`](#skripte-zur-laufzeit-installieren-reload)
- [Schritt für Schritt: der erste eigene Befehl](#schritt-für-schritt-der-erste-eigene-befehl)
- [Einen Befehl deklarieren: `dbcc.command`](#einen-befehl-deklarieren-dbcccommand)
- [Argumente und Flags beim Aufruf](#argumente-und-flags-beim-aufruf)
- [Schreibende Befehle (`write = true`)](#schreibende-befehle-write--true)
- [Der Kontext `ctx`](#der-kontext-ctx)
- [Primitive](#primitive)
- [Parameter und Platzhalter](#parameter-und-platzhalter)
- [Verhalten ohne UI](#verhalten-ohne-ui)
- [Metadaten-Tabelle von `ctx:meta`](#metadaten-tabelle-von-ctxmeta)
- [`dbcc.commands()` und `dbcc.fmt`](#dbcccommands-und-dbccfmt)
- [Fehler und Exit-Codes](#fehler-und-exit-codes)
- [Sandbox](#sandbox)
- [Beispiele](#beispiele)
- [Tipps: Testen, Debuggen, Quoting, Sicherheit](#tipps-testen-debuggen-quoting-sicherheit)

### Wo die Skripte liegen

**Eingebaute Befehle** sind in das Binary eingebettet; es gibt sie also auf jedem Rechner, auf den man nur die Datei `dbcc` kopiert. Ihr Quelltext liegt im Repository unter `internal/script/commands/` – eine Datei je Befehl, der Dateiname ist der Befehlsname:

| Datei | Befehl | Datei | Befehl |
|---|---|---|---|
| `cat.lua` | [`cat`](#cat) | `ll.lua` | [`ll`](#ll) |
| `cd.lua` | [`cd`](#cd) | `ls.lua` | [`ls`](#ls) |
| `cp.lua` | [`cp`](#cp) (schreibt) | `mv.lua` | [`mv`](#mv) (schreibt) |
| `du.lua` | [`du`](#du) | `ps.lua` | [`ps`](#ps) |
| `edit.lua` | [`edit`](#edit) | `pwd.lua` | [`pwd`](#pwd) |
| `export.lua` | [`export`](#export) | `reload.lua` | [`reload`](#reload) |
| `grep.lua` | [`grep`](#grep) | `tail.lua` | [`tail`](#tail) |
| `head.lua` | [`head`](#head) | `wc.lua` | [`wc`](#wc) |
| `help.lua` | [`help`](#help) | `kill.lua` | [`kill`](#kill) (schreibt) |
| `history.lua` | [`history`](#history) | | |

Die eingebauten Skripte verwenden nur die öffentliche API, die unten beschrieben ist – sie sind die besten Beispiele. Ohne Repository holt man sie mit

```sh
dbcc --export-commands ~/dbcc-commands     # schreibt alle eingebauten Skripte dorthin
```

Das Verzeichnis wird bei Bedarf angelegt. `--export-commands` überschreibt nie: Existiert auch nur eine der Zieldateien, wird gar nichts geschrieben (Exit-Code 2) – also ein leeres oder neues Verzeichnis wählen.

**Eigene Befehle** liegen als `*.lua` im Benutzerverzeichnis

```text
$XDG_CONFIG_HOME/dbcc/commands/*.lua     # wenn XDG_CONFIG_HOME gesetzt ist
~/.config/dbcc/commands/*.lua            # sonst
```

Das Verzeichnis muss man selbst anlegen (`mkdir -p ~/.config/dbcc/commands`); fehlt es, ist das kein Fehler. Unterverzeichnisse und andere Dateiendungen werden nicht gelesen. Eine Datei darf mehrere Befehle registrieren; üblich und übersichtlicher ist ein Befehl je Datei mit dem Befehlsnamen als Dateiname. `help <befehl>` zeigt, woher ein Befehl stammt (`source: embedded:head.lua` oder der volle Pfad der Datei).

### Laden, Ersetzen, Rückfall

- **Wann:** Beim Start lädt dbcc zuerst alle eingebauten Skripte, danach die Benutzerskripte in alphabetischer Reihenfolge der Dateinamen. In der UI lädt [`reload`](#skripte-zur-laufzeit-installieren-reload) alles neu, ohne Neustart. Headless lädt jeder Aufruf von `dbcc -c …` die Skripte frisch – dort genügt es, die Datei zu speichern.
- **Ersetzen:** Ein Benutzerbefehl mit dem Namen eines eingebauten **ersetzt** diesen – überall, auch bei Tasten und Baumaktionen, die Befehle über ihren Namen aufrufen (ein eigenes `ll.lua` ändert also auch, was `Enter` auf einer Tabelle im Baum zeigt; `F3`/`F4` im Baum rufen `cat` bzw. `edit` auf). Registrieren zwei Benutzerdateien denselben Namen, gewinnt die alphabetisch spätere.
- **Rückfall:** Ein Skript, das nicht lädt (Syntaxfehler, Laufzeitfehler beim Laden, ungültige Deklaration, Zeitüberschreitung), wird im Messages-Log (headless: stderr) gemeldet – `user command script skipped: <pfad>: <fehler>` – und übersprungen. Ein eingebauter Befehl gleichen Namens bleibt dann aktiv – ein kaputtes `cd.lua` sperrt also niemanden aus. Die Registrierungen eines Skripts werden nur wirksam, wenn das ganze Skript fehlerfrei lädt: Ein Skript, das erst `dbcc.command{…}` aufruft und danach scheitert, registriert gar nichts.
- **Zeitlimit:** Das Laden eines Skripts (der Code auf oberster Ebene, nicht die `run`-Funktion) darf höchstens 5 Sekunden dauern; danach: `loading took longer than 5s`.
- **Nur eingebaute:** `dbcc --no-user-commands` lädt keine Benutzerskripte – der Ausweg, wenn ein eigenes Skript stört. Das gilt auch für `reload` in dieser Sitzung.

Ein kaputter Ersatz für `head` headless (gekürzt):

```text
$ dbcc -p doc -c "head logs 1"
warning: user command script skipped: ~/.config/dbcc/commands/head.lua: ~/.config/dbcc/commands/head.lua line:21(column:7) near '=':   syntax error

id | level | msg     | created
---+-------+---------+--------------------
 4 | info  | cleanup | 2026-06-01 09:00:00
```

Die Warnung kommt auf stderr, das Ergebnis stammt vom eingebauten `head`.

### Skripte zur Laufzeit installieren: `reload`

Neue oder geänderte Skripte lassen sich installieren, während dbcc läuft – ohne Neustart und ohne Verbindungen, Transaktionen, Editor-Tabs oder Ergebnisse zu verlieren:

1. Die `.lua`-Datei in das Benutzerverzeichnis legen bzw. dort ändern (z. B. in einem zweiten Terminal).
2. In dbcc `F9` (Kommandozeile) drücken und `reload` eingeben.
3. Der neue Befehl ist sofort verfügbar – mit Vervollständigung und `help`.

`reload` baut eine neue Lua-Laufzeit, lädt die eingebauten Skripte und danach die Benutzerskripte genau wie beim Start und ersetzt dann die alte Laufzeit. Folgen:

- Ein kaputtes Skript wird gemeldet und übersprungen; ersetzt es einen eingebauten Befehl, bleibt dieser aktiv. Alle anderen Befehle funktionieren weiter.
- Zustand, den Skripte auf oberster Ebene halten (lokale Variablen, Tabellen), beginnt neu.
- Gelöschte Dateien verschwinden: Ein entfernter Benutzerbefehl ist nach `reload` weg, ein entfernter Ersatz gibt den eingebauten Befehl zurück.
- Headless gibt es kein `reload` (es scheitert mit „only available in the UI (headless mode loads the commands at every start)“) und es wird auch nicht gebraucht.

So sieht das im Messages-Log aus (echte Sitzung; erst wird `tpl` getippt, bevor `tpl.lua` existiert – dbcc hält es dann für SQL –, danach die Datei angelegt und `reload` ausgeführt):

```text
00:40:01 line 1 tpl  ERROR Error 1064 (42000): You have an error in your SQL syntax; …
00:40:02 reloading commands; user scripts are read from /home/anna/.config/dbcc/commands
00:40:02 commands reloaded: 26 commands (user scripts from /home/anna/.config/dbcc/commands)
00:40:12 tpl: template for settings in the editor
```

Mit einem fehlerhaften Skript im Verzeichnis:

```text
00:40:18 reloading commands; user scripts are read from /home/anna/.config/dbcc/commands
00:40:19 ERROR user command script skipped: /home/anna/.config/dbcc/commands/oops.lua: … syntax error
00:40:19 commands reloaded: 26 commands (user scripts from /home/anna/.config/dbcc/commands)
```

Eigene Skripte können das Neuladen mit [`ctx:reload_commands()`](#primitive) selbst auslösen.

### Schritt für Schritt: der erste eigene Befehl

1. Verzeichnis anlegen:

   ```sh
   mkdir -p ~/.config/dbcc/commands
   ```

2. Datei `~/.config/dbcc/commands/hello.lua` schreiben:

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

   `dbcc.command{…}` registriert den Befehl beim Laden. `run` läuft bei jedem Aufruf; `ctx` ist der Kontext mit allen Primitiven, `args` sind die Argumente als Strings.

3. Ohne UI testen – der schnellste Weg, weil jeder Aufruf die Skripte neu lädt:

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

   Die Usage-Zeile hat dbcc aus `args` erzeugt. `ctx:message` schreibt headless auf stderr, in der UI ins Messages-Log.

4. In der laufenden UI: `F9`, `reload`, dann `hello` – oder dbcc neu starten.

Die folgenden Abschnitte beschreiben jedes Feld und jedes Primitiv; danach folgen [ausführliche Beispiele](#beispiele).

### Einen Befehl deklarieren: `dbcc.command`

`dbcc.command{…}` darf nur aufgerufen werden, während ein Skript lädt (also auf oberster Ebene, nicht in `run`); sonst: „dbcc.command may only be called while a command script loads“. Felder:

| Feld | Typ | Bedeutung |
|---|---|---|
| `name` | string, Pflicht | `^[a-z][a-z0-9_-]*$` (Kleinbuchstaben, Ziffern, `_`, `-`; beginnt mit einem Buchstaben). `dbcc` ist reserviert (T-SQL-`DBCC`-Statements gehen als SQL durch). |
| `usage` | string | Usage-Zeile für Hilfe und Fehlermeldungen. Ohne Angabe erzeugt: `name [-a] [-t string] <object> [int] [string ...]` |
| `help` | string | Beschreibung (Befehlsliste von `help`, `help <befehl>`, Hilfe-Overlay) |
| `args` | Liste von strings | Positionsargumente mit Typ: `object`, `path`, `profile`, `file`, `int`, `string`. Endung `?` = optional, `...` = beliebig viele (auch keins; nur als letztes). Pflichtargumente dürfen nicht nach optionalen stehen. Der Typ steuert die Vervollständigung (Objekte, Pfade, Profile, Dateien) und die Prüfung (`int`). |
| `flags` | Tabelle Name → Typ | `bool` (Schalter ohne Wert) oder einer der Argumenttypen (Flag mit Wert; `int` wird geprüft). Namen: Buchstabe, dann Buchstaben, Ziffern, `_`, `-`. |
| `flag_values` | Tabelle Flag → Liste von strings | Werte, die die Vervollständigung für ein Flag mit Wert anbietet (z. B. `flag_values = { t = { "table", "view" } }` bei `cat`). Andere Werte bleiben erlaubt. |
| `write` | boolean | `true`: Der Befehl schreibt – siehe [Schreibende Befehle](#schreibende-befehle-write--true) |
| `run` | function, Pflicht | `run(ctx, args, flags)` |

Jede ungültige Deklaration lässt das ganze Skript scheitern. Die Meldungen (mit Datei und Zeile davor):

| Fehler | Meldung |
|---|---|
| unbekanntes Feld | `dbcc.command: unknown field(s) [descr] (name, usage, help, args, flags, flag_values, write, run)` |
| ungültiger Name | `dbcc.command: invalid command name "B2" (lower-case letters, digits, '_' and '-')` |
| Name `dbcc` | `dbcc.command: the name "dbcc" is reserved: DBCC statements are plain SQL` |
| falscher Feldtyp | `field "name" must be a string, got number`, `field "write" must be a boolean`, `field "args" must be a list` … |
| `run` fehlt | `dbcc.command b8: field "run" must be a function` |
| unbekannter Argumenttyp | `args[1]: unknown type "number" (object, path, profile, file, int, string)` |
| Reihenfolge | `args[2]: a required argument cannot follow an optional one`, `args[1]: a variadic argument must be the last one` |
| unbekannter Flagtyp | `flags.all: unknown type "boolean" (bool, object, path, profile, file, int, string)` |
| `flag_values` ohne passendes Flag | `flag_values.x: no such flag with a value` |

### Argumente und Flags beim Aufruf

`run(ctx, args, flags)` bekommt:

- `args`: die Positionsargumente als Liste von **Strings** (auch `int`-Argumente – mit `tonumber` umwandeln). Fehlende optionale Argumente sind `nil`.
- `flags`: die angegebenen Flags; Schalter sind `true`, Flags mit Wert enthalten den Wert als String. Nicht angegebene Flags fehlen (`nil`).

Die Zeile wird wie in einer Shell in Wörter zerlegt: Leerzeichen trennen, `'…'` und `"…"` gruppieren (in `"…"` maskiert `\` ein `"` oder `\`), `\` außerhalb maskiert das nächste Zeichen. Ein `;` am Zeilenende wird ignoriert. Danach gilt:

- Flags stehen **vor** den Positionsargumenten. `-a` und `--a` sind gleichwertig. Werte: `-t wert`, `-t=wert`, `--t=wert`. Schalter lassen sich nicht bündeln (`-ab` ist das Flag `ab`).
- Das erste Wort, das nicht wie ein Flag aussieht, beendet die Flags; ebenso `--` (danach ist alles positional). `-` allein und negative Zahlen (`-5`) sind positional.
- Ein Befehl **ohne** deklarierte Flags nimmt jedes Wort als Positionsargument, auch `-x`.
- Geprüft wird **vor** `run`: unbekannte Flags, Wert bei einem Schalter, fehlender Wert, `int`-Werte, die Anzahl der Argumente, `int`-Argumente. Fehler sind Aufruffehler (headless Exit-Code 2):

```text
$ dbcc -p doc -c "counts -x"
ERROR: counts: unknown flag -x; usage: counts [-m min] [path]
$ dbcc -p doc -c "latest logs x"
ERROR: latest: argument 2 must be an integer, got "x"; usage: latest <table> [n]
$ dbcc -p doc -c "setting"
ERROR: setting: usage: setting <name>
```

Weitere Meldungen: `flag -n takes no value`, `flag -m needs a int value`, `flag -m must be an integer, got "x"`.

### Schreibende Befehle (`write = true`)

`write = true` kennzeichnet einen Befehl, der Daten oder Struktur ändert (eingebaut: `cp`, `mv`, `kill`). Das bewirkt:

- **Schreibgeschützte Profile:** Steht der aktuelle Pfad in einem Profil mit `read_only = true`, startet der Befehl gar nicht: `archive-logs: writes and is blocked in the read-only profile docro`. Auch `ctx:exec` auf einer anderen schreibgeschützten Verbindung (`ctx:on(…)`) wird abgelehnt.
- **Jedes `ctx:exec` wird bestätigt:** Vor jedem Statement erscheint ein Ja/Nein-Dialog „`<befehl>` will run on `<profil>`:“ mit dem Statement. „Nein“ bricht den Befehl mit „cancelled by user“ ab; was bis dahin lief, bleibt (bei Autocommit) bestehen. Der Dialog zeigt das Statement mit seinen Platzhaltern (`?`), nicht die Parameterwerte.
- **Einmal fragen:** Bejaht der Benutzer in diesem Aufruf ein `ctx:confirm(…)`, fragen die folgenden `ctx:exec` nicht mehr. So zeigt man alle Statements (und die Werte) in einer Frage – wie `cp` es tut, siehe [Beispiel d](#d-schreibender-befehl-mit-parametern-und-rückfrage).
- **`confirm_writes`** des Profils fragt für ein so bestätigtes Statement nicht ein zweites Mal.
- **Headless:** Ohne `--yes` scheitert die Rückfrage mit `confirmation required; run with --yes:` und dem Fragetext (Exit-Code 1); mit `--yes` gilt jede Rückfrage als bejaht, mit `-v` zusätzlich protokolliert (`-- confirmed (--yes): …`).
- `ctx:query` (Lesen) wird nie bestätigt.

Auch Befehle ohne `write = true` können mit `ctx:exec` schreiben; dann gelten nur die Schutzmechanismen, die auch für getipptes SQL gelten (Lese-Schutz des Profils: „read-only profile: statement rejected“, `confirm_writes`). Für alles, was Daten ändert, ist `write = true` richtig.

### Der Kontext `ctx`

`ctx` ist nur gültig, solange sein Befehl läuft (ein in einer Variablen aufbewahrtes `ctx` scheitert später mit „ctx is only valid while its command runs“). Methoden immer mit Doppelpunkt aufrufen: `ctx:query(…)`; `ctx.query(…)` scheitert mit „call it with a colon“.

Felder (nur lesen):

| Feld | Inhalt |
|---|---|
| `ctx.command` | Name des laufenden Befehls |
| `ctx.path` | aktueller Pfad: `{conn=, database=, schema=, str="/c/d/s", level=}`; `level` ist `"root"`, `"connection"`, `"database"` oder `"schema"`; fehlende Teile sind `nil`; `tostring(ctx.path)` = `str` |
| `ctx.dialect` | `"mysql"`, `"mariadb"`, `"postgres"`, `"mssql"` oder `nil` (nicht verbunden, Pfad `/`) |
| `ctx.has_schemas` | `true` bei PostgreSQL und SQL Server (Pfade mit Schemaebene) |
| `ctx.profile` | `{name=, driver=, database=, read_only=, autocommit=, confirm_writes=}` oder `nil`; `driver` ist der Konfigurationswert (`mysql` auch bei MariaDB), `database` nur, wenn im Profil gesetzt |
| `ctx.conn` | nur bei einem mit `ctx:on` gebundenen Kontext: der Verbindungsname |

Beispiel (echte Ausgabe auf MariaDB, Profil `doc` mit `database = "dbcc_doc"`): `ctx.path.str` = `/doc/dbcc_doc`, `ctx.path.level` = `database`, `ctx.dialect` = `mariadb`, `ctx.profile.driver` = `mysql`, `ctx.has_schemas` = `false`.

**`ctx:on(target)`** liefert einen Kontext, der an eine andere Verbindung gebunden ist: `target` ist ein Profilname, ein Objekt aus `ctx:resolve` oder eine Pfadtabelle; die Verbindung wird bei Bedarf geöffnet. Bei einem gebundenen Kontext beziehen sich `dialect`, `has_schemas`, `profile`, alle SQL-Helfer (`quote`, `limit`) und die Ausführung (`query`, `exec`, `grid`, `resolve` mit einfachen Namen) auf diese Verbindung; `ctx.path` bleibt der aktuelle Pfad. Ist `target` die aktuelle Verbindung, folgt der Kontext ihr wie `ctx`. Faustregel: **Objekte immer über `ctx:on(obj)` ansprechen**, denn ein Objekt aus dem Baum oder ein absoluter Pfad kann auf einer anderen Verbindung liegen.

### Primitive

Alle Primitive blockieren, bis ihre Datenbankarbeit erledigt ist, und lösen bei Fehlern einen Lua-Fehler aus (siehe [Fehler](#fehler-und-exit-codes)); mit `pcall` lässt er sich abfangen.

**Navigation und Metadaten:**

| Methode | Rückgabe / Wirkung |
|---|---|
| `ctx:chdir(path)` | wechselt den Pfad wie `cd` (`USE` bzw. Neuverbindung, ggf. Rückfrage bei offener Transaktion); aktualisiert `ctx.path`, `ctx.dialect`, `ctx.profile`; gibt den neuen Pfad (Pfadtabelle) zurück |
| `ctx:abspath(path)` | löst einen Pfad ohne Seiteneffekte auf (`"."` oder `nil` = aktueller, `".."`, relative und absolute Pfade); gibt eine Pfadtabelle zurück. `path` darf auch eine Pfadtabelle sein |
| `ctx:objects(path, kinds)` | unter `/conn`: die Datenbanken; unter einer Datenbank eines Schema-Dialekts: die Schemata; sonst die Objekte der Datenbank bzw. des Schemas, gefiltert nach `kinds` (String oder Liste: `database`, `schema`, `table`, `view`, `materialized view`, `procedure`, `function`, `trigger`, `event`, `sequence`; `nil` = alle). Elemente `{name=, kind=, comment=, system=}`. An `/` ein Fehler: dort `ctx:profiles()` verwenden |
| `ctx:profiles()` | alle Profile der Konfiguration, nach Namen sortiert: `{name=, driver=, host=, port=, socket=, user=, database=, read_only=, autocommit=, confirm_writes=, open=, current=}`; nicht gesetzte Texte sind `""`, `port` ist eine Zahl, `open` = Verbindung offen, `current` = aktuelles Profil |
| `ctx:resolve(name, kinds)` | löst einen Objektnamen auf, siehe unten |
| `ctx:on(target)` | an die Verbindung von `target` gebundener Kontext (siehe oben) |
| `ctx:meta(obj)` | Metadaten einer Tabelle, View oder Materialized view (`obj` = Name oder Objekt), siehe [Metadaten-Tabelle](#metadaten-tabelle-von-ctxmeta) |
| `ctx:source(obj, kind)` | Quelltext einer View, Prozedur, Funktion, eines Triggers oder Events (`kind` optional, schränkt die Suche ein) |

`ctx:resolve(name, kinds)` akzeptiert:

- einen einfachen Namen (`logs`) – gesucht in der aktuellen Datenbank bzw. dem aktuellen Schema;
- qualifizierte Namen: `db.obj` (MySQL/MariaDB), `schema.obj` und `db.schema.obj` (PostgreSQL, SQL Server); Teile dürfen gequotet sein (`` `my table` ``, `"x.y"`, `[a b]`, verdoppelte Quotes maskieren sich);
- Pfade, deren letztes Segment das Objekt ist: `/conn/db/obj`, `/conn/db/schema/obj`, `../anderedb/obj`. Ein Pfad darf auf ein anderes Profil zeigen; dessen Verbindung wird geöffnet.

Gesucht wird exakt, sonst ohne Beachtung der Groß-/Kleinschreibung, wenn das eindeutig ist (`LOGS` findet `logs`). `kinds` (wie bei `ctx:objects`) schränkt die Suche ein; ein Objekt anderer Art gilt dann als nicht gefunden. Rückgabe:

```text
{ name="logs", kind="table", comment="", conn="doc", sql="`dbcc_doc`.`logs`",
  parts={ conn="doc", database="dbcc_doc", schema=nil, object="logs" } }
```

`sql` ist der vollständig qualifizierte, gequotete Name – direkt in SQL verwendbar; `tostring(obj)` = `obj.sql`. Fehler: `no such object: x in /doc/dbcc_doc`, `"a.b.c": too many name parts (mariadb has no schema level)`, `"x": no database selected; cd into a database or qualify the name`, `not connected: cd /<profile> first`.

Echte Auflösungen auf MariaDB (aktueller Pfad `/doc/dbcc_doc`):

| Eingabe | `obj.sql` |
|---|---|
| `logs`, `` `logs` ``, `LOGS`, `dbcc_doc.logs` | `` `dbcc_doc`.`logs` `` |
| `/doc/shop/orders`, `../shop/orders` | `` `shop`.`orders` `` |

**SQL-Helfer:**

| Methode | Rückgabe |
|---|---|
| `ctx:quote(ident)` | gequoteter Bezeichner des Dialekts: `` `we``ird` `` (MySQL/MariaDB), `"we""ird"` (PostgreSQL), `[we]]ird]` (SQL Server) |
| `ctx:limit(sql, n)` | die Abfrage mit Zeilenlimit `n` (≥ 0). MySQL/MariaDB/PostgreSQL: hat die Abfrage ein `ORDER BY` auf oberster Ebene, wird `LIMIT n` angehängt (`SELECT * FROM t ORDER BY a LIMIT 5`), sonst wird sie umschlossen (`SELECT * FROM (SELECT * FROM t) AS dbcc_limit LIMIT 5`). SQL Server: `TOP (n)` wird in das Haupt-`SELECT` eingesetzt (`SELECT TOP (5) * FROM t ORDER BY a`), bei `UNION`, `OFFSET` oder vorhandenem `TOP` wird umschlossen |
| `ctx:copy_sql(src, dst, opts)` | `stmts, target`: Liste der Statements, die `src` (Name oder Objekt) als neue Tabelle `dst` kopieren (wie `cp`); `opts.structure = true` wählt die `cp -a`-Variante mit Schlüsseln und Indizes. `target` ist eine objektartige Tabelle der neuen Tabelle (`name`, `kind`, `conn`, `sql`, `parts`). Führt nichts aus |
| `ctx:replace_script(obj, kind)` | ausführbares Skript, das das Objekt aus seinem aktuellen Quelltext neu anlegt (wie `edit`), siehe [Objekt-Tabs](#objekt-tabs-und-replace-skripte) |

**Ausführung:**

| Methode | Rückgabe / Wirkung |
|---|---|
| `ctx:query(sql, ...)` | führt eine Abfrage aus (Parameter nach `sql`) und puffert das Ergebnis bis `max_rows_buffered` (Standard 10.000). Rückgabe: ein Array von Zeilen; jede Zeile ist ein Array von **Strings** (Zahlen und Datumswerte als Text, so wie das Grid sie zeigt) bzw. **`nil` für NULL**. Zusätzlich `row.spaltenname` (bei doppelten Namen die erste Spalte; unbekannte Namen ergeben `nil`), `res.columns` (Liste der Spaltennamen) und `res.capped` (`true`, wenn bei `max_rows_buffered` abgeschnitten wurde) |
| `ctx:exec(sql, ...)` | führt ein Statement aus; gibt die Zahl der betroffenen Zeilen zurück. In `write`-Befehlen vorher die Rückfrage (siehe oben) |
| `ctx:reload_commands()` | lädt alle Befehle neu, sobald der laufende Befehl beendet ist (wie `reload`); gibt das Benutzerverzeichnis zurück. Nur in der UI |

Ergebnis von `ctx:query("SELECT id, level, msg FROM logs ORDER BY id")` (echte Werte):

```lua
#res          --> 3
res.columns   --> { "id", "level", "msg" }
res.capped    --> false
res[1][1]     --> "4"          (ein String, nicht die Zahl 4)
res[1].level  --> "info"
res[3].msg    --> nil          (NULL)
```

`ctx:query` und `ctx:exec` laufen in der Sitzung wie getipptes SQL: im Transaktionsmodus des Profils, mit dem Lese-Schutz und `confirm_writes` des Profils. Sie erscheinen nicht in der Statement-Historie.

**Ausgabe:**

| Methode | Wirkung |
|---|---|
| `ctx:grid(sql, opts, ...)` | öffnet ein lazy geladenes Ergebnis in einem Ergebnistab (Parameter **nach** `opts`; ohne Optionen `ctx:grid(sql, nil, p1)`). `opts.origin` (Objekt aus `ctx:resolve`) macht es **table-origin** und führt es auf der Verbindung des Objekts aus: Sortieren und Filtern auf dem Server, freies `WHERE`, Zeilenformular (`F4`), `F7` neue Zeile. `opts.title` benennt den Tab. `opts.follow = {sql=, key=, interval=}` macht es zu einem folgenden Ergebnis (unten). Kehrt sofort zurück, das Nachladen geschieht beim Blättern |
| `ctx:rows(headers, rows, opts)` | zeigt in Lua berechnete Zeilen als Ergebnistab: `headers` ist eine Liste von Namen, `rows` eine Liste von Zeilenlisten; Werte werden mit `tostring` umgewandelt, `nil` = NULL. Spalten, deren Werte alle Lua-Zahlen sind, sind numerisch (rechtsbündig). Mehr Werte als Überschriften ergeben Spalten `col2`, `col3` …. `opts.title` (Standard: Befehlsname). Kein table-origin |
| `ctx:text(str, lexer, opts)` | Text in einem Tab: `lexer = "sql"` = SQL-Lexer des Dialekts, sonst ein Chroma-Lexername (`"lua"`, `"json"` …), `nil` = Klartext; `opts.title` (Standard: Befehlsname) |
| `ctx:message(msg)` | Eintrag im Messages-Log; `msg` wird mit `tostring` umgewandelt |
| `print(...)` | wie `ctx:message`, Argumente durch Tabulatoren getrennt; auch beim Laden eines Skripts nutzbar |
| `ctx:export(file, opts)` | schreibt das aktive Ergebnis (den aktuellen Ergebnistab) als `.csv` oder `.json` (Format aus der Endung) wie `export`: der offene Cursor wird bis zum Ende gelesen, ein lokaler Filter angewandt, eine lokale Sortierung lädt erst alle Zeilen. `~/` wird expandiert. `opts.force = true` überschreibt eine vorhandene Datei, `opts.progress` = Zeilenabstand der Fortschrittsmeldungen (Standard 10.000, 0 = keine). Gibt die Zahl der geschriebenen Zeilen zurück. Bei Fehlern wird keine halbe Datei hinterlassen |

`opts.follow` von `ctx:grid`: `follow.sql` ist eine Abfrage mit genau einem `?` für den zuletzt gesehenen Wert der Ergebnisspalte `follow.key` und liefert die neueren Zeilen aufsteigend nach ihr sortiert. Sie läuft alle `interval` Sekunden (Standard 2, mindestens 0.5) im Hintergrund auf der Hilfsverbindung; ihre Zeilen werden angehängt, das Ergebnis behält höchstens `page_size` Zeilen. Das Folgen endet, wenn der Tab geschlossen oder das Ergebnis abgebrochen wird. Vorlage: `tail.lua`.

Hinweis zu table-origin: Sortieren und Filtern auf dem Server bauen die Abfrage aus dem Objekt neu (`SELECT * FROM <obj> WHERE … ORDER BY …`). `ORDER BY` und `LIMIT` der Abfrage des Befehls gelten danach nicht mehr – so verhalten sich auch `head` und `tail`.

**Interaktion:**

| Methode | Wirkung |
|---|---|
| `ctx:confirm(msg)` | Ja/Nein-Dialog; gibt `true` oder `false` zurück. In einem `write`-Befehl deckt ein „Ja“ alle folgenden `ctx:exec` dieses Aufrufs ab |
| `ctx:pick(items, opts)` | filterbare Auswahlliste (tippen filtert, `Enter` wählt, `Esc` bricht ab). `items` sind Strings oder Tabellen mit einem Feld `label` (weitere Felder sind erlaubt und kommen unverändert zurück). Gibt `item, index` zurück (das gewählte Element selbst und seine Position ab 1) bzw. ein einzelnes `nil` bei Abbruch; `opts.title` |
| `ctx:editor_set(text)` | ersetzt den Text des aktuellen Scratch-Tabs; ist der aktuelle Tab kein Scratch, den des ersten Scratches, sonst eines neuen |
| `ctx:editor_open(title, text, opts)` | mit `opts.object` (aus `ctx:resolve`): öffnet bzw. fokussiert einen an das Objekt gebundenen Objekt-Tab mit Titel `title` (wie `edit`); ohne Objekt: ein neuer Scratch mit dem Text (`title` wird dann nicht verwendet) |
| `ctx:about()` | zeigt die About-Seite |
| `ctx:history(n)` | die letzten `n` Einträge der Statement-Historie (Standard 100), älteste zuerst: `{time="2026-11-22 00:38:48", profile=, path=, duration_ms=, ok=, outcome=, stmt=}` |

### Parameter und Platzhalter

Parameter schreibt man in jedem Dialekt als `?` und übergibt die Werte nach dem SQL-Text (bei `ctx:grid` nach `opts`):

```lua
ctx:query("SELECT value FROM settings WHERE name = ?", args[1])
ctx:exec("DELETE FROM logs WHERE created < ? AND level = ?", before, "info")
ctx:grid("SELECT * FROM logs WHERE level = ?", { title = "warnings" }, "warn")
```

- Werden Argumente übergeben, ersetzt dbcc jedes `?` außerhalb von Strings, Kommentaren und gequoteten Bezeichnern durch den Platzhalter des Dialekts: `?` bleibt auf MySQL/MariaDB, `$1, $2 …` auf PostgreSQL, `@p1, @p2 …` auf SQL Server.
- **Ohne Argumente bleibt das Statement unverändert** – auf PostgreSQL ist `?` auch ein jsonb-Operator (`'{"a":1}'::jsonb ? 'a'` funktioniert also ohne Parameter).
- Native Platzhalter sind ebenfalls möglich (`$1` auf PostgreSQL, `@p1` auf SQL Server); `?` und native Platzhalter in einem Statement zu mischen nicht.
- Erlaubte Werte: `nil` (NULL), Booleans, Zahlen (ganze Zahlen werden als Ganzzahl übergeben), Strings. Tabellen sind ein Fehler.
- PostgreSQL leitet den Typ eines Parameters aus dem Zusammenhang ab. Wo das nicht geht (`SELECT ? AS a`), ist er `text`, und eine Lua-Zahl scheitert mit „unable to encode 5 into text format“: dann `?::int` schreiben oder einen String übergeben. `WHERE id = ?` mit einer Zahl funktioniert.
- Bezeichner (Tabellen- und Spaltennamen) können keine Parameter sein: dafür `obj.sql` aus `ctx:resolve` bzw. `ctx:quote` verwenden.

### Verhalten ohne UI

Jedes Primitiv funktioniert auch headless (`dbcc -p … -c "<befehl>"`):

| Primitiv | UI | headless |
|---|---|---|
| `ctx:grid` | Ergebnistab, seitenweise | alle Zeilen auf stdout im Format `--format` (kein `max_rows`); mit `follow` weiter bis SIGINT/SIGTERM |
| `ctx:rows` | Ergebnistab | stdout im Format `--format` |
| `ctx:text` | Text-Tab mit Hervorhebung | Text unverändert auf stdout (ohne Farbe, ohne Titel) |
| `ctx:message`, `print` | Messages-Log | stderr |
| `ctx:confirm`, Rückfrage von `ctx:exec` | Ja/Nein-Dialog | ohne `--yes`: Fehler `confirmation required; run with --yes:` + Fragetext; mit `--yes`: `true` |
| `ctx:pick` | Auswahlliste | Fehler `ctx:pick: choosing from a list is not available without UI` |
| `ctx:editor_set` | Scratch-Tab | Fehler `ctx:editor_set: the editor is not available without UI` |
| `ctx:editor_open` | Editor-Tab | Text auf stdout |
| `ctx:about` | About-Seite | About-Text auf stdout |
| `ctx:export` | aktueller Ergebnistab | das zuletzt ausgegebene Ergebnis – das ist dann schon auf stdout gestreamt; headless besser `--format csv`/`json` verwenden |
| `ctx:reload_commands` | lädt neu | Fehler (headless lädt jeder Aufruf neu) |

Die Fehler von `ctx:pick` und `ctx:editor_set` lassen sich mit `pcall` abfangen, um headless eine Alternative zu bieten – so macht es `history.lua`, so macht es [Beispiel g](#g-interaktion-auswahlliste-und-editor).

### Metadaten-Tabelle von `ctx:meta`

```text
{ name=, kind=, sql=, conn=, parts={…}, comment=, view_definition=, options={schlüssel=wert},
  columns = { {name=, position=, data_type=, nullable=, default=(nil: keiner), default_expr=,
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

Die ersten Felder entsprechen `ctx:resolve`. Echte Werte für `logs` auf MariaDB: `m.primary_key.name` = `PRIMARY`, `m.primary_key.columns` = `{ "id" }`, `#m.columns` = 4, `m.columns[1].data_type` = `int(11)`, `m.columns[1].auto_increment` = `true`, `m.columns[3].nullable` = `true`. `cat.lua` zeigt, wie man alle Felder ausgibt.

### `dbcc.commands()` und `dbcc.fmt`

| Funktion | Rückgabe |
|---|---|
| `dbcc.commands()` | Liste aller registrierten Befehle, nach Namen sortiert: `{name=, usage=, help=, args={"object", "int?"}, flags={name=typ}, write=, source=, default=}`; `default` ist `true` bei eingebauten Befehlen (`help.lua` baut daraus die Hilfe) |
| `dbcc.fmt.table(headers, rows)` | ausgerichtete Texttabelle als String; `nil` = `NULL`; rein numerische Spalten rechtsbündig |
| `dbcc.fmt.size(bytes)` | `"512 B"`, `"1.5 KiB"`, `"16.0 MiB"` … (`nil` → `""`) |
| `dbcc.fmt.count(n)` | `"1,234,567"` |

### Fehler und Exit-Codes

- **Eigene Fehler** meldet man mit `error("text", 0)`. Die `0` lässt die Positionsangabe weg; `error("text")` ergibt `<befehl>: /pfad/zur/datei.lua:14: text`. Nur Strings übergeben – eine Tabelle erscheint als `table: 0x…`.
- **Fehler von Primitiven** (SQL-Fehler, nicht gefundene Objekte …) werden zu Lua-Fehlern mit dem Text des Fehlers. Ungefangen beenden sie den Befehl.
- Jeder Fehler eines Befehls wird mit dem Befehlsnamen versehen: `setting: no setting named nope`, `probe: Error 1064 (42000): You have an error in your SQL syntax …`. In der UI erscheint er im Messages-Log und in der Statuszeile, headless als `ERROR: …` auf stderr.
- **Headless Exit-Codes:** `2` für Aufruffehler (falsche Zahl von Argumenten, unbekanntes Flag, kein Integer), `1` für alle Fehler beim Ausführen (`error(…)`, SQL-Fehler, abgelehnte Rückfrage), `0` bei Erfolg.
- `pcall(f, …)` fängt Fehler ab, auch die von Primitiven: `local ok, err = pcall(ctx.query, ctx, sql)`.

- Bei einem Fehler im Skript zeigt das Message-Log unter der Fehlermeldung den **Lua-Stack** („Lua stack:“ mit Datei und Zeile je Aufrufebene) – damit findest du die Stelle im Skript. Ein Skript, das sich nicht laden lässt, erscheint als eine Zeile „user command script skipped: <pfad>: <fehler>“.

### Sandbox

- Lua 5.1 (gopher-lua). Es gibt **keine** Ganzzahldivision `//`, keine Bitoperatoren und kein `goto` – `math.floor(a / b)` verwenden.
- Geladen sind nur die Bibliotheken `base`, `table`, `string` und `math`. **Kein `os`, kein `io`, kein `debug`, kein `package`, kein `coroutine`**; `dofile`, `loadfile`, `require`, `module` und `newproxy` sind entfernt. `load`/`loadstring` bleiben (nur Code aus Strings). Es gibt also weder Dateizugriff (außer über `ctx:export`) noch Uhrzeit oder Umgebungsvariablen – Datum und Zeit liefert die Datenbank (`SELECT NOW()`).
- Jedes Skript hat eine **eigene globale Umgebung**: Globale Variablen eines Skripts sind in anderen nicht sichtbar, Lesezugriffe fallen auf die gemeinsamen Standardfunktionen zurück. Gemeinsamen Code gibt es deshalb nicht (kein `require`); Hilfsfunktionen gehören in die Datei, die sie nutzt.
- Die Tabellen `dbcc` und `dbcc.fmt` sind schreibgeschützt (`dbcc is read-only`).
- Zustand auf oberster Ebene eines Skripts lebt so lange wie die Lua-Laufzeit: in der UI bis `reload` oder Programmende, headless nur für den einen Befehl.
- Laden ist auf 5 Sekunden begrenzt, die `run`-Funktion nicht. `F8` bricht einen laufenden Befehl ab: ein laufendes SQL-Statement serverseitig und den Lua-Code des Skripts, auch eine Endlosschleife in `run`. Der Befehl endet mit „<befehl>: interrupted“, danach funktionieren alle Befehle normal. Wartet das Skript gerade auf die Datenbank, endet zuerst das Statement, dann das Skript. Headless bricht `Ctrl+C` (SIGINT) ebenso ab.

### Beispiele

Alle Beispiele sind vollständig: Datei unter `~/.config/dbcc/commands/` speichern, in der UI `reload` – fertig. Sie wurden mit dbcc gegen MariaDB 11.8 getestet (Beispiel b und c zusätzlich gegen PostgreSQL 17 und SQL Server 2022, e gegen alle vier Dialekte); die Ausgaben sind echt. Die Testdatenbank `dbcc_doc` enthält `logs (id, level, msg, created)`, `logs_archive` mit derselben Struktur, `settings (name, value)` und die View `recent_errors`; das Profil `doc` zeigt auf sie.

#### a) Ein Wert lesen: `setting`

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

Der Name geht als Parameter in die Abfrage – kein Zusammensetzen von SQL aus Benutzereingaben. `#res` ist die Zahl der Zeilen; NULL kommt als `nil` an.

```text
$ dbcc -p doc -c "setting retention_days"
retention_days = 90
$ dbcc -p doc -c "setting owner"
owner = NULL
$ dbcc -p doc -c "setting nope"; echo $?
ERROR: setting: no setting named nope
1
```

#### b) Table-origin-Grid: `latest`

Die neuesten Zeilen einer Tabelle, absteigend nach Primärschlüssel – das Gegenstück zu `head`.

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

- `args = { "object", "int?" }`: Die Vervollständigung bietet Tabellen an, dbcc prüft, dass `n` eine ganze Zahl ist.
- `ctx:resolve(args[1], "table")` akzeptiert `orders`, `shop.orders` oder `/doc/shop/orders`; `obj.sql` ist der gequotete, qualifizierte Name.
- `ctx:on(obj)`: Das Objekt kann auf einer anderen Verbindung liegen – SQL-Helfer und `grid` müssen dann dort laufen.
- `ctx:meta` liefert den Primärschlüssel (auch zusammengesetzte), `on:quote` quotet die Spaltennamen, `on:limit` setzt das Limit dialektgerecht.
- `origin = obj` macht das Ergebnis table-origin: In der UI funktionieren `F4` (Zeilenformular), `w` (freies `WHERE`), Sortieren und Filtern auf dem Server – genau wie bei `head`. Ein Filter baut die Abfrage dabei aus der Tabelle neu; Reihenfolge und Limit von `latest` gelten danach nicht mehr.

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

Der zweite Aufruf liest – vom MariaDB-Profil aus – eine PostgreSQL-Tabelle über ihren Pfad (`LIMIT` angehängt, `"…"`-Quoting); gegen SQL Server (`/msdoc/dbcc_doc/dbo/logs`) entsteht `SELECT TOP (2) …`. Der dritte zeigt die Einschränkung auf Tabellen: `recent_errors` ist eine View. In der UI erscheint ein Tab „latest orders“; `w` und `status = 'paid'` filterten im Test auf dem Server (Statuszeile `134 rows | filter: (status = 'paid')`), `F4` öffnete das Formular.

#### c) Berechnete Zeilen: `counts`

Zeilenzahl aller Tabellen einer Datenbank bzw. eines Schemas, optional nur ab einer Mindestzahl.

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

- `ctx:abspath` macht aus `.`, `..`, relativen und absoluten Pfaden eine Pfadtabelle; `p.database`/`p.schema` zeigen, ob die Ebene stimmt.
- `ctx:objects(p, "table")` liefert nur Tabellen; `on:resolve(pfad)` macht daraus ein Objekt mit gequotetem `sql`.
- Die Zahlen werden mit `tonumber` zu Lua-Zahlen – damit ist die Spalte in `ctx:rows` numerisch (rechtsbündig, numerisch sortierbar).
- Das Flag `-m` ist als `int` deklariert; `-m 3`, `-m=3`, `--m=3` sind gleichwertig.

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

Die Zeile `counts: …` ist eine Meldung (stderr); `2>/dev/null` lässt nur die Daten übrig.

#### d) Schreibender Befehl mit Parametern und Rückfrage

Alte Zeilen von `logs` nach `logs_archive` verschieben; `-n` zählt nur.

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

- `write = true`: in schreibgeschützten Profilen gesperrt, jedes `ctx:exec` wird bestätigt – außer ein `ctx:confirm` wurde bejaht. Die eine Frage zeigt Anzahl, Datum und beide Statements.
- Das Datum wird geprüft und als Parameter übergeben, nie in den SQL-Text eingesetzt.
- `ctx:exec` gibt die betroffenen Zeilen zurück.

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

In der UI erscheint ein Dialog mit diesem Text und „Yes“/„No“. **Ohne** das `ctx:confirm` fragt dbcc vor jedem der beiden Statements einzeln („archive-logs will run on doc:“ plus Statement und darunter „with parameters:“ mit den gebundenen Werten). Im Test wurde das `INSERT` bejaht und das `DELETE` verneint: Der Befehl endete mit „archive-logs: cancelled by user“, die kopierten Zeilen standen danach in beiden Tabellen – deshalb die gemeinsame Frage.

#### e) Dialektabhängig: `server`

Version und Laufzeit des Servers. `ctx.dialect` wählt die Abfrage.

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

- Code auf oberster Ebene (die Tabelle `queries`, die Funktion `duration`) läuft einmal beim Laden; `local` hält ihn privat.
- MariaDB meldet sich als `mariadb`, obwohl das Profil `driver = "mysql"` hat – daher ein eigener Eintrag. MySQL 8 hat `information_schema.GLOBAL_STATUS` nicht mehr, MariaDB nutzt es weiterhin.
- Keine Ganzzahldivision in Lua 5.1: `math.floor(a / b)`.
- Auf SQL Server braucht `sys.dm_os_sys_info` das Recht `VIEW SERVER STATE`.

Getestet gegen alle vier Dialekte:

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

(Bei den letzten drei ist nur die Datenzeile gezeigt.)

#### f) Einen eingebauten Befehl ersetzen: `head` mit 20 Zeilen

```sh
dbcc --export-commands /tmp/dbcc-defaults
cp /tmp/dbcc-defaults/head.lua ~/.config/dbcc/commands/head.lua
```

Dann in der Kopie zwei Stellen ändern (Hilfe und Standardwert):

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

Nach `reload` (oder Neustart) gilt das neue `head` überall, wo `head` aufgerufen wird – in der Kommandozeile und headless:

```text
$ dbcc -p doc -c "help head" | tail -1
source: /home/anna/.config/dbcc/commands/head.lua
$ dbcc -p doc -c "head /doc/shop/orders" | wc -l
22                                   # Kopf + Trennlinie + 20 Zeilen
$ dbcc --no-user-commands -p doc -c "head /doc/shop/orders" | wc -l
12                                   # das eingebaute head: 10 Zeilen
```

Ist das Ersatzskript fehlerhaft, bleibt das eingebaute `head` aktiv (siehe [Laden, Ersetzen, Rückfall](#laden-ersetzen-rückfall)). Datei löschen und `reload` stellt das Original wieder her.

#### g) Interaktion: Auswahlliste und Editor

`tpl` schreibt ein `SELECT` mit allen Spalten einer Tabelle in den Scratch-Tab; ohne Argument wählt man die Tabelle aus einer Liste.

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

- Die Elemente von `ctx:pick` sind Tabellen mit `label` (angezeigt) und eigenem Feld `name`; zurück kommt die gewählte Tabelle selbst. `Esc` liefert `nil`.
- `pcall(ctx.editor_set, ctx, sql)` – die Methode mit `ctx` als erstem Argument – fängt den Headless-Fehler ab.

In der UI (`F9`, `tpl`) erscheint die Liste „tpl: choose a table“ mit `logs  (table)`, `logs_archive  (table)`, `recent_errors  (view)`, `settings  (table)`; Tippen von `sett` filtert, `Enter` schreibt in den Scratch:

```sql
SELECT
  `name`,
  `value`
FROM `dbcc_doc`.`settings`
WHERE 1 = 1;
```

und das Log meldet `tpl: template for settings in the editor`. Headless:

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

Ohne `pcall` scheitert `ctx:editor_set` headless mit `tpl: ctx:editor_set: the editor is not available without UI`.

### Tipps: Testen, Debuggen, Quoting, Sicherheit

- **Testen ohne UI:** `dbcc -p <profil> -c "<befehl> <argumente>"` lädt die Skripte bei jedem Aufruf frisch – Datei speichern, Befehl wiederholen. Ergebnisse kommen auf stdout (`--format csv`/`json` für Tests mit `diff` oder `jq`), Meldungen auf stderr, der Exit-Code zeigt Erfolg (`0`), Ausführungsfehler (`1`) und Aufruffehler (`2`). Schreibende Befehle brauchen `--yes`; `-v` zeigt jedes Statement. Für Tests ein eigenes Profil oder eine Testdatenbank verwenden.
- **Ladefehler finden:** Headless erscheinen sie als `warning: user command script skipped: …` mit Datei, Zeile und Spalte vor jeder Ausgabe; in der UI im Messages-Log nach dem Start bzw. nach `reload`.
- **Debuggen:** `print(...)` und `ctx:message(...)` schreiben ins Messages-Log bzw. auf stderr. Tabellen ausgeben: `print(dbcc.fmt.table(res.columns, res))` zeigt ein ganzes `ctx:query`-Ergebnis. `error("…")` ohne `0` nennt Datei und Zeile.
- **Eingebaute Skripte lesen:** `--export-commands` und die Skripte als Vorlage nehmen – `tail.lua` (Folgen, Schlüsselwahl), `cp.lua` (Rückfrage mit allen Statements), `cat.lua` (Metadaten ausgeben), `history.lua` (`pcall` um `ctx:pick`).
- **Bezeichner quoten:** Objektnamen nie selbst zusammensetzen, sondern `obj.sql` aus `ctx:resolve` verwenden (qualifiziert und gequotet); Spaltennamen mit `ctx:quote` bzw. `ctx:on(obj):quote` – des Dialekts der Verbindung, auf der das SQL läuft.
- **SQL-Injection vermeiden:** Werte immer als Parameter (`?`) übergeben, nie per `..` in den SQL-Text einsetzen. Bezeichner können keine Parameter sein: Sie kommen aus `ctx:resolve`/`ctx:meta` (existieren also) und werden gequotet. Wo ein Argument nur bestimmte Werte annehmen darf, vorher prüfen (`string.match`, eine Liste erlaubter Werte).
- **Verbindungen:** `ctx:on(obj)` für alles, was ein Objekt betrifft; `ctx.dialect` gehört zur aktuellen Verbindung, `ctx:on(obj).dialect` zur Verbindung des Objekts.
- **Zahlen:** `ctx:query` liefert Strings – vor Rechnen oder Vergleichen `tonumber`; für numerische Spalten in `ctx:rows` Lua-Zahlen übergeben.
- **Große Ergebnisse:** Für die Anzeige `ctx:grid` (lädt seitenweise), nicht `ctx:query` + `ctx:rows` (gepuffert und bei `max_rows_buffered` abgeschnitten, `res.capped`).

---

## 18. Datenbankspezifisches

### MySQL und MariaDB

- MariaDB wird beim Verbinden anhand von `VERSION()` erkannt; `ctx.dialect` ist dann `mariadb`.
- Keine Schemaebene; Datenbankwechsel per `USE`.
- Unix-Sockets (`socket`), TLS über `params.tls`. `multiStatements` ist aus – dbcc zerlegt Statements selbst.
- Abbrechen: `KILL QUERY <id>` auf der Hilfsverbindung.
- Warnungen: `SHOW WARNINGS`, wenn `@@warning_count` größer 0 ist.
- DDL committet implizit (CREATE, ALTER, DROP, RENAME, TRUNCATE, GRANT, REVOKE, LOCK, … außer `CREATE TEMPORARY`); in manuellen Transaktionen vermerkt.
- `CALL` kann mehrere Ergebnismengen liefern; jede bekommt einen Tab.
- Replace-Skripte: MariaDB `CREATE OR REPLACE`, MySQL `DROP` + `CREATE` (mit Warnung zu Rechten, siehe [Objekt-Tabs](#objekt-tabs-und-replace-skripte)), jeweils mit `DELIMITER`.
- Baum-Kategorien zusätzlich Triggers und Events; Sequences auf MariaDB.

### PostgreSQL

- Schemaebene; eine Verbindung ist an eine Datenbank gebunden. `cd` in eine andere Datenbank verbindet die Sitzung neu (offene Transaktion: Rückfrage). Der Baum verbindet sich beim Aufklappen einer anderen Datenbank im Hintergrund.
- Schemawechsel setzt `search_path` auf `"schema", public`.
- `du` und `grep` lesen nur den Katalog der aktuellen Datenbank.
- Abbrechen über einen Cancel-Request des Treibers; die Verbindung bleibt nutzbar.
- NOTICE- und WARNING-Meldungen erscheinen als Warnungen im Messages-Log.
- DDL ist transaktional; manuelle Transaktionen mit automatischem `BEGIN` (siehe [Abschnitt 13](#13-transaktionen-und-sicherheit)).
- Statements liefern höchstens eine Ergebnismenge (Prozeduren mit OUT-Parametern eine Zeile).
- Baum: Materialized views, Sequences (ohne die internen Sequenzen von Identity-Spalten), Funktionen und Prozeduren (Überladungen zusammengefasst, Erweiterungsmitglieder ausgelassen), Trigger.
- Verbindung mit Anwendungsname `dbcc`, Standard-Timeout 10 s, ohne clientseitigen Statement-Cache (DDL macht keine Pläne ungültig).

### Microsoft SQL Server

- Schemaebene. Datenbankwechsel per `USE`. T-SQL hat kein umschaltbares Standardschema: `cd` auf ein Schema prüft nur, ob es existiert; dbcc qualifiziert Namen in Befehlen mit dem Schema des Pfads, während unqualifizierte Namen im eigenen SQL gegen das Standardschema des Logins (meist `dbo`) aufgelöst werden.
- Batches mit `GO [n]`; eine Ausführungseinheit ist ein ganzer Batch. Ein Batch, der DML und `SELECT` mischt, meldet keine betroffenen Zeilen.
- Kein serverseitiger Read-only-Modus: `read_only` wirkt über den Client-Schutz und die Rechte des Logins; bei Availability-Group-Listenern kann `params.ApplicationIntent = "ReadOnly"` auf ein lesbares Secondary lenken.
- Manuelle Transaktionen über `SET IMPLICIT_TRANSACTIONS ON`, Zustand über `@@TRANCOUNT`.
- Abbrechen über TDS-Attention (nicht `KILL`, das die Sitzung beenden würde). `kill -q` ist nicht möglich.
- Informationsmeldungen (`PRINT`, `RAISERROR` mit Schweregrad ≤ 10) werden derzeit nicht angezeigt.
- Metadatenabfragen verwenden dreiteilige Namen (`[db].sys.…`) und funktionieren in jeder Datenbank.
- Benannte Instanzen: `host = "server\\instanz"` ohne Port.
- Replace-Skripte verlangen, dass die Sitzung in der Datenbank des Objekts ist (vorher `cd`).
- Hinweis zum Produktnamen: T-SQL-Anweisungen `DBCC …` gehen als SQL an den Server.

---

## 19. Dateien und Verzeichnisse

Es gelten `XDG_CONFIG_HOME` und `XDG_STATE_HOME`, falls gesetzt.

| Pfad | Inhalt |
|---|---|
| `~/.config/dbcc/config.toml` | Einstellungen, Profile, Tastenbelegung (0600 empfohlen; Warnung bei weiteren Rechten) |
| `~/.config/dbcc/credentials.toml` | Passwortspeicher (0600) |
| `~/.config/dbcc/commands/*.lua` | eigene Befehle (geladen beim Start und mit `reload`, siehe [Abschnitt 17](#wo-die-skripte-liegen)) |
| `~/.local/state/dbcc/history.jsonl` | Statement-Historie (JSON Lines: `time`, `profile`, `path`, `duration_ms`, `outcome`, `stmt`) |
| `~/.local/state/dbcc/scratches/<verbindung>/*.sql` | Scratch-Dateien (`_none` = ohne Verbindung) |
| `~/.local/state/dbcc/scratches/open.json` | zuletzt offene Scratch-Tabs mit Cursorposition |
| `~/.local/state/dbcc/hang-<zeit>-<grund>.txt` | Diagnose-Dumps (siehe [Fehlerbehebung](#20-fehlerbehebung)) |
| `~/.my.cnf`, `~/.pgpass` (`$PGPASSFILE`) | gelesen bei `password = "mycnf"` bzw. `"pgpass"` |

---

## 20. Fehlerbehebung

### Tasten kommen nicht an

- **F1, F10, F11** werden von manchen Terminals abgefangen: `Esc` und dann die Ziffer (innerhalb einer Sekunde) oder `Alt`+Ziffer verwenden (`Esc 1` = F1, `Esc 0` = F10).
- **Alt+F1/Alt+F2** sind auf vielen Desktops belegt (Fenstermanager, Startmenü). Stattdessen `Tab`/`Shift+Tab` oder `F9` verwenden oder die Aktionen `focus_tree`/`focus_editor`/`focus_results` in `[keys.global]` umbelegen.
- **Ctrl+Space** wird oft von Eingabemethoden belegt: `Ctrl+O` (`completion_key_alt`) verwenden.
- **Ctrl+Shift+C/X/V** verarbeiten viele Terminals selbst (Kopieren der Terminal-Markierung, Einfügen). Das ist gewollt; Einfügen kommt dann per Bracketed Paste an. Erreichen die Tasten dbcc, wirken sie auf den Editor.
- **Shift+F-Tasten** auf der Linux-Konsole (die sie als F13–F24 senden) werden erkannt.
- `Ctrl+Enter` ist in den meisten Terminals nicht von `Enter` zu unterscheiden und deshalb nicht belegt.
- **Eingabe „hängt“, Tasten werden verschluckt:** Falls ein Terminal je eine C1-Steuersequenz (z. B. `ESC P`) erzeugt, wartet die Eingabe auf deren Ende. `Esc` und dann `\` (der String-Terminator `ESC \`) beendet die Sequenz. dbcc verhindert solche Belegungen (Alt + Großbuchstabe) bereits bei der Konfiguration.

### Terminal und Farben

dbcc braucht keine externen terminfo-Dateien für `TERM=xterm-256color`, `screen-256color`, `tmux-256color` und `linux`. Mit weniger Farben wird auf 256, 16 bzw. monochrom zurückgeschaltet. Kopieren nutzt OSC 52; in tmux dafür `set -g set-clipboard on`.

### Oberfläche reagiert nicht

Ein Watchdog prüft alle 2 Sekunden, ob die Oberfläche antwortet. Bleibt die Antwort 5 Sekunden aus, schreibt dbcc einmal je Hänger die Stacks aller Goroutinen nach `~/.local/state/dbcc/hang-YYYYMMDD-HHMMSS-ui-unresponsive.txt` (mit Angabe, ob der Worker beschäftigt ist und wie viele Aufträge warten). Einen Dump auf Anforderung erzeugt

```sh
pkill -QUIT dbcc
```

(Datei `hang-…-signal.txt`); dbcc läuft dabei weiter. Diese Dateien helfen bei der Fehlersuche.

### Häufige Meldungen

| Meldung | Bedeutung |
|---|---|
| `not connected: cd /<profile> first` | Am Pfad `/` gibt es keine Sitzung; erst verbinden (F2 oder `cd /profil`). |
| `no connection: F2 opens the connections` | SQL im Editor ohne Verbindung. |
| `read-only profile: statement rejected` | Lese-Schutz des Profils. |
| `cancelled by user` | Rückfrage verneint (headless: ohne `--yes`). |
| `confirmation required; run with --yes` | Schreibender Befehl headless ohne `--yes`. |
| `previous pages are not available: …` | Vorige Seite nur bei Lese-Abfragen. |
| `forms need a table-origin result …` | Formular nur für Ergebnisse aus `ll`, `head`, `tail` oder dem Baum. |
| `config file … has permissions …` | `chmod 600 ~/.config/dbcc/config.toml`. |
| `passwords must not be stored in the profile file …` | Klartextpasswort in `config.toml`; Passwortquelle verwenden. |
| `settings.page_size = …: must be at least 10` | `page_size` zu klein. |
| `user command script skipped: …` | Eigenes Lua-Skript fehlerhaft; ein gleichnamiger eingebauter Befehl bleibt aktiv. |

---

## 21. Über dbcc

**dbcc — Database Command & Control**
Ein Produkt der **Datamedrix GmbH**.
Kontakt: Anton Grünberg · a.gruenberg@datamedrix.com · https://www.datamedrix.com

Die About-Seite (F1, dann `F1` oder `Tab`; oder `help about`) zeigt Logo, Version, Build-Datum und Kontaktdaten.

**Lizenzen der Fremdbibliotheken:** `L` auf der About-Seite zeigt die Lizenzhinweise der mitgelieferten Bibliotheken: zuerst eine Übersicht (Modul, Version, Lizenz), danach jeden vollständigen Lizenztext. `Up`/`Down`/`PgUp`/`PgDn`/`Home`/`End` scrollen, `L` oder `Esc` führt zurück zur About-Seite. Ohne UI gibt `dbcc --licenses` denselben Text aus. Der Text wird mit `make licenses` aus dem Modul-Cache erzeugt (`internal/branding/THIRD_PARTY_LICENSES.txt`); ein Test schlägt fehl, wenn sich `go.mod` ändert und die Datei nicht neu erzeugt wurde. Der MySQL-Treiber (`github.com/go-sql-driver/mysql`) steht unter MPL-2.0; dbcc verwendet ihn unverändert, sein Quellcode ist unter https://github.com/go-sql-driver/mysql verfügbar.
