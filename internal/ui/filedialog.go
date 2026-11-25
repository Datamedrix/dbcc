package ui

// The file dialog of ctx:save_file (A28): directory tree on the left, the
// file-name input top right, the entries of the shown directory below.
// The listing logic is kept in small pure functions (tested without a
// screen); showFileDialog only wires them to tview. The same dialog
// serves ctx:open_file in open mode (A29).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/config"
	"dbcc/internal/script"
)

// fileEntry is one line of the file list.
type fileEntry struct {
	Name  string
	Dir   bool
	Size  int64
	Mod   time.Time
	Match bool // a directory, or a file with one of the dialog's extensions
}

func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

// hasExtension reports whether name ends in one of exts (without dot,
// compared case-insensitively). Without exts every name matches.
func hasExtension(name string, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	low := strings.ToLower(name)
	for _, e := range exts {
		if strings.HasSuffix(low, "."+strings.ToLower(e)) {
			return true
		}
	}
	return false
}

// withExtension appends the first of exts to a file name that has no
// extension ("customers" -> "customers.csv"; ".hidden" has none either).
func withExtension(path string, exts []string) string {
	if len(exts) == 0 || exts[0] == "" {
		return path
	}
	base := filepath.Base(path)
	if strings.Contains(strings.TrimLeft(base, "."), ".") {
		return path
	}
	return path + "." + exts[0]
}

// sortEntries orders the file list: directories first, then files, each
// case-insensitively by name.
func sortEntries(es []fileEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Dir != es[j].Dir {
			return es[i].Dir
		}
		a, b := strings.ToLower(es[i].Name), strings.ToLower(es[j].Name)
		if a != b {
			return a < b
		}
		return es[i].Name < es[j].Name
	})
}

// listDir returns the entries of dir for the file list (without "../"),
// sorted by sortEntries; dot entries only with hidden. Symbolic links to
// directories count as directories.
func listDir(dir string, hidden bool, exts []string) ([]fileEntry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		if !hidden && isHidden(e.Name()) {
			continue
		}
		fe := fileEntry{Name: e.Name(), Dir: e.IsDir()}
		info, ierr := e.Info()
		if e.Type()&fs.ModeSymlink != 0 {
			if st, serr := os.Stat(filepath.Join(dir, e.Name())); serr == nil {
				info, ierr = st, nil
				fe.Dir = st.IsDir()
			}
		}
		if ierr == nil {
			fe.Size, fe.Mod = info.Size(), info.ModTime()
		}
		fe.Match = fe.Dir || hasExtension(fe.Name, exts)
		out = append(out, fe)
	}
	sortEntries(out)
	return out, nil
}

// subdirs lists the directory names of dir for the tree.
func subdirs(dir string, hidden bool) ([]string, error) {
	es, err := listDir(dir, hidden, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if e.Dir {
			out = append(out, e.Name)
		}
	}
	return out, nil
}

// expandTilde expands "~" and "~/…" to the home directory.
func expandTilde(p string) string {
	if p == "~" {
		return config.HomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(config.HomeDir(), p[2:])
	}
	return p
}

// resolveInput interprets the text typed into the file-name input: an
// absolute path, ~/…, or a name or path relative to the shown directory
// dir. It returns the cleaned absolute path and whether it is an existing
// directory.
func resolveInput(text, dir string) (string, bool) {
	p := expandTilde(strings.TrimSpace(text))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	return p, err == nil && st.IsDir()
}

// checkSaveTarget verifies that the parent directory of path exists.
func checkSaveTarget(path string) error {
	parent := filepath.Dir(path)
	st, err := os.Stat(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("the directory %s does not exist", parent)
		}
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", parent)
	}
	return nil
}

// checkOpenTarget verifies that path is an existing file (open mode).
func checkOpenTarget(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s does not exist", filepath.Base(path))
		}
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", filepath.Base(path))
	}
	return nil
}

