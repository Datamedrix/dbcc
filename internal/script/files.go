package script

// Files as data sources (spec A29): import formats are Lua plugins
// (dbcc.importer) with a pull model — open(ctx, src, opts) returns the
// columns and a next() function that Go calls once per row. A file result
// is an exec.Result on a cursor over a fileSource, so it gets the paging,
// Stream and Drain code of SQL results unchanged.
//
// Goroutines: next() is Lua and runs only where the runtime may run — the
// worker goroutine in the UI (grid fetches and paging go through the
// grid's Fetcher, a.do; commands run there), main in headless mode. The
// UI goroutine only ever closes a file cursor (closing a tab): that
// interrupts the source (closes the file handle, stops its Lua code) and
// never calls Lua or waits for a read in flight.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/exec"
)

// FilePrefix marks a file where a command takes a table: file:<path>.
const FilePrefix = "file:"

// Files is the state of file results that outlives a Lua runtime (reload
// replaces the runtime, the result tabs stay): the runtime that reads files
// now, the import settings remembered per path in this session, and the
// page and chunk sizes of file results. Safe for concurrent use.
type Files struct {
	// PageSize is the page size of file results (0: no paging, every row
	// is loaded — headless); FetchSize the chunk in which rows are read.
	PageSize, FetchSize int

	remember bool
	mu       sync.Mutex
	rt       *Runtime
	settings map[string]fileSettings
}

type fileSettings struct {
	importer string
	options  map[string]any
}

// NewFiles returns the file state; remember keeps the import settings of
// each path for the session (UI; headless has no memory).
func NewFiles(pageSize, fetchSize int, remember bool) *Files {
	return &Files{PageSize: pageSize, FetchSize: fetchSize, remember: remember, settings: map[string]fileSettings{}}
}

// Use makes rt the runtime that reads (and reopens) file results.
func (f *Files) Use(rt *Runtime) {
	f.mu.Lock()
	f.rt = rt
	f.mu.Unlock()
}

func (f *Files) current() *Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rt
}

func (f *Files) lookup(path string) (fileSettings, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.settings[path]
	return s, ok
}

func (f *Files) store(path string, s fileSettings) {
	if !f.remember {
		return
	}
	f.mu.Lock()
	f.settings[path] = s
	f.mu.Unlock()
}

// ---- registration ----

// dbcc.importer{name=, title=, help=, extensions={…}, options={…}, open=}
// registers an import format while a command script loads. open is called
// as open(ctx, src, opts) and returns {columns = {…}, next = function}.
func (rt *Runtime) luaImporter(L *lua.LState) int {
	rt.stagedImp = append(rt.stagedImp, &Importer{rt.parseFormat(L, "importer", "open")})
	return 0
}

// dbcc.importers() -> list like dbcc.exporters().
func (rt *Runtime) luaImporters(L *lua.LState) int {
	var fs []*Format
	for _, e := range rt.Importers() {
		fs = append(fs, &e.Format)
	}
	L.Push(formatList(L, fs))
	return 1
}

// ---- encodings ----

// cp1252 maps the bytes 0x80–0x9F of windows-1252; the five unassigned
// ones stay the C1 controls of ISO-8859-1 (as WHATWG decodes them).
var cp1252 = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

var encodings = map[string]bool{"utf-8": true, "windows-1252": true, "iso-8859-1": true}

// decode turns bytes of encoding enc into a (valid UTF-8) string; invalid
// UTF-8 becomes U+FFFD.
func decode(b []byte, enc string) string {
	switch enc {
	case "windows-1252", "iso-8859-1":
		ascii := true
		for _, c := range b {
			if c >= 0x80 {
				ascii = false
				break
			}
		}
		if ascii {
			return string(b)
		}
		var sb strings.Builder
		sb.Grow(len(b) + len(b)/4)
		for _, c := range b {
			switch {
			case c < 0x80:
				sb.WriteByte(c)
			case c < 0xA0 && enc == "windows-1252":
				sb.WriteRune(cp1252[c-0x80])
			default:
				sb.WriteRune(rune(c))
			}
		}
		return sb.String()
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

// ---- the file a plugin reads (src) ----

// fileReader is the src of an importer: a buffered file with an encoding.
// It is used on the Lua goroutine only; Interrupt (any goroutine) may
// close the handle under it, after which reads fail.
type fileReader struct {
	path, name string
	size       int64
	modified   string
	f          *os.File
	br         *bufio.Reader
	enc        string
	started    bool // something was read: the encoding is fixed
	line       int  // lines consumed so far
	closed     atomic.Bool
}

func openFileReader(path string) (*fileReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, fmt.Errorf("%s is a directory", path)
	}
	return &fileReader{path: path, name: filepath.Base(path), size: st.Size(),
		modified: st.ModTime().Local().Format("2006-01-02 15:04:05"),
		f:        f, br: bufio.NewReaderSize(f, 64<<10), enc: "utf-8"}, nil
}

func (r *fileReader) close() {
	r.closed.Store(true)
	r.f.Close()
}

func (r *fileReader) check() error {
	if r.closed.Load() {
		return errors.New("the file is closed")
	}
	return nil
}

// readLine returns the next line without LF or CRLF; ok is false at the end.
func (r *fileReader) readLine() (string, bool, error) {
	if err := r.check(); err != nil {
		return "", false, err
	}
	r.started = true
	b, err := r.br.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		// A line longer than the buffer: collect it.
		buf := append([]byte(nil), b...)
		for err == bufio.ErrBufferFull {
			b, err = r.br.ReadSlice('\n')
			buf = append(buf, b...)
		}
		b = buf
	}
	if err != nil && err != io.EOF {
		return "", false, err
	}
	if len(b) == 0 && err == io.EOF {
		return "", false, nil
	}
	r.line++
	b = trimEOL(b)
	return decode(b, r.enc), true, nil
}

