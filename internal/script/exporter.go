package script

// Export formats as Lua plugins (spec A28): dbcc.exporter, dbcc.exporters,
// ctx:export_with and the generic dialogs ctx:form and ctx:save_file. The
// flow of the export command lives in export.lua; Go only offers these
// primitives.

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/exec"
)

// OptionType is the declared type of an exporter option or form field.
type OptionType string

// Option types of dbcc.exporter options and ctx:form fields.
const (
	OptString OptionType = "string"
	OptInt    OptionType = "int"
	OptBool   OptionType = "bool"
	OptChoice OptionType = "choice"
)

var optionTypes = map[OptionType]bool{OptString: true, OptInt: true, OptBool: true, OptChoice: true}

// Choice is one value of a choice option.
type Choice struct {
	Value string
	Label string
}

// OptionSpec is one declared option (dbcc.exporter{options = {...}}) or
// form field (ctx:form). The same declaration drives the settings dialog
// and the validation of -o name=value; a future dbcc.importer uses it too.
type OptionSpec struct {
	Name  string
	Label string
	Help  string
	Type  OptionType
	// Default is a string (string, choice), an int64 (int) or a bool (bool).
	Default any
	// Choices of a choice option, in declaration order.
	Choices []Choice
}

// Format is the declaration shared by export formats (dbcc.exporter) and
// import formats (dbcc.importer, A29).
type Format struct {
	Name  string
	Title string
	Help  string
	// Extensions without dot; the first one is the default extension.
	Extensions []string
	Options    []OptionSpec
	// Source names the script that registered the format (see Command).
	Source string
	// Default is true for formats from the embedded default scripts.
	Default bool
	// Replaces is true when a user format replaced an embedded default.
	Replaces bool

	fn *lua.LFunction // write (exporters) or open (importers)
}

// Option returns the declared option with the given name.
func (f *Format) Option(name string) (OptionSpec, bool) {
	for _, o := range f.Options {
		if o.Name == name {
			return o, true
		}
	}
	return OptionSpec{}, false
}

// Exporter is a registered export format (dbcc.exporter{...}).
type Exporter struct{ Format }

// Importer is a registered import format (dbcc.importer{...}, A29).
type Importer struct{ Format }

var optionNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var extensionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var optionFields = map[string]bool{"name": true, "label": true, "help": true, "type": true, "default": true, "choices": true}

// parseOptionSpecs reads a list of option declarations (the options of
// dbcc.exporter, the fields of ctx:form). Errors name the entry
// ("options[2].type: …"); the caller prefixes its own name.
func parseOptionSpecs(v lua.LValue, field string) ([]OptionSpec, error) {
	if v == lua.LNil {
		return nil, nil
	}
	t, ok := v.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("field %q must be a list of options", field)
	}
	var out []OptionSpec
	seen := map[string]bool{}
	for i := 1; i <= t.Len(); i++ {
		e, ok := t.RawGetInt(i).(*lua.LTable)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a table {name=, type=, …}", field, i)
		}
		o, err := parseOptionSpec(e)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %v", field, i, err)
		}
		if seen[o.Name] {
			return nil, fmt.Errorf("%s[%d]: duplicate option %q", field, i, o.Name)
		}
		seen[o.Name] = true
		out = append(out, o)
	}
	if n := countKeys(t); n != t.Len() {
		return nil, fmt.Errorf("field %q must be a list (1, 2, … without holes)", field)
	}
	return out, nil
}

func countKeys(t *lua.LTable) int {
	n := 0
	t.ForEach(func(lua.LValue, lua.LValue) { n++ })
	return n
}

