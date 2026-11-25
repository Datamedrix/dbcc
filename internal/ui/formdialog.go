package ui

// The generic settings dialog of ctx:form (A28): one form item per
// declared option (string/int -> input field, bool -> checkbox, choice ->
// drop-down), the option's help as a dim line below it.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/script"
)

var intInputRE = regexp.MustCompile(`^-?[0-9]*$`)

// acceptInt lets an int field take digits and a leading "-" only.
func acceptInt(text string, _ rune) bool { return intInputRE.MatchString(text) }

// showForm opens the settings dialog; done receives the values (name ->
// string, int64 or bool) or ok = false. It runs on the UI goroutine.
func (a *App) showForm(title string, fields []script.OptionSpec, values map[string]any, done func(map[string]any, bool)) {
	prev := a.tv.GetFocus()
	f := tview.NewForm()
	f.SetItemPadding(0).SetButtonsAlign(tview.AlignLeft).SetBorderPadding(0, 0, 0, 0)
	f.SetFieldBackgroundColor(tcell.ColorDefault).SetLabelColor(a.theme.Accent)
	_, _, sw, sh := a.pages.GetRect()
	labelW := 0
	for _, fd := range fields {
		labelW = max(labelW, len([]rune(fd.Label)))
	}
	width := min(max(60, labelW+44), max(sw-4, 40))
	fieldW := max(width-labelW-6, 10)
	lines := 0
	get := make([]func() (any, error), len(fields))
	for i, fd := range fields {
		fd := fd
		v := values[fd.Name]
		if v == nil {
			v = fd.Default
		}
		switch fd.Type {
		case script.OptBool:
			b, _ := v.(bool)
			cb := tview.NewCheckbox().SetLabel(fd.Label).SetChecked(b)
			f.AddFormItem(cb)
			get[i] = func() (any, error) { return cb.IsChecked(), nil }
		case script.OptChoice:
			labels := make([]string, len(fd.Choices))
			cur := 0
			for k, c := range fd.Choices {
				labels[k] = c.Label
				if s, _ := v.(string); s == c.Value {
					cur = k
				}
			}
			dd := tview.NewDropDown().SetLabel(fd.Label).SetOptions(labels, nil).SetCurrentOption(cur)
			f.AddFormItem(dd)
			get[i] = func() (any, error) {
				k, _ := dd.GetCurrentOption()
				return fd.Choices[max(k, 0)].Value, nil
			}
		case script.OptInt:
			n, _ := v.(int64)
			in := tview.NewInputField().SetLabel(fd.Label).SetText(strconv.FormatInt(n, 10)).
				SetFieldWidth(min(fieldW, 20)).SetAcceptanceFunc(acceptInt)
			f.AddFormItem(in)
			get[i] = func() (any, error) {
				n, err := strconv.ParseInt(strings.TrimSpace(in.GetText()), 10, 64)
				if err != nil {
					return nil, fmt.Errorf("%s: enter an integer", fd.Label)
				}
				return n, nil
			}
		default:
			s, _ := v.(string)
			in := tview.NewInputField().SetLabel(fd.Label).SetText(s).SetFieldWidth(fieldW)
			f.AddFormItem(in)
			get[i] = func() (any, error) { return in.GetText(), nil }
		}
		lines++
		if fd.Help != "" {
			// A non-scrollable text view passes the focus on: Tab skips it.
			help := tview.NewTextView().SetWrap(false).SetScrollable(false).SetDynamicColors(true).SetText("[gray]" + tview.Escape(fd.Help) + "[-]")
			help.SetSize(1, fieldW)
			f.AddFormItem(help)
			lines++
		}
	}
	status := tview.NewTextView().SetDynamicColors(true).
		SetText("[gray]Tab moves · Enter opens a drop-down · F2/OK accepts · Esc cancels[-]")
	finished := false
	finish := func(v map[string]any, ok bool) {
		if finished {
			return
		}
		finished = true
		a.closeModal("form", prev)
		done(v, ok)
	}
	accept := func() {
		out := make(map[string]any, len(fields))
		for i, fd := range fields {
			v, err := get[i]()
			if err != nil {
				status.SetText("[red::b]" + tview.Escape(err.Error()) + "[-::-]")
				return
			}
			out[fd.Name] = v
		}
		finish(out, true)
	}
	cancel := func() { finish(nil, false) }
	f.AddButton("OK", accept)
	f.AddButton("Cancel", cancel)
	f.SetCancelFunc(cancel)
	f.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyF2 {
			accept()
			return nil
		}
		return ev
	})
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(f, 0, 1, true).AddItem(status, 1, 0, false)
	box.SetBorder(true).SetTitle(" "+title+" ").SetBorderColor(a.theme.BorderFocus).SetBorderPadding(0, 0, 1, 1)
	// items, a blank line, the buttons, the status line and the frame
	a.modal("form", box, width, min(lines+5, max(sh-2, 10)))
	a.tv.SetFocus(f)
}