// trimEOL removes LF, CRLF, or — on a last line without LF — a lone CR.
// Files that use CR alone as the line end throughout (old Mac) are not
// supported: they read as one line.
func trimEOL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

// completeRune extends b by up to 3 bytes so that a UTF-8 sequence cut
// by a byte count is decoded whole.
func (r *fileReader) completeRune(b []byte) []byte {
	if r.enc != "utf-8" {
		return b
	}
	for i := 0; i < 3 && len(b) > 0 && !utf8.FullRune(b[lastRuneStart(b):]); i++ {
		c, err := r.br.ReadByte()
		if err != nil {
			break
		}
		b = append(b, c)
	}
	return b
}

func lastRuneStart(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-4; i-- {
		if utf8.RuneStart(b[i]) {
			return i
		}
	}
	return len(b) - 1
}

// luaTable builds the src table handed to open(ctx, src, opts).
func (r *fileReader) luaTable(rt *Runtime) *lua.LTable {
	L := rt.L
	t := L.NewTable()
	t.RawSetString("path", lua.LString(r.path))
	t.RawSetString("name", lua.LString(r.name))
	t.RawSetString("size", lua.LNumber(r.size))
	self := func(L *lua.LState, m string) {
		if s, ok := L.Get(1).(*lua.LTable); !ok || s != t {
			L.RaiseError("src:%s: call it with a colon, src:%s(...)", m, m)
		}
		if err := r.check(); err != nil {
			L.RaiseError("src:%s: %v", m, err)
		}
	}
	ioErr := func(L *lua.LState, m string, err error) int {
		return rt.raise(fmt.Errorf("src:%s: %s: %w", m, r.name, err))
	}
	t.RawSetString("encoding", L.NewFunction(func(L *lua.LState) int {
		self(L, "encoding")
		name := strings.ToLower(L.CheckString(2))
		if !encodings[name] {
			L.ArgError(2, fmt.Sprintf("src:encoding: unknown encoding %q (utf-8, windows-1252, iso-8859-1)", name))
		}
		if r.started {
			L.RaiseError("src:encoding: must be called before the first read")
		}
		r.enc = name
		return 0
	}))
	t.RawSetString("skip_bom", L.NewFunction(func(L *lua.LState) int {
		self(L, "skip_bom")
		ok := false
		if !r.started {
			if b, err := r.br.Peek(3); err == nil && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
				r.br.Discard(3)
				ok = true
			}
		}
		L.Push(lua.LBool(ok))
		return 1
	}))
	t.RawSetString("read", L.NewFunction(func(L *lua.LState) int {
		self(L, "read")
		n := L.CheckInt(2)
		if n <= 0 {
			L.ArgError(2, "src:read: n must be positive")
		}
		r.started = true
		b := make([]byte, n)
		got, err := io.ReadFull(r.br, b)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return ioErr(L, "read", err)
		}
		if got == 0 {
			L.Push(lua.LNil)
			return 1
		}
		b = r.completeRune(b[:got])
		r.line += strings.Count(string(b), "\n")
		L.Push(lua.LString(decode(b, r.enc)))
		return 1
	}))
	t.RawSetString("peek", L.NewFunction(func(L *lua.LState) int {
		self(L, "peek")
		n := L.CheckInt(2)
		if n <= 0 || n > r.br.Size() {
			n = r.br.Size()
		}
		b, err := r.br.Peek(n)
		if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
			return ioErr(L, "peek", err)
		}
		if r.enc == "utf-8" && len(b) > 0 && !utf8.FullRune(b[lastRuneStart(b):]) {
			b = b[:lastRuneStart(b)]
		}
		L.Push(lua.LString(decode(b, r.enc)))
		return 1
	}))
	t.RawSetString("line", L.NewFunction(func(L *lua.LState) int {
		self(L, "line")
		s, ok, err := r.readLine()
		if err != nil {
			return ioErr(L, "line", err)
		}
		if !ok {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(s))
		return 1
	}))
	t.RawSetString("csv", L.NewFunction(func(L *lua.LState) int {
		self(L, "csv")
		c, err := newCSVParser(L, r, ensureTable(L, 2, "opts"))
		if err != nil {
			L.ArgError(2, "src:csv: "+err.Error())
		}
		L.Push(c.luaTable(rt))
		return 1
	}))
	return t
}