func parseOptionSpec(e *lua.LTable) (OptionSpec, error) {
	var bad []string
	e.ForEach(func(k, _ lua.LValue) {
		if ks, ok := k.(lua.LString); !ok || !optionFields[string(ks)] {
			bad = append(bad, k.String())
		}
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		return OptionSpec{}, fmt.Errorf("unknown field(s) %v (name, label, help, type, default, choices)", bad)
	}
	str := func(f string) (string, error) {
		switch x := e.RawGetString(f).(type) {
		case lua.LString:
			return string(x), nil
		case *lua.LNilType:
			return "", nil
		default:
			return "", fmt.Errorf("field %q must be a string, got %s", f, x.Type())
		}
	}
	var o OptionSpec
	var err error
	if o.Name, err = str("name"); err != nil {
		return o, err
	}
	if !optionNameRE.MatchString(o.Name) {
		if o.Name == "" {
			return o, errors.New(`field "name" is required`)
		}
		return o, fmt.Errorf("invalid option name %q (lower-case letters, digits and '_', starting with a letter)", o.Name)
	}
	if o.Label, err = str("label"); err != nil {
		return o, err
	}
	if o.Label == "" {
		o.Label = o.Name
	}
	if o.Help, err = str("help"); err != nil {
		return o, err
	}
	typ, err := str("type")
	if err != nil {
		return o, err
	}
	o.Type = OptionType(typ)
	if typ == "" {
		return o, fmt.Errorf("%s: field \"type\" is required (string, int, bool, choice)", o.Name)
	}
	if !optionTypes[o.Type] {
		return o, fmt.Errorf("%s: unknown type %q (string, int, bool, choice)", o.Name, typ)
	}
	switch c := e.RawGetString("choices").(type) {
	case *lua.LNilType:
		if o.Type == OptChoice {
			return o, fmt.Errorf("%s: a choice needs choices = { … }", o.Name)
		}
	case *lua.LTable:
		if o.Type != OptChoice {
			return o, fmt.Errorf("%s: choices are only allowed for type \"choice\"", o.Name)
		}
		for i := 1; i <= c.Len(); i++ {
			switch x := c.RawGetInt(i).(type) {
			case lua.LString:
				o.Choices = append(o.Choices, Choice{Value: string(x), Label: string(x)})
			case *lua.LTable:
				val, ok := x.RawGetString("value").(lua.LString)
				if !ok {
					return o, fmt.Errorf("%s: choices[%d].value must be a string", o.Name, i)
				}
				ch := Choice{Value: string(val), Label: string(val)}
				switch l := x.RawGetString("label").(type) {
				case lua.LString:
					ch.Label = string(l)
				case *lua.LNilType:
				default:
					return o, fmt.Errorf("%s: choices[%d].label must be a string", o.Name, i)
				}
				o.Choices = append(o.Choices, ch)
			default:
				return o, fmt.Errorf("%s: choices[%d] must be a string or {value=, label=}", o.Name, i)
			}
		}
		if len(o.Choices) == 0 {
			return o, fmt.Errorf("%s: choices must not be empty", o.Name)
		}
	default:
		return o, fmt.Errorf("%s: field \"choices\" must be a list", o.Name)
	}
	dv := e.RawGetString("default")
	if dv == lua.LNil {
		switch o.Type {
		case OptString:
			o.Default = ""
		case OptInt:
			o.Default = int64(0)
		case OptBool:
			o.Default = false
		case OptChoice:
			o.Default = o.Choices[0].Value
		}
		return o, nil
	}
	ok := false
	switch o.Type {
	case OptString:
		var s lua.LString
		s, ok = dv.(lua.LString)
		o.Default = string(s)
	case OptInt:
		var n lua.LNumber
		if n, ok = dv.(lua.LNumber); ok {
			f := float64(n)
			ok = f == math.Trunc(f) && math.Abs(f) < 1<<53
			o.Default = int64(f)
		}
	case OptBool:
		var b lua.LBool
		b, ok = dv.(lua.LBool)
		o.Default = bool(b)
	case OptChoice:
		if s, isStr := dv.(lua.LString); isStr {
			for _, c := range o.Choices {
				if c.Value == string(s) {
					ok = true
				}
			}
			o.Default = string(s)
		}
	}
	if !ok {
		return o, fmt.Errorf("%s: default %s does not fit type %s", o.Name, describeLua(dv), o.Type)
	}
	return o, nil
}

// describeGo renders a Go option value for error messages.
func describeGo(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(v)
}

// Convert checks v (a string, int64, float64 or bool: from the command
// line, Lua or the form) against the option and returns the typed value:
// string, int64 or bool. Text from the command line is accepted for every
// type ("true"/"false"/"yes"/"no"/"1"/"0" for bool, integer text for int);
// a choice also accepts the first word of a label ("tab" for "\t").
func (o OptionSpec) Convert(v any) (any, error) {
	text, isText := v.(string)
	switch o.Type {
	case OptString:
		switch x := v.(type) {
		case string:
			return x, nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return formatNumber(x), nil
		}
		return nil, fmt.Errorf("want a string, got %s", describeGo(v))
	case OptInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case float64:
			if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
				return int64(x), nil
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
				return n, nil
			}
		}
		return nil, fmt.Errorf("want an integer, got %s", describeGo(v))
	case OptBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		if isText {
			switch strings.ToLower(strings.TrimSpace(text)) {
			case "true", "yes", "1":
				return true, nil
			case "false", "no", "0":
				return false, nil
			}
		}
		return nil, fmt.Errorf("want true or false (also yes/no, 1/0), got %s", describeGo(v))
	case OptChoice:
		switch x := v.(type) {
		case int64:
			text, isText = strconv.FormatInt(x, 10), true
		case float64:
			text, isText = formatNumber(x), true
		}
		if isText {
			for _, c := range o.Choices {
				if c.Value == text {
					return c.Value, nil
				}
			}
			for _, c := range o.Choices {
				if f := strings.Fields(c.Label); len(f) > 0 && (strings.EqualFold(c.Label, text) || strings.EqualFold(f[0], text)) {
					return c.Value, nil
				}
			}
		}
		vals := make([]string, len(o.Choices))
		for i, c := range o.Choices {
			vals[i] = strconv.Quote(c.Value)
			if f := strings.Fields(c.Label); len(f) > 0 && f[0] != c.Value {
				vals[i] += " (" + f[0] + ")"
			}
		}
		return nil, fmt.Errorf("%s is not one of %s", describeGo(v), strings.Join(vals, ", "))
	}
	return nil, fmt.Errorf("unknown option type %q", o.Type)
}

