package editor

// Scratch files, modelled on DataGrip's scratches (owner request
// 2026-11-21): every scratch tab is a file named after its creation time,
// saved automatically, closable only when saved, and re-openable later.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ScratchIndexFile records which scratch files were open (session state).
const ScratchIndexFile = "open.json"

const scratchTimeLayout = "2006-01-02_15-04-05"

var (
	scratchNameRe = regexp.MustCompile(`^scratch_(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})(-\d+)?\.sql$`)
	legacyNameRe  = regexp.MustCompile(`^scratch-[0-9]+\.sql$`)
	// userNameRe: names given by the owner (file name without .sql).
	userNameRe = regexp.MustCompile(`^[^/\\\x00-\x1f.][^/\\\x00-\x1f]{0,99}$`)
	// connDirRe: profile names (config.ValidProfileName) or NoConnection.
	connDirRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]{0,63}|_none)$`)
)

// NoConnection is the folder of scratches created without a connection.
const NoConnection = "_none"

// validScratchFile reports whether name is a scratch file name we created
// or one the owner chose (never a path).
func validScratchFile(name string) bool {
	if !strings.HasSuffix(name, ".sql") {
		return false
	}
	return scratchNameRe.MatchString(name) || userNameRe.MatchString(strings.TrimSuffix(name, ".sql"))
}

func connDir(conn string) string {
	if conn == "" {
		return NoConnection
	}
	return conn
}

type scratchIndex struct {
	Version int            `json:"version"`
	Current string         `json:"current"` // "conn/file" of the current tab ("" = none)
	Open    []scratchEntry `json:"open"`
}

type scratchEntry struct {
	Conn string `json:"conn"`
	File string `json:"file"`
	Line int    `json:"line"` // 1-based cursor line
	Col  int    `json:"col"`  // byte column
}

// ScratchInfo describes a scratch file for the open dialog.
type ScratchInfo struct {
	Conn     string    // connection folder (NoConnection for none)
	Name     string    // file name
	Created  time.Time // from the name
	Modified time.Time
	Preview  string // first non-empty line
	Open     bool   // has a tab
}

// SetScratchDir sets the directory of scratch files.
func (t *Tabs) SetScratchDir(dir string) { t.scratchDir = dir }

// ScratchDir returns the directory of scratch files.
func (t *Tabs) ScratchDir() string { return t.scratchDir }

// scratchTitle renders the tab title of a scratch file.
func scratchTitle(name string) string {
	m := scratchNameRe.FindStringSubmatch(name)
	if m == nil {
		return strings.TrimSuffix(name, ".sql") // a name the owner chose
	}
	ts, err := time.ParseInLocation(scratchTimeLayout, m[1], time.Local)
	if err != nil {
		return strings.TrimSuffix(name, ".sql")
	}
	return "scratch " + ts.Format("2006-01-02 15:04") + m[2]
}

// NewScratch creates a scratch file of connection conn ("" = none) named
// after now and opens it in a new tab. The file exists immediately
// (empty), so it can always be re-opened.
func (t *Tabs) NewScratch(now time.Time, conn string) (*Tab, error) {
	if t.scratchDir == "" {
		return nil, errors.New("no scratch directory")
	}
	conn = connDir(conn)
	if !connDirRe.MatchString(conn) {
		return nil, fmt.Errorf("invalid connection name %q", conn)
	}
	dir := filepath.Join(t.scratchDir, conn)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	base := "scratch_" + now.Format(scratchTimeLayout)
	name := base + ".sql"
	for n := 2; ; n++ {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		name = fmt.Sprintf("%s-%d.sql", base, n)
	}
	return t.add(&Tab{Title: scratchTitle(name), Kind: KindScratch, Conn: conn, File: name, Editor: t.newEditor("")}), nil
}

// scratchPath is the file of a scratch tab.
func (t *Tabs) scratchPath(conn, file string) string {
	return filepath.Join(t.scratchDir, connDir(conn), file)
}

// SaveTab writes a dirty scratch tab to its file and marks it clean.
// Object tabs are not saved here (Run all replaces the object).
func (t *Tabs) SaveTab(tab *Tab) error {
	if tab == nil || tab.Kind != KindScratch || !tab.Dirty() {
		return nil
	}
	if tab.File == "" {
		return errors.New("scratch tab has no file")
	}
	text := tab.Editor.Text()
	if err := writeAtomic(t.scratchPath(tab.Conn, tab.File), []byte(text)); err != nil {
		return fmt.Errorf("save %s: %w", tab.File, err)
	}
	tab.Editor.markClean(text)
	t.changed()
	return nil
}

// ScratchesDirty reports whether any scratch tab has unsaved changes.
func (t *Tabs) ScratchesDirty() bool {
	for _, tab := range t.tabs {
		if tab.Kind == KindScratch && tab.Dirty() {
			return true
		}
	}
	return false
}

// SaveScratches saves every dirty scratch tab; the first error is returned
// after trying all of them.
func (t *Tabs) SaveScratches() error {
	var first error
	for _, tab := range t.tabs {
		if err := t.SaveTab(tab); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CloseScratch saves and closes scratch tab i. If saving fails the tab
// stays open and the error is returned: a changed scratch is never lost.
func (t *Tabs) CloseScratch(i int) error {
	if i < 0 || i >= len(t.tabs) || t.tabs[i].Kind != KindScratch {
		return errors.New("not a scratch tab")
	}
	if err := t.SaveTab(t.tabs[i]); err != nil {
		return err
	}
	t.Close(i)
	return nil
}

// ScratchFiles lists the scratch files of all connections, newest
// (last changed) first.
func (t *Tabs) ScratchFiles() ([]ScratchInfo, error) {
	conns, err := os.ReadDir(t.scratchDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	open := map[string]bool{}
	for _, tab := range t.tabs {
		if tab.Kind == KindScratch {
			open[connDir(tab.Conn)+"/"+tab.File] = true
		}
	}
	var out []ScratchInfo
	for _, cd := range conns {
		if !cd.IsDir() || !connDirRe.MatchString(cd.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(t.scratchDir, cd.Name()))
		if err != nil {
			continue
		}
		for _, de := range entries {
			if de.IsDir() || !validScratchFile(de.Name()) {
				continue
			}
			info := ScratchInfo{Conn: cd.Name(), Name: de.Name(), Open: open[cd.Name()+"/"+de.Name()]}
			if m := scratchNameRe.FindStringSubmatch(de.Name()); m != nil {
				info.Created, _ = time.ParseInLocation(scratchTimeLayout, m[1], time.Local)
			}
			if fi, err := de.Info(); err == nil {
				info.Modified = fi.ModTime()
				if info.Created.IsZero() {
					info.Created = fi.ModTime()
				}
			}
			info.Preview = firstLine(filepath.Join(t.scratchDir, cd.Name(), de.Name()))
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Modified.Equal(out[j].Modified) {
			return out[i].Modified.After(out[j].Modified)
		}
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return scratchSeq(out[i].Name) > scratchSeq(out[j].Name)
	})
	return out, nil
}

// RenameScratch renames a scratch file of conn to newName (".sql" is
// added). Names are unique per connection, case-insensitively. An open
// tab follows the rename.
func (t *Tabs) RenameScratch(conn, file, newName string) error {
	conn = connDir(conn)
	newName = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(newName), ".sql"))
	if !userNameRe.MatchString(newName) {
		return fmt.Errorf("invalid name %q: 1–100 characters, no '/', '\\', control characters or leading '.'", newName)
	}
	if !connDirRe.MatchString(conn) || !validScratchFile(file) {
		return fmt.Errorf("not a scratch file: %s/%s", conn, file)
	}
	target := newName + ".sql"
	if target == file {
		return nil
	}
	dir := filepath.Join(t.scratchDir, conn)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, de := range entries {
		if strings.EqualFold(de.Name(), target) && de.Name() != file {
			return fmt.Errorf("a scratch named %q already exists for %s", newName, conn)
		}
	}
	// Save pending edits first, so nothing is lost by the rename.
	for _, tab := range t.tabs {
		if tab.Kind == KindScratch && connDir(tab.Conn) == conn && tab.File == file {
			if err := t.SaveTab(tab); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(filepath.Join(dir, file), filepath.Join(dir, target)); err != nil {
		return err
	}
	for _, tab := range t.tabs {
		if tab.Kind == KindScratch && connDir(tab.Conn) == conn && tab.File == file {
			tab.File, tab.Title = target, scratchTitle(target)
		}
	}
	t.changed()
	return nil
}

// scratchSeq is the "-N" suffix of a scratch name created in the same
// second as another (1 when there is none).
func scratchSeq(name string) int {
	m := scratchNameRe.FindStringSubmatch(name)
	n := 1
	if m != nil && m[2] != "" {
		fmt.Sscanf(m[2], "-%d", &n)
	}
	return n
}

func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			return l
		}
	}
	return ""
}

// OpenScratch focuses the tab of a scratch file or opens it in a new tab.
func (t *Tabs) OpenScratch(conn, name string) (*Tab, error) {
	conn = connDir(conn)
	if !connDirRe.MatchString(conn) || !validScratchFile(name) {
		return nil, fmt.Errorf("not a scratch file: %q", name)
	}
	for i, tab := range t.tabs {
		if tab.Kind == KindScratch && connDir(tab.Conn) == conn && tab.File == name {
			t.SetCurrent(i)
			return tab, nil
		}
	}
	text, err := os.ReadFile(t.scratchPath(conn, name))
	if err != nil {
		return nil, err
	}
	return t.add(&Tab{Title: scratchTitle(name), Kind: KindScratch, Conn: conn, File: name, Editor: t.newEditor(string(text))}), nil
}

// DeleteScratch removes a scratch file; an open tab of it is closed first.
func (t *Tabs) DeleteScratch(conn, name string) error {
	conn = connDir(conn)
	if !connDirRe.MatchString(conn) || !validScratchFile(name) {
		return fmt.Errorf("not a scratch file: %q", name)
	}
	for i, tab := range t.tabs {
		if tab.Kind == KindScratch && connDir(tab.Conn) == conn && tab.File == name {
			t.Close(i)
			break
		}
	}
	err := os.Remove(t.scratchPath(conn, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// SaveSession saves all scratch tabs and records which were open (order,
// cursor, current tab) so the next start restores them.
func (t *Tabs) SaveSession() error {
	if err := t.SaveScratches(); err != nil {
		return err
	}
	idx := scratchIndex{Version: 2}
	for i, tab := range t.tabs {
		if tab.Kind != KindScratch {
			continue
		}
		line, col := tab.Editor.Cursor()
		idx.Open = append(idx.Open, scratchEntry{Conn: connDir(tab.Conn), File: tab.File, Line: line, Col: col})
		if i == t.cur {
			idx.Current = connDir(tab.Conn) + "/" + tab.File
		}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(t.scratchDir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(t.scratchDir, ScratchIndexFile), data)
}

// LoadSession re-opens the scratch tabs that were open at the last exit.
// Legacy files (scratch-N.sql + scratch.json in legacyDir) are migrated
// first: renamed to timestamped scratch files and opened.
func (t *Tabs) LoadSession(legacyDir string) error {
	if err := os.MkdirAll(t.scratchDir, 0o700); err != nil {
		return err
	}
	migrated := t.migrateLegacy(legacyDir)
	data, err := os.ReadFile(filepath.Join(t.scratchDir, ScratchIndexFile))
	var idx scratchIndex
	if err == nil {
		if err := json.Unmarshal(data, &idx); err != nil {
			return fmt.Errorf("%s: %w", ScratchIndexFile, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, name := range migrated {
		idx.Open = append(idx.Open, scratchEntry{Conn: NoConnection, File: name, Line: 1})
	}
	cur := -1
	for _, ent := range idx.Open {
		tab, err := t.OpenScratch(ent.Conn, ent.File) // validates both (never follows paths)
		if err != nil {
			continue // deleted meanwhile
		}
		tab.Editor.SetCursor(ent.Line, ent.Col)
		if connDir(ent.Conn)+"/"+ent.File == idx.Current {
			cur = t.Index(tab)
		}
	}
	if cur >= 0 {
		t.SetCurrent(cur)
	}
	return nil
}

// migrateLegacy renames scratch-N.sql files of the previous scheme to
// timestamped scratch files (by modification time) and returns their names.
func (t *Tabs) migrateLegacy(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	target := filepath.Join(t.scratchDir, NoConnection)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return nil
	}
	for _, de := range entries {
		if !legacyNameRe.MatchString(de.Name()) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			os.Remove(filepath.Join(dir, de.Name()))
			continue
		}
		base := "scratch_" + fi.ModTime().Format(scratchTimeLayout)
		name := base + ".sql"
		for n := 2; fileExists(filepath.Join(target, name)); n++ {
			name = fmt.Sprintf("%s-%d.sql", base, n)
		}
		if err := writeAtomic(filepath.Join(target, name), data); err != nil {
			continue
		}
		os.Remove(filepath.Join(dir, de.Name()))
		out = append(out, name)
	}
	os.Remove(filepath.Join(dir, "scratch.json"))
	return out
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".scratch-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