// startDir picks the directory the dialog opens in: dir if it exists, else
// last (the directory of the last chosen file), else the working directory.
func startDir(dir, last string) string {
	for _, d := range []string{dir, last} {
		if d == "" {
			continue
		}
		if abs, err := filepath.Abs(expandTilde(d)); err == nil {
			if st, err := os.Stat(abs); err == nil && st.IsDir() {
				return abs
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/"
}

// dirNode is the reference of a tree node: a directory, its children
// loaded on demand.
type dirNode struct {
	path   string
	loaded bool
}

// fileDialog is the state of one open file dialog (UI goroutine only).
type fileDialog struct {
	a       *App
	opts    script.SaveFileOptions
	dir     string // the directory shown on the right
	hidden  bool
	entries []fileEntry // shown in the list after "../"
	name    string      // the last file name typed or suggested

	tree     *tview.TreeView
	input    *tview.InputField
	where    *tailLine
	list     *tview.Table
	status   *tview.TextView
	hint     string
	openMode bool // open mode (A29)
	done     func(path string, ok bool)
}

const fileDialogHint = "[yellow]Tab[-] next pane  [yellow]Enter[-] open/save  [yellow]Esc[-] cancel  [yellow]Alt+H[-] hidden files"

const fileOpenHint = "[yellow]Tab[-] next pane  [yellow]Enter[-] open  [yellow]Esc[-] cancel  [yellow]Alt+H[-] hidden files"

// showFileDialog opens the file dialog; done receives the chosen path or
// ok = false. open selects the open mode (ctx:open_file, A29): only an
// existing file is returned, Enter on a file in the list returns it, and
// no extension is appended. It runs on the UI goroutine.
func (a *App) showFileDialog(opts script.SaveFileOptions, open bool, done func(path string, ok bool)) {
	prev := a.tv.GetFocus()
	if open {
		opts.Name = ""
	}
	d := &fileDialog{a: a, opts: opts, name: opts.Name, openMode: open}
	d.dir = startDir(opts.Dir, a.lastSaveDir)
	finish := func(path string, ok bool) {
		a.closeModal("savefile", prev)
		if ok {
			a.lastSaveDir = filepath.Dir(path)
		}
		done(path, ok)
	}
	d.done = finish

	root := tview.NewTreeNode("/").SetReference(&dirNode{path: "/"})
	d.tree = tview.NewTreeView().SetRoot(root).SetCurrentNode(root)
	d.tree.SetSelectedFunc(func(n *tview.TreeNode) {
		if ref, ok := n.GetReference().(*dirNode); ok {
			d.load(n)
			n.SetExpanded(!n.IsExpanded() || len(n.GetChildren()) == 0)
			d.show(ref.path, false)
		}
	})
	d.tree.SetChangedFunc(func(n *tview.TreeNode) {
		if ref, ok := n.GetReference().(*dirNode); ok {
			d.show(ref.path, false)
		}
	})
	d.input = tview.NewInputField().SetText(opts.Name).SetFieldBackgroundColor(tcell.ColorDefault)
	d.where = &tailLine{Box: tview.NewBox(), color: a.theme.Dim}
	d.list = tview.NewTable().SetSelectable(true, false)
	d.list.SetSelectedFunc(func(row, _ int) { d.open(row) })
	d.hint = fileDialogHint
	if open {
		d.hint = fileOpenHint
	}
	d.status = tview.NewTextView().SetDynamicColors(true).SetText(d.hint)

	// Three framed areas; the one with the focus gets the focus colour
	// (focusFrames sets it at draw time, so it follows Tab, Enter and the
	// mouse alike). tview also draws a focused frame with double lines,
	// which tells them apart on monochrome terminals.
	d.tree.SetBorder(true).SetTitle(" Folders ").SetTitleAlign(tview.AlignLeft)
	d.input.SetBorder(true).SetTitle(" File name ").SetTitleAlign(tview.AlignLeft)
	files := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(d.where, 1, 0, false).AddItem(d.list, 0, 1, true)
	files.SetBorder(true).SetTitle(" Files ").SetTitleAlign(tview.AlignLeft)
	right := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(d.input, 3, 0, true).AddItem(files, 0, 1, false)
	body := tview.NewFlex().AddItem(d.tree, 0, 1, false).AddItem(right, 0, 2, true)
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(body, 0, 1, true).AddItem(d.status, 1, 0, false)
	title := opts.Title
	if title == "" {
		title = "Save to"
		if open {
			title = "Open file"
		}
	}
	// The outer frame keeps the normal colour so that only the active
	// area stands out; its title names the dialog.
	box.SetBorder(true).SetTitle(" " + title + " ").SetBorderColor(a.theme.Border).SetTitleColor(a.theme.Title)
	frames := &focusFrames{Primitive: box, focus: a.theme.BorderFocus, normal: a.theme.Border,
		frames: []*tview.Box{d.tree.Box, d.input.Box, files.Box},
		owners: []tview.Primitive{d.tree, d.input, files}}

	order := []tview.Primitive{d.input, d.list, d.tree}
	keys := func(self int) func(ev *tcell.EventKey) *tcell.EventKey {
		return func(ev *tcell.EventKey) *tcell.EventKey {
			switch {
			case ev.Key() == tcell.KeyTab:
				a.tv.SetFocus(order[(self+1)%len(order)])
				return nil
			case ev.Key() == tcell.KeyBacktab:
				a.tv.SetFocus(order[(self+len(order)-1)%len(order)])
				return nil
			case ev.Key() == tcell.KeyEscape:
				finish("", false)
				return nil
			case ev.Key() == tcell.KeyRune && ev.Modifiers()&tcell.ModAlt != 0 && (ev.Rune() == 'h' || ev.Rune() == 'H'):
				d.hidden = !d.hidden
				d.reloadTree()
				d.show(d.dir, true)
				return nil
			case self == 0 && ev.Key() == tcell.KeyEnter:
				d.submit()
				return nil
			}
			return ev
		}
	}
	d.input.SetInputCapture(keys(0))
	d.list.SetInputCapture(keys(1))
	d.tree.SetInputCapture(keys(2))

	d.show(d.dir, true)
	_, _, w, h := a.pages.GetRect()
	a.modal("savefile", frames, max(w*4/5, 60), max(h*4/5, 16))
	a.tv.SetFocus(d.input)
}

// load reads the subdirectories of a tree node once.
func (d *fileDialog) load(n *tview.TreeNode) {
	ref, ok := n.GetReference().(*dirNode)
	if !ok || ref.loaded {
		return
	}
	ref.loaded = true
	n.ClearChildren()
	names, err := subdirs(ref.path, d.hidden)
	if err != nil {
		// An unreadable directory shows the error instead of failing.
		n.AddChild(tview.NewTreeNode(oneLine(err.Error())).SetColor(d.a.theme.Dim).SetSelectable(false))
		return
	}
	for _, name := range names {
		n.AddChild(tview.NewTreeNode(name).SetReference(&dirNode{path: filepath.Join(ref.path, name)}).SetExpanded(false))
	}
}

// reloadTree rebuilds the tree (after the hidden toggle) with dir selected.
func (d *fileDialog) reloadTree() {
	root := tview.NewTreeNode("/").SetReference(&dirNode{path: "/"})
	d.tree.SetRoot(root).SetCurrentNode(root)
	d.selectInTree(d.dir)
}

// selectInTree expands the tree down to dir and selects it. A component
// that is not listed (a hidden directory while hidden entries are off) is
// added so that the path can still be shown.
func (d *fileDialog) selectInTree(dir string) {
	n := d.tree.GetRoot()
	cur := "/"
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" {
			continue
		}
		d.load(n)
		n.SetExpanded(true)
		next := filepath.Join(cur, part)
		var found *tview.TreeNode
		for _, c := range n.GetChildren() {
			if ref, ok := c.GetReference().(*dirNode); ok && ref.path == next {
				found = c
				break
			}
		}
		if found == nil {
			found = tview.NewTreeNode(part).SetReference(&dirNode{path: next})
			n.AddChild(found)
		}
		n, cur = found, next
	}
	d.load(n)
	n.SetExpanded(true)
	d.tree.SetCurrentNode(n)
}

// show lists dir on the right; syncTree also moves the tree selection.
func (d *fileDialog) show(dir string, syncTree bool) {
	d.dir = dir
	d.status.SetText(d.hint)
	d.where.text = "in " + dir
	if syncTree {
		d.selectInTree(dir)
	}
	d.list.Clear()
	d.entries = nil
	es, err := listDir(dir, d.hidden, d.opts.Extensions)
	d.list.SetCell(0, 0, tview.NewTableCell("../").SetExpansion(1))
	if err != nil {
		d.list.SetCell(1, 0, tview.NewTableCell(tview.Escape(oneLine(err.Error()))).SetTextColor(d.a.theme.Error).SetSelectable(false))
		d.list.Select(0, 0)
		return
	}
	d.entries = es
	for i, e := range es {
		row := i + 1
		color := tview.Styles.PrimaryTextColor
		if !e.Match {
			color = d.a.theme.Dim
		}
		if e.Dir {
			d.list.SetCell(row, 0, tview.NewTableCell(tview.Escape(e.Name+"/")).SetExpansion(1).SetTextColor(color))
			d.list.SetCell(row, 1, tview.NewTableCell(""))
			d.list.SetCell(row, 2, tview.NewTableCell(""))
			continue
		}
		d.list.SetCell(row, 0, tview.NewTableCell(tview.Escape(e.Name)).SetExpansion(1).SetTextColor(color))
		d.list.SetCell(row, 1, tview.NewTableCell(script.FormatSize(float64(e.Size))).SetAlign(tview.AlignRight).SetTextColor(color))
		date := ""
		if !e.Mod.IsZero() {
			date = e.Mod.Format("2006-01-02")
		}
		d.list.SetCell(row, 2, tview.NewTableCell(" "+date).SetTextColor(color))
	}
	d.list.Select(0, 0)
	d.list.ScrollToBeginning()
}

// open acts on Enter in the file list: "../" and directories are entered,
// a file's name goes into the input.
func (d *fileDialog) open(row int) {
	if row == 0 {
		d.show(filepath.Dir(d.dir), true)
		return
	}
	if row-1 >= len(d.entries) {
		return
	}
	e := d.entries[row-1]
	if e.Dir {
		d.show(filepath.Join(d.dir, e.Name), true)
		return
	}
	if d.openMode {
		d.done(filepath.Join(d.dir, e.Name), true)
		return
	}
	d.name = e.Name
	d.input.SetText(e.Name)
	d.a.tv.SetFocus(d.input)
}

// submit acts on Enter in the file-name input: an existing directory is
// entered, anything else is the result (with the default extension).
func (d *fileDialog) submit() {
	text := strings.TrimSpace(d.input.GetText())
	if text == "" {
		d.fail(errors.New("enter a file name"))
		return
	}
	p, isDir := resolveInput(text, d.dir)
	if isDir {
		d.input.SetText(d.name)
		d.show(p, true)
		return
	}
	if d.openMode {
		if err := checkOpenTarget(p); err != nil {
			d.fail(err)
			return
		}
		d.done(p, true)
		return
	}
	p = withExtension(p, d.opts.Extensions)
	if err := checkSaveTarget(p); err != nil {
		d.fail(err)
		return
	}
	d.done(p, true)
}

func (d *fileDialog) fail(err error) {
	d.status.SetText("[red::b]" + tview.Escape(oneLine(err.Error())) + "[-::-]")
}

// tailLine shows one line of text; when it is too wide its end stays
// visible ("…/deep/directory"), which matters for paths.
type tailLine struct {
	*tview.Box
	text  string
	color tcell.Color
}

func (t *tailLine) Draw(screen tcell.Screen) {
	t.Box.DrawForSubclass(screen, t)
	x, y, w, _ := t.GetInnerRect()
	r := []rune(t.text)
	if len(r) > w && w > 1 {
		r = append([]rune("…"), r[len(r)-w+1:]...)
	}
	tview.Print(screen, tview.Escape(string(r)), x, y, w, tview.AlignLeft, t.color)
}

// focusFrames colours the frames of a dialog's areas by focus each time it
// is drawn: the frame whose owner has the focus gets the focus colour.
type focusFrames struct {
	tview.Primitive
	focus, normal tcell.Color
	frames        []*tview.Box
	owners        []tview.Primitive
}

func (f *focusFrames) Draw(screen tcell.Screen) {
	for i, b := range f.frames {
		if f.owners[i].HasFocus() {
			b.SetBorderColor(f.focus)
		} else {
			b.SetBorderColor(f.normal)
		}
	}
	f.Primitive.Draw(screen)
}