// ---- CSV records ----

// csvParser reads CSV records from a fileReader (src:csv). Quoting follows
// spreadsheets: a field that starts with the quote character is quoted,
// a doubled quote inside is one quote, delimiters and line breaks inside
// are data (a line break is read as LF); text after the closing quote up
// to the next delimiter is appended as is. Empty lines are skipped.
type csvParser struct {
	r            *fileReader
	delim, quote string
	null         *string // an unquoted field equal to it is NULL
	emptyIsNull  bool    // an unquoted empty field is NULL
	record, line int
}

func newCSVParser(L *lua.LState, r *fileReader, opts *lua.LTable) (*csvParser, error) {
	p := &csvParser{r: r, delim: ",", quote: `"`}
	if opts == nil {
		return p, nil
	}
	one := func(field, def string) (string, error) {
		v := opts.RawGetString(field)
		if v == lua.LNil {
			return def, nil
		}
		s, ok := v.(lua.LString)
		if !ok || utf8.RuneCountInString(string(s)) != 1 {
			return "", fmt.Errorf("opts.%s must be exactly one character, got %s", field, describeLua(v))
		}
		return string(s), nil
	}
	var err error
	if p.delim, err = one("delimiter", p.delim); err != nil {
		return nil, err
	}
	if p.quote, err = one("quote", p.quote); err != nil {
		return nil, err
	}
	if p.delim == p.quote || p.delim == "\n" || p.delim == "\r" || p.quote == "\n" || p.quote == "\r" {
		return nil, errors.New("opts.delimiter and opts.quote must differ and must not be a line break")
	}
	switch v := opts.RawGetString("null").(type) {
	case *lua.LNilType:
	case lua.LString:
		s := string(v)
		p.null = &s
	default:
		return nil, errors.New("opts.null must be a string")
	}
	p.emptyIsNull = lua.LVAsBool(opts.RawGetString("empty_is_null"))
	return p, nil
}

// errUnterminated is wrapped by the error of a quote that never closes.
var errUnterminated = errors.New("unterminated quoted field")

// next reads one record; ok is false at the end.
func (p *csvParser) next() (fields []string, quoted []bool, ok bool, err error) {
	var line string
	for {
		line, ok, err = p.r.readLine()
		if err != nil || !ok {
			return nil, nil, false, err
		}
		if line != "" {
			break
		}
	}
	start := p.r.line
	pos := 0
	for {
		if strings.HasPrefix(line[pos:], p.quote) {
			pos += len(p.quote)
			var b strings.Builder
			for {
				i := strings.Index(line[pos:], p.quote)
				if i < 0 {
					b.WriteString(line[pos:])
					var more bool
					line, more, err = p.r.readLine()
					if err != nil {
						return nil, nil, false, err
					}
					if !more {
						return nil, nil, false, fmt.Errorf("%w: the quote opened in line %d is not closed", errUnterminated, start)
					}
					b.WriteByte('\n')
					pos = 0
					continue
				}
				b.WriteString(line[pos : pos+i])
				pos += i + len(p.quote)
				if strings.HasPrefix(line[pos:], p.quote) {
					b.WriteString(p.quote)
					pos += len(p.quote)
					continue
				}
				break
			}
			// Text after the closing quote belongs to the field (lenient).
			if j := strings.Index(line[pos:], p.delim); j < 0 {
				b.WriteString(line[pos:])
				pos = len(line)
			} else {
				b.WriteString(line[pos : pos+j])
				pos += j
			}
			fields, quoted = append(fields, b.String()), append(quoted, true)
		} else {
			f := line[pos:]
			if j := strings.Index(f, p.delim); j >= 0 {
				f = f[:j]
			}
			pos += len(f)
			fields, quoted = append(fields, f), append(quoted, false)
		}
		if !strings.HasPrefix(line[pos:], p.delim) {
			break
		}
		pos += len(p.delim)
		if pos == len(line) {
			fields, quoted = append(fields, ""), append(quoted, false) // trailing delimiter: an empty last field
			break
		}
	}
	p.record++
	p.line = start
	return fields, quoted, true, nil
}