// ResolveOptions validates given (name -> value) against specs and
// completes it with the defaults. Errors name the option.
func ResolveOptions(specs []OptionSpec, given map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(specs))
	for name, v := range given {
		var spec *OptionSpec
		for i := range specs {
			if specs[i].Name == name {
				spec = &specs[i]
			}
		}
		if spec == nil {
			if len(specs) == 0 {
				return nil, fmt.Errorf("unknown option %q (there are no options)", name)
			}
			names := make([]string, len(specs))
			for i, s := range specs {
				names[i] = s.Name
			}
			sort.Strings(names)
			return nil, fmt.Errorf("unknown option %q (options: %s)", name, strings.Join(names, ", "))
		}
		cv, err := spec.Convert(v)
		if err != nil {
			return nil, fmt.Errorf("option %s: %v", name, err)
		}
		out[name] = cv
	}
	for _, s := range specs {
		if _, ok := out[s.Name]; !ok {
			out[s.Name] = s.Default
		}
	}
	return out, nil
}

// luaOptionValue converts a Lua option value for Convert.
func luaOptionValue(v lua.LValue) (any, error) {
	switch x := v.(type) {
	case lua.LString:
		return string(x), nil
	case lua.LNumber:
		return float64(x), nil
	case lua.LBool:
		return bool(x), nil
	}
	return nil, fmt.Errorf("must be a string, number or boolean, got %s", v.Type())
}

func optionToLua(v any) lua.LValue {
	switch x := v.(type) {
	case string:
		return lua.LString(x)
	case int64:
		return lua.LNumber(x)
	case int:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case bool:
		return lua.LBool(x)
	}
	return lua.LNil
}

// optionValues reads a Lua table name -> value (opts.options of
// ctx:export_with, opts.values of ctx:form).
func optionValues(t *lua.LTable, what string) (map[string]any, error) {
	out := map[string]any{}
	var err error
	t.ForEach(func(k, v lua.LValue) {
		if err != nil {
			return
		}
		ks, ok := k.(lua.LString)
		if !ok {
			err = fmt.Errorf("%s must map option names to values", what)
			return
		}
		gv, verr := luaOptionValue(v)
		if verr != nil {
			err = fmt.Errorf("%s.%s %v", what, ks, verr)
			return
		}
		out[string(ks)] = gv
	})
	return out, err
}

// optionSpecTable mirrors an OptionSpec for Lua (dbcc.exporters).
func optionSpecTable(L *lua.LState, o OptionSpec) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("name", lua.LString(o.Name))
	t.RawSetString("label", lua.LString(o.Label))
	t.RawSetString("help", lua.LString(o.Help))
	t.RawSetString("type", lua.LString(string(o.Type)))
	t.RawSetString("default", optionToLua(o.Default))
	if o.Type == OptChoice {
		cs := L.CreateTable(len(o.Choices), 0)
		for _, c := range o.Choices {
			ct := L.NewTable()
			ct.RawSetString("value", lua.LString(c.Value))
			ct.RawSetString("label", lua.LString(c.Label))
			cs.Append(ct)
		}
		t.RawSetString("choices", cs)
	}
	return t
}

