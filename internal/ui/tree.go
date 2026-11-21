package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/meta"
	"dbcc/internal/session"
)

// NodeKind classifies tree nodes.
type NodeKind int

const (
	NodeConn NodeKind = iota
	NodeDatabase
	NodeSchema
	NodeCategory
	NodeObject
	NodeGroup  // Columns, Keys, Foreign keys, Indexes, Triggers below a table
	NodeDetail // a column, key, index … (leaf)
	NodeInfo   // "loading…", errors
)

// NodeRef is the reference stored in every tree node.
type NodeRef struct {
	Kind     NodeKind
	Path     meta.Path       // Object set for objects and their details
	ObjKind  meta.ObjectKind // object kind, or category kind
	Name     string
	System   bool
	loaded   bool
	children []*tview.TreeNode // full child list (before filtering)
}

// categoryOrder and titles of category nodes (spec 7.2).
var categoryOrder = []struct {
	kind  meta.ObjectKind
	title string
}{
	{meta.KindTable, "Tables"},
	{meta.KindView, "Views"},
	{meta.KindMaterializedView, "Materialized views"},
	{meta.KindProcedure, "Procedures"},
	{meta.KindFunction, "Functions"},
	{meta.KindTrigger, "Triggers"},
	{meta.KindEvent, "Events"},
	{meta.KindSequence, "Sequences"},
}

// Tree is the object tree pane.
type Tree struct {
	*tview.TreeView
	ws    *session.Workspace
	async func(func()) // run off the UI goroutine
	queue func(func()) // run on the UI goroutine and redraw
	theme *Theme

	mu     sync.Mutex
	conn   string
	filter string
	// OnError reports load errors (message log).
	OnError func(err error)
}

// NewTree creates the tree for the workspace.
func NewTree(ws *session.Workspace, theme *Theme, async, queue func(func())) *Tree {
	t := &Tree{TreeView: tview.NewTreeView(), ws: ws, async: async, queue: queue, theme: theme}
	t.SetBorder(true)
	t.SetGraphics(false)
	t.SetTopLevel(1)
	t.SetRoot(tview.NewTreeNode("").SetReference(&NodeRef{Kind: NodeConn}))
	t.SetInputCapture(t.input)
	return t
}

// Refresh rebuilds the tree for the current connection (A18: the tree
// shows the current connection only and switches with it).
func (t *Tree) Refresh() {
	s := t.ws.Current()
	name := ""
	if s != nil {
		name = s.Profile.Name
	}
	t.mu.Lock()
	t.conn = name
	t.mu.Unlock()
	if name == "" {
		t.SetTitle(" no connection ")
		root := tview.NewTreeNode("").SetReference(&NodeRef{Kind: NodeConn})
		root.AddChild(info("F2: connections — n creates a new one"))
		t.SetRoot(root)
		t.SetCurrentNode(nil)
		return
	}
	t.SetTitle(" " + name + " ")
	root := tview.NewTreeNode(name).SetReference(&NodeRef{Kind: NodeConn, Path: meta.Path{Conn: name}, Name: name})
	t.SetRoot(root)
	t.expand(root, false)
}

// Selected returns the reference of the selected node.
func (t *Tree) Selected() *NodeRef {
	n := t.GetCurrentNode()
	if n == nil {
		return nil
	}
	ref, _ := n.GetReference().(*NodeRef)
	return ref
}

// Toggle expands or collapses the selected node.
func (t *Tree) Toggle() {
	n := t.GetCurrentNode()
	if n == nil {
		return
	}
	if n.IsExpanded() && len(n.GetChildren()) > 0 {
		n.SetExpanded(false)
		return
	}
	t.expand(n, false)
}

// Reload reloads the selected node (F5).
func (t *Tree) Reload() {
	if n := t.GetCurrentNode(); n != nil {
		if ref, ok := n.GetReference().(*NodeRef); ok && ref.Kind == NodeDetail {
			n = t.parentOf(n)
		}
		if n != nil {
			t.expand(n, true)
		}
	}
}

// SetFilter filters loaded nodes by name (spec 7.2: "/" filters while typing).
func (t *Tree) SetFilter(f string) {
	t.mu.Lock()
	t.filter = strings.ToLower(f)
	t.mu.Unlock()
	t.applyFilter(t.GetRoot())
	if f != "" {
		t.SetTitle(fmt.Sprintf(" %s  /%s ", t.conn, f))
	} else {
		t.SetTitle(" " + t.conn + " ")
	}
}

// Filter returns the active filter.
func (t *Tree) Filter() string { t.mu.Lock(); defer t.mu.Unlock(); return t.filter }