// luaTable builds the reader object of src:csv: r:read(raw) -> list of
// field strings (unquoted fields equal to opts.null, or empty with
// opts.empty_is_null, are nil unless raw is true) and the field count, or
// nil at the end; r.record and r.line describe the record last read.
func (p *csvParser) luaTable(rt *Runtime) *lua.LTable {
	L := rt.L
	t := L.NewTable()
	t.RawSetString("record", lua.LNumber(0))
	t.RawSetString("line", lua.LNumber(0))
	t.RawSetString("read", L.NewFunction(func(L *lua.LState) int {
		if s, ok := L.Get(1).(*lua.LTable); !ok || s != t {
			L.RaiseError("csv:read: call it with a colon, r:read()")
		}
		if err := p.r.check(); err != nil {
			L.RaiseError("csv:read: %v", err)
		}
		raw := lua.LVAsBool(L.Get(2))
		fields, quoted, ok, err := p.next()
		if err != nil {
			if errors.Is(err, errUnterminated) {
				return rt.raise(err) // the importer's error names the file
			}
			return rt.raise(fmt.Errorf("csv:read: %s: %w", p.r.name, err))
		}
		if !ok {
			L.Push(lua.LNil)
			return 1
		}
		row := L.CreateTable(len(fields), 0)
		for i, f := range fields {
			if !raw && !quoted[i] && ((p.null != nil && f == *p.null) || (p.emptyIsNull && f == "")) {
				continue
			}
			row.RawSetInt(i+1, lua.LString(f))
		}
		t.RawSetString("record", lua.LNumber(p.record))
		t.RawSetString("line", lua.LNumber(p.line))
		L.Push(row)
		L.Push(lua.LNumber(len(fields)))
		return 2
	}))
	return t
}

// ---- the row source of a file result ----

// fileSource is the exec.RowSource of a file result: it calls the
// importer's next() for every row.
type fileSource struct {
	rt       *Runtime
	rd       *fileReader
	importer string
	next     *lua.LFunction
	ncols    int
	limit    int // stop after limit rows (-1: no limit)
	n        int // rows handed out
	intr     atomic.Bool

	mu  sync.Mutex // res/cur, set after the cursor exists
	res *exec.Result
	cur *exec.Cursor
}

var _ exec.RowSource = (*fileSource)(nil)
var _ exec.Interrupter = (*fileSource)(nil)

func (s *fileSource) attach(res *exec.Result, cur *exec.Cursor) {
	s.mu.Lock()
	s.res, s.cur = res, cur
	s.mu.Unlock()
}

// Next calls next() (Lua) and converts the row it returns.
func (s *fileSource) Next(scan bool) (exec.Row, bool, error) {
	if s.intr.Load() {
		return nil, false, exec.ErrCursorClosed
	}
	if s.limit >= 0 && s.n >= s.limit {
		return nil, false, nil
	}
	row, ok, err := s.rt.callNext(s)
	if ok {
		s.n++
	}
	return row, ok, err
}

// Close closes the file; it never calls Lua (it may run on the UI
// goroutine when a tab is closed).
func (s *fileSource) Close() error {
	s.rd.close()
	s.rt.srcMu.Lock()
	delete(s.rt.sources, s)
	s.rt.srcMu.Unlock()
	return nil
}

// Interrupt stops a read in flight from any goroutine: the file handle is
// closed and the importer's Lua code, if it runs outside a command, is
// cancelled. A command's own Lua code is stopped with F8 instead.
func (s *fileSource) Interrupt() {
	s.intr.Store(true)
	s.rd.close()
	s.rt.invMu.Lock()
	if s.rt.bgOwner == s && s.rt.bgStop != nil {
		s.rt.bgStop()
	}
	s.rt.invMu.Unlock()
}

// closeByRuntime closes the source because its runtime goes away
// (reload): the result tab becomes partial and reopens on paging.
func (s *fileSource) closeByRuntime() {
	s.mu.Lock()
	res, cur := s.res, s.cur
	s.mu.Unlock()
	if cur == nil {
		s.Close()
		return
	}
	if res != nil {
		res.MarkPartial(cur)
	}
	cur.Close()
}

// enterLua prepares the Lua state for importer code of source s and
// returns the function that undoes it. Inside a command the command's
// context stays (F8 interrupts the command, including a nested importer);
// outside one — grid paging and rerun on the worker — the state gets a
// cancellable context of its own, so F8 (Interrupt) and closing the
// result stop the importer's Lua code too.
func (rt *Runtime) enterLua(s *fileSource) func() {
	rt.invMu.Lock()
	if rt.inv != nil || rt.loading || rt.bgOwner != nil {
		rt.invMu.Unlock()
		return func() {}
	}
	if rt.bgCtx == nil || rt.bgCtx.Err() != nil {
		rt.bgCtx, rt.bgStop = context.WithCancel(context.Background())
	}
	rt.bgOwner = s
	ctx := rt.bgCtx
	rt.invMu.Unlock()
	rt.L.SetContext(ctx)
	return func() {
		rt.L.RemoveContext()
		rt.invMu.Lock()
		rt.bgOwner = nil
		rt.invMu.Unlock()
	}
}