// parseFormat reads the declaration of dbcc.exporter (kind "exporter",
// function field "write") or dbcc.importer ("importer", "open").
func (rt *Runtime) parseFormat(L *lua.LState, kind, fnField string) Format {
	t := L.CheckTable(1)
	what := "dbcc." + kind
	if !rt.loading {
		L.RaiseError("%s may only be called while a command script loads", what)
	}
	fields := map[string]bool{"name": true, "title": true, "help": true, "extensions": true, "options": true, fnField: true}
	var bad []string
	t.ForEach(func(k, _ lua.LValue) {
		if ks, ok := k.(lua.LString); !ok || !fields[string(ks)] {
			bad = append(bad, k.String())
		}
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		L.RaiseError("%s: unknown field(s) %v (name, title, help, extensions, options, %s)", what, bad, fnField)
	}
	str := func(field string, required bool) string {
		v := t.RawGetString(field)
		switch x := v.(type) {
		case lua.LString:
			return string(x)
		case *lua.LNilType:
			if required {
				L.RaiseError("%s: field %q is required", what, field)
			}
			return ""
		}
		L.RaiseError("%s: field %q must be a string, got %s", what, field, v.Type())
		return ""
	}
	e := Format{Name: str("name", true), Title: str("title", false), Help: str("help", false),
		Source: rt.loadSrc, Default: rt.loadDef}
	if !nameRE.MatchString(e.Name) {
		L.RaiseError("%s: invalid %s name %q (lower-case letters, digits, '_' and '-')", what, kind, e.Name)
	}
	if e.Title == "" {
		e.Title = e.Name
	}
	switch v := t.RawGetString("extensions").(type) {
	case *lua.LTable:
		for i := 1; i <= v.Len(); i++ {
			s, ok := v.RawGetInt(i).(lua.LString)
			ext := strings.TrimPrefix(string(s), ".")
			if !ok || !extensionRE.MatchString(ext) {
				L.RaiseError("%s %s: extensions must be a list of file extensions like { \"csv\" }", what, e.Name)
			}
			e.Extensions = append(e.Extensions, ext)
		}
	case *lua.LNilType:
	default:
		L.RaiseError("%s %s: field \"extensions\" must be a list", what, e.Name)
	}
	opts, err := parseOptionSpecs(t.RawGetString("options"), "options")
	if err != nil {
		L.RaiseError("%s %s: %v", what, e.Name, err)
	}
	e.Options = opts
	fn, ok := t.RawGetString(fnField).(*lua.LFunction)
	if !ok {
		L.RaiseError("%s %s: field \"%s\" must be a function", what, e.Name, fnField)
	}
	e.fn = fn
	return e
}

// dbcc.exporter{name=, title=, help=, extensions={…}, options={…}, write=}
// registers an export format while a command script loads. write is
// called as write(ctx, src, out, opts) by ctx:export_with.
func (rt *Runtime) luaExporter(L *lua.LState) int {
	rt.stagedExp = append(rt.stagedExp, &Exporter{rt.parseFormat(L, "exporter", "write")})
	return 0
}

// formatList mirrors registered formats for Lua (dbcc.exporters, dbcc.importers).
func formatList(L *lua.LState, fs []*Format) *lua.LTable {
	out := L.NewTable()
	for _, e := range fs {
		t := L.NewTable()
		t.RawSetString("name", lua.LString(e.Name))
		t.RawSetString("title", lua.LString(e.Title))
		t.RawSetString("help", lua.LString(e.Help))
		t.RawSetString("extensions", strList(L, e.Extensions))
		opts := L.CreateTable(len(e.Options), 0)
		for _, o := range e.Options {
			opts.Append(optionSpecTable(L, o))
		}
		t.RawSetString("options", opts)
		t.RawSetString("source", lua.LString(e.Source))
		t.RawSetString("default", lua.LBool(e.Default))
		out.Append(t)
	}
	return out
}

// dbcc.exporters() -> { {name=, title=, help=, extensions={…}, options={…},
// source=, default=}, … } sorted by name.
func (rt *Runtime) luaExporters(L *lua.LState) int {
	var fs []*Format
	for _, e := range rt.Exporters() {
		fs = append(fs, &e.Format)
	}
	L.Push(formatList(L, fs))
	return 1
}

// exportPath normalises a path for the chosen-paths check of
// ctx:export_with: "~" expanded, absolute against the working directory,
// cleaned. Symbolic links are not resolved.
func exportPath(p string) string {
	p = expandHome(p)
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// checkChosen allows a write to file (ctx:export, ctx:export_with) only
// when the owner chose that path in the running invocation: a "file"
// argument or flag of the command, or a path from ctx:save_file. It does
// not touch the file system, so a refusal says nothing about the path.
func (rt *Runtime) checkChosen(prim, file string) error {
	p := exportPath(file)
	if rt.inv == nil || !rt.inv.chosen[p] {
		return fmt.Errorf("%s: %s was not chosen by the user: pass a path from a \"file\" argument of the command or from ctx:save_file", prim, p)
	}
	return nil
}

// checkChosenRead is checkChosen for the primitives that read a file
// (A29); the message names the ways a command gets a readable path.
func (rt *Runtime) checkChosenRead(prim, file string) error {
	p := exportPath(file)
	if rt.inv == nil || !rt.inv.chosen[p] {
		return fmt.Errorf("%s: %s was not chosen by the user: pass file:<path> to an \"object\" argument, a path to a \"file\" argument of the command, or a path from ctx:open_file", prim, p)
	}
	return nil
}

// unknownExporter is the error for a name no script registered.
func (rt *Runtime) unknownExporter(name string) error {
	var names []string
	for _, e := range rt.Exporters() {
		names = append(names, e.Name)
	}
	if len(names) == 0 {
		return fmt.Errorf("unknown export type %q (no export types are registered)", name)
	}
	return fmt.Errorf("unknown export type %q (types: %s)", name, strings.Join(names, ", "))
}

// reraise raises an error returned by a protected call of Lua code again,
// unchanged (the message already carries its position).
func (rt *Runtime) reraise(err error) int {
	var ae *lua.ApiError
	if errors.As(err, &ae) && ae.Object != nil {
		rt.L.Error(ae.Object, 0)
		return 0
	}
	return rt.raise(err)
}

// ctx:export_with(name, file, opts) -> rows written. Runs exporter name on
// the active result: opts.options (name -> value) are checked and
// completed with the defaults, opts.force overwrites an existing file,
// opts.progress is the row interval of progress messages (0: none). The
// file is opened by Go and removed again when anything fails.
func (rt *Runtime) apiExportWith(L *lua.LState, self *lua.LTable) int {
	name := L.CheckString(2)
	// file:<path> names a file here too (A29): never a file called "file:…".
	file := expandHome(strings.TrimPrefix(L.CheckString(3), FilePrefix))
	opts := ensureTable(L, 4, "opts")
	if rt.exportBusy {
		return rt.raise(errors.New("ctx:export_with cannot run inside an exporter"))
	}
	// Only a path the owner chose in this invocation; checked before the
	// file system is touched, so other paths cannot even be probed.
	if err := rt.checkChosen("ctx:export_with", file); err != nil {
		return rt.raise(err)
	}
	e, ok := rt.Exporter(name)
	if !ok {
		return rt.raise(rt.unknownExporter(name))
	}
	force, every := false, ExportProgressEvery
	given := map[string]any{}
	if opts != nil {
		force = lua.LVAsBool(opts.RawGetString("force"))
		if n, ok := opts.RawGetString("progress").(lua.LNumber); ok {
			every = int(n)
		}
		switch o := opts.RawGetString("options").(type) {
		case *lua.LNilType:
		case *lua.LTable:
			var err error
			if given, err = optionValues(o, "opts.options"); err != nil {
				L.ArgError(4, err.Error())
			}
		default:
			L.ArgError(4, "opts.options must be a table name -> value")
		}
	}
	values, err := ResolveOptions(e.Options, given)
	if err != nil {
		return rt.raise(fmt.Errorf("export type %s: %w", e.Name, err))
	}
	res, view := rt.env.Host.ActiveResult()
	if res == nil {
		return rt.raise(ErrNoResult)
	}
	n, err := rt.runExporter(self, e, file, force, every, res, view, values)
	if err != nil {
		return rt.reraise(err)
	}
	L.Push(lua.LNumber(n))
	return 1
}

// runExporter opens file, calls the exporter's write(ctx, src, out, opts)
// and closes the file. Errors of Lua code are returned as *lua.ApiError for
// reraise. A failed export never destroys data:
//
//   - without force the file is created with O_EXCL (an existing one is
//     refused) and removed again on any error or cancel;
//   - with force and an existing regular file, symbolic links are resolved
//     and the export is written to a temporary file next to the real
//     target (O_EXCL, unique name, the permission bits of the old file),
//     which replaces the target by rename only after everything was
//     written; on failure only the temporary file is removed and the old
//     file stays as it was. A link stays a link.
//   - with force and an existing non-regular file (a device, a FIFO) it is
//     written in place and never removed.
func (rt *Runtime) runExporter(self *lua.LTable, e *Exporter, file string, force bool, every int,
	res *exec.Result, view exec.View, values map[string]any) (n int, err error) {
	f, commit, abort, err := openExportTarget(file, force, "ctx:export_with(name, file, {force = true})")
	if err != nil {
		return 0, err
	}
	done := false
	defer func() {
		if !done {
			abort()
		}
	}()
	bw := bufio.NewWriterSize(f, 64<<10)
	L := rt.L
	// src and out are valid only during the call.
	valid := true
	streamErr := new(error)
	src := rt.exportSource(res, view, file, every, &n, &valid, streamErr)
	out := L.NewTable()
	out.RawSetString("path", lua.LString(file))
	out.RawSetString("write", L.NewFunction(func(L *lua.LState) int {
		if self, ok := L.Get(1).(*lua.LTable); !ok || self != out {
			L.RaiseError("out:write: call it with a colon, out:write(...)")
		}
		if !valid {
			L.RaiseError("out is only valid while the exporter's write function runs")
		}
		for i := 2; i <= L.GetTop(); i++ {
			var s string
			switch x := L.Get(i).(type) {
			case lua.LString:
				s = string(x)
			case lua.LNumber:
				s = x.String()
			default:
				L.ArgError(i, "out:write takes strings and numbers, got "+x.Type().String())
			}
			if _, werr := bw.WriteString(s); werr != nil {
				return rt.raise(werr)
			}
		}
		return 0
	}))
	optT := L.NewTable()
	for k, v := range values {
		optT.RawSetString(k, optionToLua(v))
	}
	rt.exportBusy = true
	err = L.CallByParam(lua.P{Fn: e.fn, NRet: 0, Protect: true}, self, src, out, optT)
	rt.exportBusy, valid = false, false
	if err != nil {
		return n, err
	}
	// A plugin may catch the error of src:each with pcall and return
	// normally; the export is incomplete all the same.
	if *streamErr != nil {
		return n, *streamErr
	}
	if err := bw.Flush(); err != nil {
		return n, err
	}
	done = true
	if err := commit(); err != nil {
		return n, err
	}
	if res.Capped {
		rt.message(fmt.Sprintf("export %s: the result was capped at %s rows before the export; re-run the query to export all rows",
			filepath.Base(file), exec.FormatCount(res.Len())))
	}
	return n, nil
}

// exportSource builds the src table of an exporter: columns, title and
// each(fn), which hands every row of the whole result to fn (local filter
// and sort applied as in ctx:export) and counts them in *n. An error that
// ends the stream early (database, cancel, an error in fn) is kept in
// *streamErr, so the export fails even when write catches it.
func (rt *Runtime) exportSource(res *exec.Result, view exec.View, file string, every int, n *int, valid *bool, streamErr *error) *lua.LTable {
	L := rt.L
	src := L.NewTable()
	cols := L.CreateTable(len(res.Columns), 0)
	for _, c := range res.Columns {
		ct := L.NewTable()
		ct.RawSetString("name", lua.LString(c.Name))
		ct.RawSetString("type", lua.LString(c.DBType))
		ct.RawSetString("numeric", lua.LBool(c.Numeric))
		ct.RawSetString("binary", lua.LBool(c.Binary))
		ct.RawSetString("decimal", lua.LString(decimalName(c)))
		cols.Append(ct)
	}
	src.RawSetString("columns", cols)
	src.RawSetString("title", lua.LString(res.Title))
	used := false
	src.RawSetString("each", L.NewFunction(func(L *lua.LState) int {
		if self, ok := L.Get(1).(*lua.LTable); !ok || self != src {
			L.RaiseError("src:each: call it with a colon, src:each(function(row) … end)")
		}
		fn := L.CheckFunction(2)
		if !*valid {
			L.RaiseError("src is only valid while the exporter's write function runs")
		}
		if used {
			L.RaiseError("src:each may be called only once")
		}
		used = true
		ctx := rt.gctx()
		lctx := L.Context()
		stopped := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if lctx != nil {
				return lctx.Err()
			}
			return nil
		}
		var luaErr error
		emit := func(r exec.Row) error {
			// The text the grid and the CSV export before A28 show
			// (exec.CellText): every value is a string, NULL is nil.
			row := L.CreateTable(len(r), 0)
			for i, v := range r {
				if v != nil {
					row.RawSetInt(i+1, lua.LString(exec.CellText(v)))
				}
			}
			if err := L.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true}, row); err != nil {
				luaErr = err
				return err
			}
			*n++
			if every > 0 && *n%every == 0 {
				rt.message(fmt.Sprintf("export %s: %s rows…", filepath.Base(file), exec.FormatCount(*n)))
			}
			return nil
		}
		var err error
		// The whole result, not just the page in memory (spec 10.7).
		if view != nil && view.Sorted() {
			// A local sort needs every row in memory before the first is handed out.
			var kept []exec.Row
			err = res.Stream(ctx, 1000, func(rows []exec.Row) error {
				for _, r := range rows {
					if view.Keep(r) {
						kept = append(kept, r)
					}
				}
				return stopped()
			})
			if err == nil {
				for _, r := range view.Order(kept) {
					if err = emit(r); err != nil {
						break
					}
				}
			}
		} else {
			err = res.Stream(ctx, 1000, func(rows []exec.Row) error {
				for _, r := range rows {
					if view != nil && !view.Keep(r) {
						continue
					}
					if err := emit(r); err != nil {
						return err
					}
				}
				return stopped()
			})
		}
		if luaErr != nil {
			*streamErr = luaErr
			return rt.reraise(luaErr)
		}
		if err != nil {
			*streamErr = err
			return rt.raise(err)
		}
		return 0
	}))
	return src
}

