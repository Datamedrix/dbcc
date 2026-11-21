package complete

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"dbcc/internal/command"
	"dbcc/internal/meta"
	"dbcc/internal/script"
)

// CommandLineTimeout bounds metadata loads during command-line completion.
var CommandLineTimeout = 3 * time.Second

// commandSource is the part of *command.Registry CommandLine needs.
type commandSource interface {
	Complete(prefix string) []string
	Lookup(name string) (*script.Command, bool)
}

// CommandLine returns completed command lines for line (spec 9.2): the
// first word completes to command names; later words by the argument type
// the command declares (object, path, profile, file; int and string have
// no candidates), with declared flags taken into account (script.Command.
// Expect): a word starting with "-" completes to flag names while flags
// are open, the word after a value flag to the flag's declared values or
// by its value type, positionals after the flags by their type; "--"
// ends the flags. A line whose first word is not a command is SQL and is
// completed like the editor. Each result is the whole new line, ready for
// tview.InputField's autocomplete list. comp may be nil (commands only).
func CommandLine(reg *command.Registry, comp *Completer, line string) []string {
	if reg == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), CommandLineTimeout)
	defer cancel()
	return commandLine(ctx, reg, comp, line)
}

func commandLine(ctx context.Context, reg commandSource, comp *Completer, line string) []string {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	lead := line[:len(line)-len(trimmed)]
	if !strings.ContainsFunc(trimmed, unicode.IsSpace) {
		var out []string
		for _, n := range reg.Complete(trimmed) {
			out = append(out, lead+n)
		}
		return out
	}
	name := command.FirstWord(trimmed)
	cmd, ok := reg.Lookup(name)
	if !ok {
		// SQL typed on the command line.
		if comp == nil {
			return nil
		}
		res := comp.Complete(ctx, line, len(line))
		var out []string
		for _, it := range res.Items {
			out = append(out, line[:res.Start]+it.Text)
		}
		return out
	}
	start := lastWordStart(line)
	word := unquoteWord(line[start:])
	before, _ := splitLoose(line[:start])
	var prior []string
	if len(before) > 1 {
		prior = before[1:]
	}
	e := cmd.Expect(prior)
	var cands []string
	switch {
	case e.FlagAllowed && strings.HasPrefix(word, "-"):
		if name, val, ok := strings.Cut(strings.TrimLeft(word, "-"), "="); ok {
			// -t=value
			if f, found := cmd.Flag(name); found && f.TakesValue() {
				dashes := word[:len(word)-len(strings.TrimLeft(word, "-"))]
				for _, c := range flagValues(ctx, comp, f, val) {
					cands = append(cands, dashes+name+"="+c)
				}
			}
			break
		}
		cands = flagNames(cmd.Flags, word)
	case e.FlagValue != "":
		if f, found := cmd.Flag(e.FlagValue); found {
			cands = flagValues(ctx, comp, f, word)
		}
	default:
		cands = typedArgs(ctx, comp, e.Type, word)
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, line[:start]+quoteArg(c))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// typedArgs returns the candidates for a word of type t (object, path,
// profile, file; others have none).
func typedArgs(ctx context.Context, comp *Completer, t script.ArgType, word string) []string {
	if comp == nil {
		return nil
	}
	switch t {
	case script.ArgObject:
		return comp.objectArgs(ctx, word)
	case script.ArgPath:
		return comp.pathArgs(ctx, word)
	case script.ArgProfile:
		if comp.Profiles != nil {
			return filterPrefix(comp.Profiles(), word)
		}
	case script.ArgFile:
		return comp.fileArgs(word)
	}
	return nil
}

// flagValues completes the value of flag f: its declared values when it
// has some, otherwise by its value type.
func flagValues(ctx context.Context, comp *Completer, f script.FlagSpec, word string) []string {
	if len(f.Values) > 0 {
		return filterPrefix(f.Values, word)
	}
	return typedArgs(ctx, comp, script.ArgType(f.Type), word)
}

// flagNames completes a word starting with "-" to the declared flag
// names, keeping the number of dashes typed ("-" or "--").
func flagNames(flags []script.FlagSpec, word string) []string {
	dashes := "-"
	if strings.HasPrefix(word, "--") {
		dashes = "--"
	}
	prefix := strings.TrimPrefix(word, dashes)
	var out []string
	for _, f := range flags {
		if strings.HasPrefix(f.Name, prefix) {
			out = append(out, dashes+f.Name)
		}
	}
	return out
}

// lastWordStart returns the byte offset where the last (possibly empty)
// word of line starts, honouring command.Split quoting.
func lastWordStart(line string) int {
	start := 0
	var q rune
	esc := false
	for i, r := range line {
		switch {
		case esc:
			esc = false
		case q != 0:
			if r == '\\' && q == '"' {
				esc = true
			} else if r == q {
				q = 0
			}
		case r == '\\':
			esc = true
		case r == '\'' || r == '"':
			q = r
		case unicode.IsSpace(r):
			start = i + len(string(r))
		}
	}
	return start
}

// splitLoose splits like command.Split but tolerates an unterminated
// quote at the end.
func splitLoose(s string) ([]string, error) {
	w, err := command.Split(s)
	if err == nil {
		return w, nil
	}
	for _, q := range []string{`"`, `'`} {
		if w, err2 := command.Split(s + q); err2 == nil {
			return w, nil
		}
	}
	return nil, err
}

// unquoteWord returns the value of a partially typed word.
func unquoteWord(s string) string {
	w, err := splitLoose(s)
	if err != nil || len(w) == 0 {
		return ""
	}
	return w[0]
}

func filterPrefix(names []string, prefix string) []string {
	var out []string
	for _, n := range names {
		if hasPrefixFold(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// resolvePath resolves a path argument prefix (the part up to and
// including the last "/") against the current path like
// session.Workspace.Resolve: "/", "/conn", "/conn/db", "..", relative names.
func (c *Completer) resolvePath(dir string) []string {
	var parts []string
	if !strings.HasPrefix(dir, "/") {
		for _, s := range []string{c.Path.Conn, c.Path.Database, c.Path.Schema} {
			if s == "" {
				break
			}
			parts = append(parts, s)
		}
	}
	for _, seg := range strings.Split(dir, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, seg)
		}
	}
	return parts
}

func (c *Completer) hasSchemasOn(conn string) bool {
	if conn == c.Path.Conn && c.Dialect != nil {
		return c.Dialect.HasSchemas()
	}
	if c.DialectOf != nil {
		if d := c.DialectOf(conn); d != nil {
			return d.HasSchemas()
		}
	}
	return false
}

// children lists the entries below parts: profiles at the root, databases
// of a connection, schemas of a database (dialects with schemas), objects
// of a database or schema. container reports for each name whether it has
// children of its own (for objects: false).
func (c *Completer) children(ctx context.Context, parts []string, withObjects bool) (names []string, container bool) {
	switch len(parts) {
	case 0:
		if c.Profiles != nil {
			return c.Profiles(), true
		}
		return nil, true
	case 1:
		if c.Cache == nil {
			return nil, true
		}
		dbs, _ := c.Cache.Databases(ctx, parts[0])
		return objNames(dbs, nil), true
	}
	conn := parts[0]
	schemas := c.hasSchemasOn(conn)
	if c.Cache == nil {
		return nil, false
	}
	if len(parts) == 2 && schemas {
		s, _ := c.Cache.Schemas(ctx, conn, parts[1])
		return objNames(s, nil), true
	}
	if !withObjects || len(parts) > 3 || (len(parts) == 3 && !schemas) {
		return nil, false
	}
	at := meta.Path{Conn: conn, Database: parts[1]}
	if len(parts) == 3 {
		at.Schema = parts[2]
	}
	objs, _ := c.Cache.Objects(ctx, at)
	return objNames(objs, func(k meta.ObjectKind) bool { return k != meta.KindTrigger }), false
}

func objNames(objs []meta.Object, keep func(meta.ObjectKind) bool) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		if keep == nil || keep(o.Kind) {
			out = append(out, o.Name)
		}
	}
	return out
}