// luaErr turns the error of a protected call of importer code into a Go
// error naming the file.
func (s *fileSource) luaErr(err error) error {
	if s.intr.Load() {
		return exec.ErrCursorClosed
	}
	msg := cleanLuaErr(err)
	if s.rt.goErr != nil && strings.Contains(msg, s.rt.goErrMsg) && errors.Is(s.rt.goErr, context.Canceled) {
		return fmt.Errorf("%s: %w", s.rd.name, context.Canceled)
	}
	if strings.Contains(msg, "context canceled") {
		return fmt.Errorf("%s: interrupted: %w", s.rd.name, context.Canceled)
	}
	return fmt.Errorf("import %s (%s): %s", s.rd.name, s.importer, msg)
}

// callNext runs next() of s and converts its row: strings as they are,
// numbers as dbcc.fmt.cell writes them, booleans true/false, nil NULL.
func (rt *Runtime) callNext(s *fileSource) (exec.Row, bool, error) {
	if rt.isClosed() {
		return nil, false, exec.ErrCursorClosed
	}
	release := rt.enterLua(s)
	defer release()
	L := rt.L
	if err := L.CallByParam(lua.P{Fn: s.next, NRet: 1, Protect: true}); err != nil {
		return nil, false, s.luaErr(err)
	}
	v := L.Get(-1)
	L.Pop(1)
	switch x := v.(type) {
	case *lua.LNilType:
		return nil, false, nil
	case *lua.LTable:
		return s.convert(x)
	}
	return nil, false, fmt.Errorf("import %s (%s): next must return a list or nil, got %s", s.rd.name, s.importer, v.Type())
}

func (s *fileSource) convert(t *lua.LTable) (exec.Row, bool, error) {
	n := t.MaxN()
	rec := s.n + 1
	if n > s.ncols {
		return nil, false, fmt.Errorf("import %s (%s): record %d has %d fields, more than the %d columns", s.rd.name, s.importer, rec, n, s.ncols)
	}
	row := make(exec.Row, s.ncols)
	for i := 1; i <= n; i++ {
		switch x := t.RawGetInt(i).(type) {
		case *lua.LNilType:
		case lua.LString:
			row[i-1] = string(x)
		case lua.LNumber:
			row[i-1] = formatNumber(float64(x))
		case lua.LBool:
			if x {
				row[i-1] = "true"
			} else {
				row[i-1] = "false"
			}
		default:
			return nil, false, fmt.Errorf("import %s (%s): record %d, column %d: a %s is not a value", s.rd.name, s.importer, rec, i, x.Type())
		}
	}
	return row, true, nil
}

func (rt *Runtime) isClosed() bool {
	rt.srcMu.Lock()
	defer rt.srcMu.Unlock()
	return rt.closed
}

// openImport opens path with importer imp: open(ctx, src, opts) runs and
// returns the columns and the source of the rows; limit < 0: all rows.
func (rt *Runtime) openImport(path string, imp *Importer, values map[string]any, limit int) (*fileSource, []exec.ColumnInfo, error) {
	rd, err := openFileReader(path)
	if err != nil {
		return nil, nil, err
	}
	s := &fileSource{rt: rt, rd: rd, importer: imp.Name, limit: limit}
	rt.srcMu.Lock()
	if rt.closed {
		rt.srcMu.Unlock()
		rd.close()
		return nil, nil, errors.New("the commands are being reloaded; try again")
	}
	rt.sources[s] = struct{}{}
	rt.srcMu.Unlock()
	fail := func(err error) (*fileSource, []exec.ColumnInfo, error) {
		s.Close()
		return nil, nil, err
	}
	L := rt.L
	optT := L.NewTable()
	for k, v := range values {
		optT.RawSetString(k, optionToLua(v))
	}
	release := rt.enterLua(s)
	err = L.CallByParam(lua.P{Fn: imp.fn, NRet: 1, Protect: true}, rt.newCtx(""), rd.luaTable(rt), optT)
	release()
	if err != nil {
		return fail(s.luaErr(err))
	}
	ret := L.Get(-1)
	L.Pop(1)
	t, ok := ret.(*lua.LTable)
	bad := func(msg string) (*fileSource, []exec.ColumnInfo, error) {
		return fail(fmt.Errorf("import type %s: open must return {columns = {…}, next = function}: %s", imp.Name, msg))
	}
	if !ok {
		return bad("got " + ret.Type().String())
	}
	next, ok := t.RawGetString("next").(*lua.LFunction)
	if !ok {
		return bad("next is not a function")
	}
	ct, ok := t.RawGetString("columns").(*lua.LTable)
	if !ok || ct.Len() == 0 {
		return bad("columns must be a non-empty list")
	}
	cols := make([]exec.ColumnInfo, 0, ct.Len())
	for i := 1; i <= ct.Len(); i++ {
		var c exec.ColumnInfo
		switch x := ct.RawGetInt(i).(type) {
		case lua.LString:
			c.Name = string(x)
		case *lua.LTable:
			c.Name = luaFieldString(x, "name")
			c.Numeric = lua.LVAsBool(x.RawGetString("numeric"))
			switch d := x.RawGetString("decimal").(type) {
			case *lua.LNilType:
			case lua.LString:
				switch {
				case d != "." && d != ",":
					return bad(fmt.Sprintf("columns[%d].decimal must be \".\" or \",\", got %q", i, string(d)))
				case !c.Numeric:
					// A separator only means something for a numeric column.
					return bad(fmt.Sprintf("columns[%d].decimal needs numeric = true", i))
				case d == ",":
					c.Decimal = ','
				}
			default:
				return bad(fmt.Sprintf("columns[%d].decimal must be \".\" or \",\"", i))
			}
		}
		if c.Name == "" {
			return bad(fmt.Sprintf("columns[%d] must be a non-empty name or {name=, numeric=, decimal=}", i))
		}
		cols = append(cols, c)
	}
	s.next, s.ncols = next, len(cols)
	return s, cols, nil
}