// openExportTarget opens the file an export writes (see runExporter).
// commit finishes a successful export (close, and rename for a
// replacement); abort undoes a failed one.
// luaHint names the Lua call in the "already exists" message.
func openExportTarget(file string, force bool, luaHint string) (f *os.File, commit func() error, abort func(), err error) {
	exists := func() error {
		return fmt.Errorf("%s already exists; overwrite with export -f (in Lua: %s)", file, luaHint)
	}
	if force {
		if li, lerr := os.Lstat(file); lerr == nil {
			real, rerr := filepath.EvalSymlinks(file)
			if rerr != nil {
				if li.Mode()&os.ModeSymlink != 0 {
					return nil, nil, nil, fmt.Errorf("%s is a symbolic link whose target cannot be resolved: %w", file, rerr)
				}
				return nil, nil, nil, rerr
			}
			st, serr := os.Stat(real)
			if serr != nil {
				return nil, nil, nil, serr
			}
			switch {
			case st.IsDir():
				return nil, nil, nil, fmt.Errorf("%s is a directory", file)
			case !st.Mode().IsRegular():
				// A device or FIFO: write in place, never remove it.
				f, err := os.OpenFile(real, os.O_WRONLY, 0)
				if err != nil {
					return nil, nil, nil, err
				}
				return f, f.Close, func() { f.Close() }, nil
			}
			// os.CreateTemp opens with O_EXCL under a random name.
			tmp, terr := os.CreateTemp(filepath.Dir(real), "."+filepath.Base(real)+".dbcc-*.tmp")
			if terr != nil {
				return nil, nil, nil, fmt.Errorf("cannot create a temporary file next to %s (the old file is kept): %w", real, terr)
			}
			tmp.Chmod(st.Mode().Perm()) // best effort: keep the permission bits of the old file
			name := tmp.Name()
			commit = func() error {
				if err := tmp.Close(); err != nil {
					os.Remove(name)
					return err
				}
				if err := os.Rename(name, real); err != nil {
					os.Remove(name)
					return err
				}
				return nil
			}
			abort = func() {
				tmp.Close()
				os.Remove(name) // the old file stays untouched
			}
			return tmp, commit, abort, nil
		}
	}
	f, err = os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil, nil, exists()
		}
		return nil, nil, nil, err
	}
	commit = func() error {
		if err := f.Close(); err != nil {
			os.Remove(file)
			return err
		}
		return nil
	}
	abort = func() {
		f.Close()
		os.Remove(file) // our own new file: no half-written export
	}
	return f, commit, abort, nil
}