// pathArgs completes navigation paths: /conn, /conn/db, /conn/db/schema,
// relative names and "..".
func (c *Completer) pathArgs(ctx context.Context, word string) []string {
	dir, base := "", word
	if i := strings.LastIndex(word, "/"); i >= 0 {
		dir, base = word[:i+1], word[i+1:]
	}
	parts := c.resolvePath(dir)
	names, _ := c.children(ctx, parts, false)
	out := filterPrefix(names, base)
	for i := range out {
		out[i] = dir + out[i]
	}
	if dir == "" && strings.HasPrefix("..", base) && len(parts) > 0 {
		out = append(out, "..")
	}
	return out
}

// objectArgs completes object arguments: names of the current path,
// "db.name" / "schema.name" qualified names and absolute or relative
// /conn/db[/schema]/object paths (containers get a trailing "/").
func (c *Completer) objectArgs(ctx context.Context, word string) []string {
	if i := strings.LastIndex(word, "/"); i >= 0 {
		dir, base := word[:i+1], word[i+1:]
		parts := c.resolvePath(dir)
		names, container := c.children(ctx, parts, true)
		out := filterPrefix(names, base)
		for k := range out {
			out[k] = dir + out[k]
			if container {
				out[k] += "/"
			}
		}
		return out
	}
	if i := strings.LastIndex(word, "."); i > 0 {
		q, base := word[:i], word[i+1:]
		parts := []string{c.Path.Conn}
		if c.hasSchemasOn(c.Path.Conn) {
			parts = append(parts, c.Path.Database, q)
		} else {
			parts = append(parts, q)
		}
		if c.Path.Conn == "" || parts[1] == "" {
			return nil
		}
		names, _ := c.children(ctx, parts, true)
		out := filterPrefix(names, base)
		for k := range out {
			out[k] = q + "." + out[k]
		}
		return out
	}
	parts := c.resolvePath("")
	names, container := c.children(ctx, parts, true)
	out := filterPrefix(names, word)
	if container {
		// Above the object level (at "/" or /conn) names are containers.
		for k := range out {
			out[k] += "/"
		}
	}
	return out
}

// fileArgs completes file names relative to Dir (directories get "/").
func (c *Completer) fileArgs(word string) []string {
	dir, base := "", word
	if i := strings.LastIndex(word, "/"); i >= 0 {
		dir, base = word[:i+1], word[i+1:]
	}
	root := c.Dir
	if root == "" {
		root = "."
	}
	full := dir
	if !filepath.IsAbs(dir) {
		full = filepath.Join(root, dir)
	}
	if full == "" {
		full = "."
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(n, base) {
			continue
		}
		if e.IsDir() {
			n += "/"
		}
		out = append(out, dir+n)
	}
	sort.Strings(out)
	return out
}