// ---- file arguments and settings ----

// fileArg reads argument n as a file: a file object from ctx:resolve, a
// "file:<path>" string or a plain path; it must be a path the owner chose
// (checked before the file system is touched). Returns the absolute path.
func (rt *Runtime) fileArg(L *lua.LState, n int, prim string) string {
	var p string
	switch v := L.Get(n).(type) {
	case lua.LString:
		p = strings.TrimPrefix(string(v), FilePrefix)
	case *lua.LTable:
		if luaFieldString(v, "kind") != "file" || luaFieldString(v, "path") == "" {
			L.ArgError(n, prim+": a file object from ctx:resolve(\"file:…\") or a path")
		}
		p = luaFieldString(v, "path")
	default:
		L.ArgError(n, prim+": a file object from ctx:resolve(\"file:…\") or a path")
	}
	if err := rt.checkChosenRead(prim, p); err != nil {
		rt.raise(err)
	}
	return exportPath(p)
}

// importerFor picks the import format and its options for path: from
// opts.importer / opts.options; without either, the settings remembered
// for the path in this session, else the format matching the extension
// with its defaults. Explicit settings are remembered.
func (rt *Runtime) importerFor(L *lua.LState, path string, opts *lua.LTable, n int) (*Importer, map[string]any) {
	name := ""
	var given map[string]any
	explicit := false
	if opts != nil {
		switch v := opts.RawGetString("importer").(type) {
		case *lua.LNilType:
		case lua.LString:
			name, explicit = string(v), true
		default:
			L.ArgError(n, "opts.importer must be a string")
		}
		switch v := opts.RawGetString("options").(type) {
		case *lua.LNilType:
		case *lua.LTable:
			var err error
			if given, err = optionValues(v, "opts.options"); err != nil {
				L.ArgError(n, err.Error())
			}
			explicit = true
		default:
			L.ArgError(n, "opts.options must be a table name -> value")
		}
	}
	files := rt.env.Files
	if !explicit {
		if st, ok := files.lookup(path); ok {
			name, given = st.importer, st.options
		}
	}
	var imp *Importer
	if name != "" {
		var ok bool
		if imp, ok = rt.Importer(name); !ok {
			rt.raise(rt.unknownImporter(name))
		}
	} else {
		imp = rt.importerByExtension(path)
		if imp == nil {
			rt.raise(rt.noImporterFor(path))
		}
	}
	values, err := ResolveOptions(imp.Options, given)
	if err != nil {
		rt.raise(fmt.Errorf("import type %s: %w", imp.Name, err))
	}
	if explicit {
		files.store(path, fileSettings{importer: imp.Name, options: given})
	}
	return imp, values
}

func (rt *Runtime) importerByExtension(path string) *Importer {
	low := strings.ToLower(path)
	for _, e := range rt.Importers() {
		for _, x := range e.Extensions {
			if strings.HasSuffix(low, "."+strings.ToLower(x)) {
				return e
			}
		}
	}
	return nil
}

func (rt *Runtime) importerNames() string {
	var names []string
	for _, e := range rt.Importers() {
		names = append(names, e.Name)
	}
	if len(names) == 0 {
		return "no import types are registered"
	}
	return "types: " + strings.Join(names, ", ")
}

func (rt *Runtime) unknownImporter(name string) error {
	return fmt.Errorf("unknown import type %q (%s)", name, rt.importerNames())
}