// ctx:form(fields, opts) -> values, or nil when cancelled. fields use the
// option format of dbcc.exporter; opts.title, opts.values (name -> initial
// value; others start at their default).
func (rt *Runtime) apiForm(L *lua.LState, self *lua.LTable) int {
	specs, err := parseOptionSpecs(L.Get(2), "fields")
	if err != nil {
		L.ArgError(2, "ctx:form: "+err.Error())
	}
	opts := ensureTable(L, 3, "opts")
	given := map[string]any{}
	if opts != nil {
		switch v := opts.RawGetString("values").(type) {
		case *lua.LNilType:
		case *lua.LTable:
			if given, err = optionValues(v, "opts.values"); err != nil {
				L.ArgError(3, "ctx:form: "+err.Error())
			}
		default:
			L.ArgError(3, "ctx:form: opts.values must be a table name -> value")
		}
	}
	values, err := ResolveOptions(specs, given)
	if err != nil {
		return rt.raise(fmt.Errorf("ctx:form: %w", err))
	}
	got, ok, err := rt.env.Host.Form(rt.title(opts), specs, values)
	if err != nil {
		return rt.raise(fmt.Errorf("ctx:form: %w", err))
	}
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	// The host returns typed values; check them like any other input.
	if got, err = ResolveOptions(specs, got); err != nil {
		return rt.raise(fmt.Errorf("ctx:form: %w", err))
	}
	t := L.NewTable()
	for _, s := range specs {
		t.RawSetString(s.Name, optionToLua(got[s.Name]))
	}
	L.Push(t)
	return 1
}