// applyFilter shows the children of loaded nodes that match the filter or
// have a matching descendant; returns whether n itself should stay.
func (t *Tree) applyFilter(n *tview.TreeNode) bool {
	ref, _ := n.GetReference().(*NodeRef)
	if ref == nil {
		return true
	}
	f := t.Filter()
	selfMatch := f == "" || strings.Contains(strings.ToLower(ref.Name), f)
	if !ref.loaded {
		return selfMatch
	}
	var keep []*tview.TreeNode
	for _, c := range ref.children {
		if t.applyFilter(c) || selfMatch && ref.Kind >= NodeObject {
			keep = append(keep, c)
		}
	}
	n.SetChildren(keep)
	if f != "" && len(keep) > 0 {
		n.SetExpanded(true)
	}
	return selfMatch || len(keep) > 0 || ref.Kind == NodeConn
}

// SelectPath moves the selection to the node of path, loading as needed
// (spec 7.2: cd in the command line moves the tree selection).
func (t *Tree) SelectPath(p meta.Path) {
	if p.Conn != t.conn {
		t.Refresh()
	}
	want := []string{p.Database, p.Schema}
	var walk func(n *tview.TreeNode, depth int)
	walk = func(n *tview.TreeNode, depth int) {
		if depth >= len(want) || want[depth] == "" {
			t.selectVisible(n)
			return
		}
		ref, _ := n.GetReference().(*NodeRef)
		next := func() {
			for _, c := range n.GetChildren() {
				if cr, _ := c.GetReference().(*NodeRef); cr != nil && cr.Name == want[depth] {
					c.SetExpanded(true)
					walk(c, depth+1)
					return
				}
			}
			t.selectVisible(n)
		}
		if ref != nil && !ref.loaded {
			t.load(n, next)
			return
		}
		next()
	}
	walk(t.GetRoot(), 0)
}

// selectVisible selects n, or the first child when n is the hidden root
// (SetTopLevel(1) hides the connection node).
func (t *Tree) selectVisible(n *tview.TreeNode) {
	if n == t.GetRoot() {
		if cs := n.GetChildren(); len(cs) > 0 {
			t.SetCurrentNode(cs[0])
		}
		return
	}
	t.SetCurrentNode(n)
}

func (t *Tree) parentOf(target *tview.TreeNode) *tview.TreeNode {
	var found *tview.TreeNode
	t.GetRoot().Walk(func(n, parent *tview.TreeNode) bool {
		if n == target {
			found = parent
			return false
		}
		return true
	})
	return found
}

func info(text string) *tview.TreeNode {
	return tview.NewTreeNode(text).SetReference(&NodeRef{Kind: NodeInfo, Name: text}).SetSelectable(false).
		SetColor(tcell.ColorGray)
}

// expand loads (or reloads) the children of n and expands it.
func (t *Tree) expand(n *tview.TreeNode, reload bool) {
	ref, _ := n.GetReference().(*NodeRef)
	if ref == nil || ref.Kind == NodeDetail || ref.Kind == NodeInfo {
		return
	}
	if ref.loaded && !reload {
		n.SetExpanded(true)
		return
	}
	t.load(n, nil)
}

