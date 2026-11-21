package ui

// Scratch files (owner request 2026-11-21, modelled on DataGrip): created
// with a timestamp in the name, saved automatically, closable only when
// saved, re-openable with Alt+O.

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/exec"
	"dbcc/internal/ui/editor"
)

// defaultAutosaveDelay is the pause after the last edit before scratches
// are saved (Options.AutosaveDelay overrides it).
const defaultAutosaveDelay = time.Second

// newScratch creates a scratch file and focuses its tab (UI goroutine).
func (a *App) newScratch() *editor.Tab {
	tab, err := a.editors.NewScratch(time.Now(), a.ws.Path().Conn)
	if err != nil {
		a.lastErr = "new scratch: " + err.Error()
		a.log.Add(exec.Message{Err: fmt.Errorf("new scratch: %w", err)})
		return nil
	}
	a.tv.SetFocus(a.editors)
	return tab
}

// ensureScratch keeps at least one editor tab open.
func (a *App) ensureScratch() {
	if a.editors.Len() == 0 {
		if _, err := a.editors.NewScratch(time.Now(), a.ws.Path().Conn); err != nil {
			a.log.Add(exec.Message{Err: fmt.Errorf("new scratch: %w", err)})
		}
	}
}

// scheduleAutosave saves dirty scratches autosaveDelay after the last
// change (editor OnChange, UI goroutine).
func (a *App) scheduleAutosave() {
	// Saving itself reports a change; only unsaved scratches need a timer
	// (otherwise every save would schedule the next one).
	if !a.editors.ScratchesDirty() {
		return
	}
	delay := a.opts.AutosaveDelay
	if delay <= 0 {
		delay = defaultAutosaveDelay
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autosave != nil {
		a.autosave.Stop()
	}
	a.autosave = time.AfterFunc(delay, func() {
		select {
		case <-a.quit:
			return
		default:
		}
		a.queue(func() {
			if err := a.editors.SaveScratches(); err != nil {
				a.lastErr = "autosave: " + oneLine(err.Error())
			}
		})
	})
}

// showScratches lists all scratch files (newest first) with a filter:
// Enter opens, d deletes, Esc closes.
func (a *App) showScratches() {
	files, err := a.editors.ScratchFiles()
	if err != nil {
		a.lastErr = err.Error()
		return
	}
	prev := a.tv.GetFocus()
	list := tview.NewList().ShowSecondaryText(true).SetHighlightFullLine(true).SetSecondaryTextColor(tcell.ColorGray)
	filter := tview.NewInputField().SetLabel("Search: ").SetFieldBackgroundColor(tcell.ColorDefault).
		SetPlaceholder("filter by name or content").SetPlaceholderTextColor(tcell.ColorGray)
	var shown []editor.ScratchInfo
	fill := func(q string) {
		list.Clear()
		shown = shown[:0]
		q = strings.ToLower(q)
		for _, f := range files {
			if q != "" && !strings.Contains(strings.ToLower(f.Conn+" "+f.Name+" "+f.Preview), q) {
				continue
			}
			shown = append(shown, f)
			state := ""
			if f.Open {
				state = "  [green]● open[-]"
			}
			conn := f.Conn
			if conn == editor.NoConnection {
				conn = "no connection"
			}
			title := fmt.Sprintf("[::b]%s[::-]  [teal]%s[-]  [gray]changed %s[-]%s", tview.Escape(strings.TrimSuffix(f.Name, ".sql")),
				tview.Escape(conn), f.Modified.Format("2006-01-02 15:04"), state)
			preview := f.Preview
			if preview == "" {
				preview = "(empty)"
			}
			list.AddItem(title, "  "+tview.Escape(exec.Shorten(preview, 90)), 0, nil)
		}
	}
	fill("")
	hint := tview.NewTextView().SetDynamicColors(true).SetText(
		"[yellow]Enter[-] open  [yellow]Alt+R[-] rename  [yellow]Alt+D[-] delete  [yellow]Esc[-] close   [gray]" + tview.Escape(a.editors.ScratchDir()) + "[-]")
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(filter, 1, 0, true).AddItem(list, 0, 1, false).AddItem(hint, 1, 0, false)
	box.SetBorder(true).SetTitle(fmt.Sprintf(" Scratches (%d) ", len(files))).SetBorderColor(a.theme.BorderFocus).
		SetBorderPadding(0, 0, 1, 1)
	filter.SetChangedFunc(fill)
	filter.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		i := list.GetCurrentItem()
		switch {
		case ev.Key() == tcell.KeyEscape:
			a.closeModal("scratches", prev)
			return nil
		case ev.Key() == tcell.KeyUp || ev.Key() == tcell.KeyDown || ev.Key() == tcell.KeyPgUp || ev.Key() == tcell.KeyPgDn:
			list.InputHandler()(ev, nil)
			return nil
		case ev.Key() == tcell.KeyEnter && i >= 0 && i < len(shown):
			conn, name := shown[i].Conn, shown[i].Name
			a.closeModal("scratches", a.editors)
			if _, err := a.editors.OpenScratch(conn, name); err != nil {
				a.lastErr = err.Error()
			}
			a.tv.SetFocus(a.editors)
			return nil
		case ev.Key() == tcell.KeyRune && ev.Rune() == 'd' && ev.Modifiers()&tcell.ModAlt != 0 && i >= 0 && i < len(shown):
			conn, name := shown[i].Conn, shown[i].Name
			a.confirmUI("Delete the scratch "+name+"? This cannot be undone.", func() {
				if err := a.editors.DeleteScratch(conn, name); err != nil {
					a.lastErr = err.Error()
				}
				a.ensureScratch()
				a.closeModal("scratches", prev)
				a.showScratches()
			})
			return nil
		case ev.Key() == tcell.KeyRune && ev.Rune() == 'r' && ev.Modifiers()&tcell.ModAlt != 0 && i >= 0 && i < len(shown):
			f := shown[i]
			a.closeModal("scratches", prev)
			a.renameScratch(f.Conn, f.Name, func() { a.showScratches() })
			return nil
		}
		return ev
	})
	_, _, w, h := a.pages.GetRect()
	a.modal("scratches", box, min(max(w-16, 60), 120), min(max(len(files)*2+5, 10), max(h-4, 10)))
	a.tv.SetFocus(filter)
}

// renameScratch asks for a new name (unique per connection) and renames
// the scratch file; then runs after (UI goroutine).
func (a *App) renameScratch(conn, file string, after func()) {
	prev := a.tv.GetFocus()
	in := tview.NewInputField().SetLabel("New name: ").SetText(strings.TrimSuffix(file, ".sql")).
		SetFieldBackgroundColor(tcell.ColorDefault)
	msg := tview.NewTextView().SetDynamicColors(true).
		SetText("[gray]unique per connection · Enter renames · Esc cancels[-]")
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(in, 1, 0, true).AddItem(msg, 1, 0, false)
	box.SetBorder(true).SetTitle(" Rename scratch ").SetBorderColor(a.theme.BorderFocus).SetBorderPadding(0, 0, 1, 1)
	in.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEscape:
			a.closeModal("rename", prev)
		case tcell.KeyEnter:
			if err := a.editors.RenameScratch(conn, file, in.GetText()); err != nil {
				msg.SetText("[red::b]" + tview.Escape(err.Error()) + "[-::-]")
				return // the dialog stays open
			}
			a.closeModal("rename", prev)
			if after != nil {
				after()
			}
		}
	})
	a.modal("rename", box, 70, 4)
	a.tv.SetFocus(in)
}