func (rt *Runtime) noImporterFor(path string) error {
	var exts []string
	for _, e := range rt.Importers() {
		for _, x := range e.Extensions {
			exts = append(exts, "."+x)
		}
	}
	sort.Strings(exts)
	return fmt.Errorf("no import type for %q (extensions: %s; %s)", filepath.Base(path), strings.Join(exts, ", "), rt.importerNames())
}

// ---- file results ----

// FileResult opens origin as a result: lazily read in chunks and pages,
// or — with origin.Tail — the last rows read completely into memory.
// title "" means the file's base name. Paging, export and rerun reopen
// the file through the runtime that is current then (Files.Use). Call it
// on the goroutine of the runtime (the worker in the UI).
func (rt *Runtime) FileResult(origin *exec.FileOrigin, title string) (*exec.Result, error) {
	imp, ok := rt.Importer(origin.Importer)
	if !ok {
		return nil, fmt.Errorf("import type %q is no longer registered; open the file again", origin.Importer)
	}
	if title == "" {
		title = filepath.Base(origin.Path)
	}
	s, cols, err := rt.openImport(origin.Path, imp, origin.Options, origin.RowLimit())
	if err != nil {
		return nil, err
	}
	files := rt.env.Files
	if origin.HasTail {
		rows, err := rt.tailRows(s, origin.Tail)
		s.Close()
		if err != nil {
			return nil, err
		}
		res := exec.NewResult(title, cols, rows)
		res.File = origin
		return res, nil
	}
	cur := exec.NewSourceCursor(s, cols)
	var res *exec.Result
	reopen := func(ctx context.Context, work func(c *exec.Cursor) error) error {
		now := files.current()
		if now == nil {
			now = rt
		}
		imp, ok := now.Importer(origin.Importer)
		if !ok {
			return fmt.Errorf("import type %q is no longer registered; open the file again", origin.Importer)
		}
		s2, cols2, err := now.openImport(origin.Path, imp, origin.Options, origin.RowLimit())
		if err != nil {
			return err
		}
		if !sameColumns(cols, cols2) {
			s2.Close()
			return exec.ErrColumnsChanged
		}
		c2 := exec.NewSourceCursor(s2, cols2)
		res.PartialOnClose(c2)
		s2.attach(res, c2)
		old := res.Cursor()
		if err := work(c2); err != nil {
			c2.Close()
			if res.Cursor() == c2 && old != nil && old != c2 {
				old.Close() // work moved the page to c2 before failing
			}
			return err
		}
		if res.Cursor() != c2 {
			c2.Close() // streamed (export), not attached
		} else if old != nil && old != c2 {
			old.Close() // the page moved to the new cursor
		}
		return nil
	}
	res = exec.NewFileResult(title, cols, cur, origin, files.PageSize, files.FetchSize, reopen)
	s.attach(res, cur)
	chunk := files.FetchSize
	if chunk <= 0 {
		chunk = 500
	}
	if _, err := res.Fetch(chunk); err != nil {
		res.Release()
		return nil, err
	}
	return res, nil
}