// ctx:save_file(opts) -> path, exists — or nil when cancelled. The file
// dialog: opts.title, opts.name (suggested file name), opts.dir (start
// directory), opts.extensions (list). exists tells whether the chosen file
// is already there (the dialog itself does not ask about overwriting).
func (rt *Runtime) apiSaveFile(L *lua.LState, self *lua.LTable) int {
	opts := ensureTable(L, 2, "opts")
	so := SaveFileOptions{Title: rt.title(opts)}
	if opts != nil {
		so.Name = luaFieldString(opts, "name")
		if d := luaFieldString(opts, "dir"); d != "" {
			so.Dir = expandHome(d)
		}
		switch v := opts.RawGetString("extensions").(type) {
		case *lua.LNilType:
		case *lua.LTable:
			for i := 1; i <= v.Len(); i++ {
				so.Extensions = append(so.Extensions, strings.TrimPrefix(L.ToStringMeta(v.RawGetInt(i)).String(), "."))
			}
		default:
			L.ArgError(2, "ctx:save_file: opts.extensions must be a list")
		}
	}
	path, ok, err := rt.env.Host.SaveFile(so)
	if err != nil {
		return rt.raise(fmt.Errorf("ctx:save_file: %w", err))
	}
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	// The owner chose this path: ctx:export_with may write there.
	rt.inv.chosen[exportPath(path)] = true
	_, serr := os.Stat(path)
	L.Push(lua.LString(path))
	L.Push(lua.LBool(serr == nil))
	return 2
}

// ctx:result_info() -> {title=, columns={ {name=, type=, numeric=}, … },
// loaded=, capped=} of the active result, or nil when there is none.
func (rt *Runtime) apiResultInfo(L *lua.LState, self *lua.LTable) int {
	res, _ := rt.env.Host.ActiveResult()
	if res == nil {
		L.Push(lua.LNil)
		return 1
	}
	t := L.NewTable()
	t.RawSetString("title", lua.LString(res.Title))
	cols := L.CreateTable(len(res.Columns), 0)
	for _, c := range res.Columns {
		ct := L.NewTable()
		ct.RawSetString("name", lua.LString(c.Name))
		ct.RawSetString("type", lua.LString(c.DBType))
		ct.RawSetString("numeric", lua.LBool(c.Numeric))
		cols.Append(ct)
	}
	t.RawSetString("columns", cols)
	t.RawSetString("loaded", lua.LNumber(res.Len()))
	t.RawSetString("capped", lua.LBool(res.Capped))
	L.Push(t)
	return 1
}