// load fetches the children of n on the auxiliary connection.
func (t *Tree) load(n *tview.TreeNode, then func()) {
	ref := n.GetReference().(*NodeRef)
	s := t.ws.Sessions()[ref.Path.Conn]
	if s == nil {
		return
	}
	n.SetChildren([]*tview.TreeNode{info("loading…")})
	n.SetExpanded(true)
	t.async(func() {
		children, err := t.children(context.Background(), s, ref)
		t.queue(func() {
			if err != nil {
				n.SetChildren([]*tview.TreeNode{info("error: " + oneLine(err.Error()))})
				if t.OnError != nil {
					t.OnError(err)
				}
				return
			}
			ref.loaded = true
			ref.children = children
			n.SetChildren(children)
			if len(children) == 0 {
				n.SetChildren([]*tview.TreeNode{info("(empty)")})
			}
			if t.Filter() != "" {
				t.applyFilter(n)
			}
			if cur := t.GetCurrentNode(); (cur == nil || cur == t.GetRoot()) && n == t.GetRoot() {
				t.selectVisible(n)
			}
			if then != nil {
				then()
			}
		})
	})
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// children computes the child nodes of ref (runs off the UI goroutine).
func (t *Tree) children(ctx context.Context, s *session.Session, ref *NodeRef) ([]*tview.TreeNode, error) {
	d := s.Dialect
	// PostgreSQL needs a connection to the database being expanded; the
	// session opens it in the background (spec 7.2).
	aux, err := s.AuxFor(ctx, ref.Path.Database)
	if err != nil {
		return nil, err
	}
	switch ref.Kind {
	case NodeConn:
		dbs, err := d.ListDatabases(ctx, aux)
		if err != nil {
			return nil, err
		}
		var out []*tview.TreeNode
		for _, db := range dbs {
			p := ref.Path
			p.Database = db.Name
			node := tview.NewTreeNode(db.Name).SetReference(&NodeRef{Kind: NodeDatabase, Path: p, Name: db.Name, System: db.System}).
				SetExpanded(false)
			if db.System {
				node.SetColor(t.theme.Dim)
			} else {
				node.SetColor(t.theme.Text)
			}
			out = append(out, node)
		}
		return out, nil
	case NodeDatabase:
		if d.HasSchemas() {
			// PostgreSQL: expanding another database connects to it in the
			// background — handled by the dialect through the aux session.
			schemas, err := d.ListSchemas(ctx, aux, ref.Path.Database)
			if err != nil {
				return nil, err
			}
			var out []*tview.TreeNode
			for _, sc := range schemas {
				p := ref.Path
				p.Schema = sc.Name
				node := tview.NewTreeNode(sc.Name).SetReference(&NodeRef{Kind: NodeSchema, Path: p, Name: sc.Name, System: sc.System})
				if sc.System {
					node.SetColor(t.theme.Dim)
				} else {
					node.SetColor(t.theme.Text)
				}
				out = append(out, node)
			}
			return out, nil
		}
		return t.categories(ctx, s, ref)
	case NodeSchema:
		return t.categories(ctx, s, ref)
	case NodeCategory:
		return ref.children, nil
	case NodeObject:
		if ref.ObjKind != meta.KindTable && ref.ObjKind != meta.KindView && ref.ObjKind != meta.KindMaterializedView {
			return nil, nil
		}
		tbl, err := d.DescribeTable(ctx, aux, ref.Path)
		if err != nil {
			return nil, err
		}
		return t.tableChildren(ref, tbl), nil
	}
	return nil, nil
}

func (t *Tree) categories(ctx context.Context, s *session.Session, ref *NodeRef) ([]*tview.TreeNode, error) {
	aux, err := s.AuxFor(ctx, ref.Path.Database)
	if err != nil {
		return nil, err
	}
	objs, err := s.Dialect.ListObjects(ctx, aux, ref.Path, nil)
	if err != nil {
		return nil, err
	}
	byKind := map[meta.ObjectKind][]meta.Object{}
	for _, o := range objs {
		byKind[o.Kind] = append(byKind[o.Kind], o)
	}
	var out []*tview.TreeNode
	for _, c := range categoryOrder {
		list := byKind[c.kind]
		if len(list) == 0 {
			continue // empty categories are hidden
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		cref := &NodeRef{Kind: NodeCategory, Path: ref.Path, ObjKind: c.kind, Name: c.title, loaded: true}
		node := tview.NewTreeNode(fmt.Sprintf("%s (%d)", c.title, len(list))).SetReference(cref).SetExpanded(false).
			SetColor(t.theme.Accent)
		for _, o := range list {
			p := ref.Path
			p.Object = o.Name
			child := tview.NewTreeNode(o.Name).SetReference(&NodeRef{Kind: NodeObject, Path: p, ObjKind: o.Kind, Name: o.Name}).
				SetColor(t.theme.Text)
			cref.children = append(cref.children, child)
		}
		node.SetChildren(cref.children)
		out = append(out, node)
	}
	return out, nil
}

func (t *Tree) tableChildren(ref *NodeRef, tbl *meta.Table) []*tview.TreeNode {
	detail := func(text string) *tview.TreeNode {
		return tview.NewTreeNode(text).SetReference(&NodeRef{Kind: NodeDetail, Path: ref.Path, ObjKind: ref.ObjKind, Name: text}).
			SetColor(t.theme.Text)
	}
	group := func(title string, items []*tview.TreeNode) *tview.TreeNode {
		g := tview.NewTreeNode(title).SetReference(&NodeRef{Kind: NodeGroup, Path: ref.Path, ObjKind: ref.ObjKind, Name: title, loaded: true, children: items}).
			SetExpanded(title == "Columns").SetColor(t.theme.Accent)
		g.SetChildren(items)
		return g
	}
	fkCols := map[string]bool{}
	for _, fk := range tbl.ForeignKeys {
		for _, c := range fk.Columns {
			fkCols[strings.ToLower(c)] = true
		}
	}
	pkCols := map[string]bool{}
	if tbl.PrimaryKey != nil {
		for _, c := range tbl.PrimaryKey.Columns {
			pkCols[strings.ToLower(c)] = true
		}
	}
	nameW, typeW := 0, 0
	for _, c := range tbl.Columns {
		nameW = max(nameW, len([]rune(c.Name)))
		typeW = max(typeW, len([]rune(c.DataType)))
	}
	var cols []*tview.TreeNode
	for _, c := range tbl.Columns {
		var marks []string
		if pkCols[strings.ToLower(c.Name)] {
			marks = append(marks, "PK")
		}
		if fkCols[strings.ToLower(c.Name)] {
			marks = append(marks, "FK")
		}
		if !c.Nullable {
			marks = append(marks, "not null")
		}
		if c.AutoIncrement {
			marks = append(marks, "auto")
		}
		if c.Generated != nil {
			marks = append(marks, "generated")
		}
		cols = append(cols, detail(fmt.Sprintf("%-*s  %-*s  %s", nameW, c.Name, typeW, c.DataType, strings.Join(marks, " "))))
	}
	out := []*tview.TreeNode{group("Columns", cols)}
	if tbl.Kind != meta.KindTable {
		return out
	}
	var keys []*tview.TreeNode
	if tbl.PrimaryKey != nil {
		keys = append(keys, detail(fmt.Sprintf("PRIMARY (%s)", strings.Join(tbl.PrimaryKey.Columns, ", "))))
	}
	for _, k := range tbl.UniqueKeys {
		keys = append(keys, detail(fmt.Sprintf("%s UNIQUE (%s)", k.Name, strings.Join(k.Columns, ", "))))
	}
	var fks []*tview.TreeNode
	for _, fk := range tbl.ForeignKeys {
		target := fk.RefTable.Object
		if fk.RefTable.Database != "" && fk.RefTable.Database != tbl.Path.Database {
			target = fk.RefTable.Database + "." + target
		}
		fks = append(fks, detail(fmt.Sprintf("%s (%s) → %s (%s)", fk.Name, strings.Join(fk.Columns, ", "), target, strings.Join(fk.RefColumns, ", "))))
	}
	var idx []*tview.TreeNode
	for _, ix := range tbl.Indexes {
		var cs []string
		for _, c := range ix.Columns {
			s := c.Name
			if c.SubPart > 0 {
				s += fmt.Sprintf("(%d)", c.SubPart)
			}
			if c.Descending {
				s += " DESC"
			}
			cs = append(cs, s)
		}
		u := ""
		if ix.Unique {
			u = " UNIQUE"
		}
		idx = append(idx, detail(fmt.Sprintf("%s%s (%s) %s", ix.Name, u, strings.Join(cs, ", "), ix.Type)))
	}
	var trg []*tview.TreeNode
	for _, tr := range tbl.Triggers {
		trg = append(trg, detail(fmt.Sprintf("%s %s %s", tr.Name, tr.Timing, tr.Event)))
	}
	for _, g := range []struct {
		title string
		items []*tview.TreeNode
	}{{"Keys", keys}, {"Foreign keys", fks}, {"Indexes", idx}, {"Triggers", trg}} {
		if len(g.items) > 0 {
			out = append(out, group(g.title, g.items))
		}
	}
	return out
}

// input handles Right/Left as expand/collapse or go to parent.
func (t *Tree) input(ev *tcell.EventKey) *tcell.EventKey {
	n := t.GetCurrentNode()
	if n == nil {
		return ev
	}
	switch ev.Key() {
	case tcell.KeyHome:
		// The connection node is hidden (SetTopLevel(1)); tview's Home
		// would select it, so select the first visible node instead.
		if cs := t.GetRoot().GetChildren(); len(cs) > 0 {
			t.SetCurrentNode(cs[0])
		}
		return nil
	case tcell.KeyRight:
		if !n.IsExpanded() || len(n.GetChildren()) == 0 {
			t.expand(n, false)
		} else if cs := n.GetChildren(); len(cs) > 0 && cs[0].GetReference().(*NodeRef).Kind != NodeInfo {
			t.SetCurrentNode(cs[0])
		}
		return nil
	case tcell.KeyLeft:
		if n.IsExpanded() && len(n.GetChildren()) > 0 {
			n.SetExpanded(false)
		} else if p := t.parentOf(n); p != nil && p != t.GetRoot() {
			t.SetCurrentNode(p)
		}
		return nil
	}
	return ev
}

// QualifiedName renders the absolute object path used to invoke commands
// (/conn/db[/schema]/object); it does not change the current path.
func QualifiedName(p meta.Path) string {
	parts := []string{p.Conn, p.Database}
	if p.Schema != "" {
		parts = append(parts, p.Schema)
	}
	parts = append(parts, p.Object)
	return QuoteArg("/" + strings.Join(parts, "/"))
}

// QuoteArg quotes a command argument for the command-line word splitter
// when it contains spaces, quotes or backslashes.
func QuoteArg(s string) string {
	if !strings.ContainsAny(s, " \t'\"\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
