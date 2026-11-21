package editor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// ScratchIndexFile is the name of the index file in the state directory.
const ScratchIndexFile = "scratch.json"

var scratchFileRe = regexp.MustCompile(`^scratch-[0-9]+\.sql$`)

type scratchIndex struct {
	Version int            `json:"version"`
	Current int            `json:"current"` // index into Tabs, -1 if no scratch tab was current
	Tabs    []scratchEntry `json:"tabs"`
}

type scratchEntry struct {
	File  string `json:"file"`
	Title string `json:"title"`
	Line  int    `json:"line"` // 1-based cursor line
	Col   int    `json:"col"`  // byte column
}

// SaveScratch writes all scratch tabs to dir as scratch-1.sql, … plus the
// index file scratch.json (titles, order, cursor positions, current tab).
// Stale scratch files are removed. Saved tabs are marked clean.
func (t *Tabs) SaveScratch(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	idx := scratchIndex{Version: 1, Current: -1}
	keep := map[string]bool{}
	var saved []*Tab
	for i, tab := range t.tabs {
		if tab.Kind != KindScratch {
			continue
		}
		name := fmt.Sprintf("scratch-%d.sql", len(idx.Tabs)+1)
		if err := writeAtomic(filepath.Join(dir, name), []byte(tab.Editor.Text())); err != nil {
			return err
		}
		keep[name] = true
		line, col := tab.Editor.Cursor()
		if i == t.cur {
			idx.Current = len(idx.Tabs)
		}
		idx.Tabs = append(idx.Tabs, scratchEntry{File: name, Title: tab.Title, Line: line, Col: col})
		saved = append(saved, tab)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, ScratchIndexFile), data); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, de := range entries {
			if scratchFileRe.MatchString(de.Name()) && !keep[de.Name()] {
				_ = os.Remove(filepath.Join(dir, de.Name()))
			}
		}
	}
	for _, tab := range saved {
		tab.Editor.cleanText = tab.Editor.Text()
		tab.Editor.dirtyVer = -1
	}
	t.changed()
	return nil
}

// LoadScratch restores the scratch tabs saved by SaveScratch. They are
// inserted before the existing tabs; existing scratch tabs that are empty
// and clean are dropped. A missing index is not an error (nothing is
// loaded). The saved current tab becomes current.
func (t *Tabs) LoadScratch(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, ScratchIndexFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var idx scratchIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return fmt.Errorf("%s: %w", ScratchIndexFile, err)
	}
	var loaded []*Tab
	for _, ent := range idx.Tabs {
		if !scratchFileRe.MatchString(ent.File) {
			continue // never follow paths from the index
		}
		text, err := os.ReadFile(filepath.Join(dir, ent.File))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		title := ent.Title
		if title == "" {
			title = "scratch"
		}
		e := t.newEditor(string(text))
		e.SetCursor(ent.Line, ent.Col)
		loaded = append(loaded, &Tab{Title: title, Kind: KindScratch, Editor: e})
	}
	if len(loaded) == 0 {
		return nil
	}
	var rest []*Tab
	for _, tab := range t.tabs {
		if tab.Kind == KindScratch && tab.Editor.Text() == "" && !tab.Dirty() {
			tab.Editor.tabsHook = nil
			continue
		}
		rest = append(rest, tab)
	}
	t.tabs = append(loaded, rest...)
	t.cur = 0
	if idx.Current >= 0 && idx.Current < len(loaded) {
		t.cur = idx.Current
	}
	t.changed()
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