func sameColumns(a, b []exec.ColumnInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// tailRows reads s to the end keeping the last n rows (a ring).
func (rt *Runtime) tailRows(s *fileSource, n int) ([]exec.Row, error) {
	if n == 0 {
		return nil, nil
	}
	// The ring grows as rows arrive: tail file:small.csv 1000000000 must
	// not allocate for a billion rows first.
	ring := make([]exec.Row, 0, min(n, 1024))
	start := 0
	ctx := rt.gctx()
	for i := 0; ; i++ {
		if i%1024 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		row, ok, err := s.Next(true)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if len(ring) < n {
			ring = append(ring, row)
		} else {
			ring[start] = row
			start = (start + 1) % n
		}
	}
	return append(ring[start:], ring[:start]...), nil
}

// ---- primitives ----

// fileObject is the object ctx:resolve returns for file:<path>:
// {kind="file", name=<base name>, path=<absolute path>}; tostring = path.
func (rt *Runtime) fileObject(name string) *lua.LTable {
	p := exportPath(strings.TrimPrefix(name, FilePrefix))
	t := rt.L.NewTable()
	t.RawSetString("kind", lua.LString("file"))
	t.RawSetString("name", lua.LString(filepath.Base(p)))
	t.RawSetString("path", lua.LString(p))
	rt.L.SetMetatable(t, rt.fileMeta)
	return t
}

func isFileName(v lua.LValue) bool {
	switch x := v.(type) {
	case lua.LString:
		return strings.HasPrefix(string(x), FilePrefix)
	case *lua.LTable:
		return luaFieldString(x, "kind") == "file"
	}
	return false
}

// fileOrigin reads the file argument and opts of a file primitive.
func (rt *Runtime) fileOrigin(L *lua.LState, prim string) (*exec.FileOrigin, *lua.LTable) {
	path := rt.fileArg(L, 2, prim)
	opts := ensureTable(L, 3, "opts")
	imp, values := rt.importerFor(L, path, opts, 3)
	o := &exec.FileOrigin{Path: path, Importer: imp.Name, Options: values}
	return o, opts
}

// ctx:file_grid(file, opts) opens the file in a result tab (lazily read,
// paged); opts.importer, opts.options, opts.limit (first n rows),
// opts.tail (last n rows), opts.title.
func (rt *Runtime) apiFileGrid(L *lua.LState, self *lua.LTable) int {
	o, opts := rt.fileOrigin(L, "ctx:file_grid")
	title := ""
	if opts != nil {
		title = luaFieldString(opts, "title")
		for _, f := range []string{"limit", "tail"} {
			switch v := opts.RawGetString(f).(type) {
			case *lua.LNilType:
			case lua.LNumber:
				if v < 0 {
					L.ArgError(3, "opts."+f+" must not be negative")
				}
				// 0 is a limit too: no rows, only the columns (head t 0).
				if f == "limit" {
					o.Limit, o.HasLimit = int(v), true
				} else {
					o.Tail, o.HasTail = int(v), true
				}
			default:
				L.ArgError(3, "opts."+f+" must be a number")
			}
		}
	}
	res, err := rt.FileResult(o, title)
	if err != nil {
		return rt.raise(err)
	}
	rt.env.Host.Result(res)
	return 0
}

// ctx:file_count(file, opts) -> number of rows (read to the end, nothing kept).
func (rt *Runtime) apiFileCount(L *lua.LState, self *lua.LTable) int {
	o, _ := rt.fileOrigin(L, "ctx:file_count")
	imp, _ := rt.Importer(o.Importer)
	s, _, err := rt.openImport(o.Path, imp, o.Options, -1)
	if err != nil {
		return rt.raise(err)
	}
	defer s.Close()
	ctx := rt.gctx()
	n := 0
	for {
		if n%1024 == 0 && ctx.Err() != nil {
			return rt.raise(ctx.Err())
		}
		_, ok, err := s.Next(false)
		if err != nil {
			return rt.raise(err)
		}
		if !ok {
			break
		}
		n++
	}
	L.Push(lua.LNumber(n))
	return 1
}

// ctx:file_info(file, opts) -> {path=, name=, size=, modified=, importer=,
// options={…}, columns={ {name=, numeric=}, … }}.
func (rt *Runtime) apiFileInfo(L *lua.LState, self *lua.LTable) int {
	o, _ := rt.fileOrigin(L, "ctx:file_info")
	imp, _ := rt.Importer(o.Importer)
	s, cols, err := rt.openImport(o.Path, imp, o.Options, -1)
	if err != nil {
		return rt.raise(err)
	}
	s.Close()
	t := L.NewTable()
	t.RawSetString("path", lua.LString(o.Path))
	t.RawSetString("name", lua.LString(s.rd.name))
	t.RawSetString("size", lua.LNumber(s.rd.size))
	t.RawSetString("modified", lua.LString(s.rd.modified))
	t.RawSetString("importer", lua.LString(imp.Name))
	ot := L.NewTable()
	for k, v := range o.Options {
		ot.RawSetString(k, optionToLua(v))
	}
	t.RawSetString("options", ot)
	ct := L.CreateTable(len(cols), 0)
	for _, c := range cols {
		e := L.NewTable()
		e.RawSetString("name", lua.LString(c.Name))
		e.RawSetString("numeric", lua.LBool(c.Numeric))
		e.RawSetString("decimal", lua.LString(decimalName(c)))
		ct.Append(e)
	}
	t.RawSetString("columns", ct)
	L.Push(t)
	return 1
}

// decimalName is the decimal separator of a column as Lua sees it.
func decimalName(c exec.ColumnInfo) string {
	if c.Decimal == ',' {
		return ","
	}
	return "."
}

// ctx:open_file(opts) -> path, or nil when cancelled: the file dialog in
// open mode (opts.title, opts.dir, opts.extensions). The path becomes a
// chosen path of this invocation.
func (rt *Runtime) apiOpenFile(L *lua.LState, self *lua.LTable) int {
	opts := ensureTable(L, 2, "opts")
	so := SaveFileOptions{Title: rt.title(opts)}
	if opts != nil {
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
			L.ArgError(2, "ctx:open_file: opts.extensions must be a list")
		}
	}
	path, ok, err := rt.env.Host.OpenFile(so)
	if err != nil {
		return rt.raise(fmt.Errorf("ctx:open_file: %w", err))
	}
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	rt.inv.chosen[exportPath(path)] = true
	L.Push(lua.LString(path))
	return 1
}
