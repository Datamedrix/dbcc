package session

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"dbcc/internal/config"
	"dbcc/internal/meta"
)

// Workspace holds all open profiles and the current path (spec 6.3:
// several profiles can be open at once; cd /other-connection/db switches).
type Workspace struct {
	Config *config.Config
	Prompt config.Prompter
	// OnOpen is called after a profile was connected (e.g. to run hooks).
	OnOpen func(s *Session)
	// BeforeDatabaseChange is called when cd changes the database of a
	// session with an open transaction (spec 12: commit, roll back or
	// stay). Returning an error aborts the cd. Nil means "proceed".
	BeforeDatabaseChange func(s *Session, target string) error

	mu       sync.Mutex
	sessions map[string]*Session
	current  *Session
	path     meta.Path
	prev     meta.Path
}

// NewWorkspace returns an empty workspace.
func NewWorkspace(cfg *config.Config, prompt config.Prompter) *Workspace {
	return &Workspace{Config: cfg, Prompt: prompt, sessions: map[string]*Session{}}
}

// Current returns the session of the current path, or nil at "/".
func (w *Workspace) Current() *Session { w.mu.Lock(); defer w.mu.Unlock(); return w.current }

// Path returns the current path.
func (w *Workspace) Path() meta.Path { w.mu.Lock(); defer w.mu.Unlock(); return w.path }

// Sessions returns the open sessions by profile name.
func (w *Workspace) Sessions() map[string]*Session {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]*Session, len(w.sessions))
	for k, v := range w.sessions {
		out[k] = v
	}
	return out
}

// Attach registers an already opened session and makes it current.
func (w *Workspace) Attach(s *Session) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions[s.Profile.Name] = s
	w.prev = w.path
	w.current = s
	w.path = meta.Path{Conn: s.Profile.Name, Database: s.Database()}
}

// Open connects a profile (or returns the open session) without changing the path.
func (w *Workspace) Open(ctx context.Context, name string) (*Session, error) {
	w.mu.Lock()
	s, ok := w.sessions[name]
	w.mu.Unlock()
	if ok {
		return s, nil
	}
	p, err := w.Config.Profile(name)
	if err != nil {
		return nil, err
	}
	cred, err := config.ResolveCredentials(p, w.Prompt)
	if err != nil {
		return nil, err
	}
	s, err = Open(ctx, p, cred, "")
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}
	if cred.PromptedForStore {
		// password = "stored" but nothing was stored yet: keep the password
		// that just worked (owner decision A22).
		if err := config.StorePassword(name, cred.Password); err != nil {
			s.Close()
			return nil, fmt.Errorf("store password for %s: %w", name, err)
		}
	}
	w.mu.Lock()
	w.sessions[name] = s
	w.mu.Unlock()
	if w.OnOpen != nil {
		w.OnOpen(s)
	}
	return s, nil
}

// Close closes one profile's session.
func (w *Workspace) Close(name string) error {
	w.mu.Lock()
	s, ok := w.sessions[name]
	delete(w.sessions, name)
	if w.current == s {
		w.current = nil
		w.path = meta.Path{}
	}
	w.mu.Unlock()
	if ok {
		return s.Close()
	}
	return nil
}

// CloseAll closes every session.
func (w *Workspace) CloseAll() {
	for name := range w.Sessions() {
		w.Close(name)
	}
}

// Resolve turns a path argument into an absolute path without side effects.
// Supported: "/", "/conn", "/conn/db", "/conn/db/schema", "..", "-",
// relative names and combinations ("../other", "db/schema").
func (w *Workspace) Resolve(arg string) (meta.Path, error) {
	w.mu.Lock()
	cur, prev := w.path, w.prev
	hasSchemas := w.current != nil && w.current.Dialect.HasSchemas()
	w.mu.Unlock()
	arg = strings.TrimSpace(arg)
	if arg == "-" {
		return prev, nil
	}
	var parts []string
	if !strings.HasPrefix(arg, "/") {
		for _, s := range []string{cur.Conn, cur.Database, cur.Schema} {
			if s == "" {
				break
			}
			parts = append(parts, s)
		}
	}
	for _, seg := range strings.Split(arg, "/") {
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
	max := 2
	if hasSchemas || (len(parts) > 0 && parts[0] != cur.Conn) {
		max = 3 // depth is checked again after connecting
	}
	if len(parts) > max {
		return meta.Path{}, fmt.Errorf("no such path: /%s", strings.Join(parts, "/"))
	}
	var p meta.Path
	if len(parts) > 0 {
		p.Conn = parts[0]
	}
	if len(parts) > 1 {
		p.Database = parts[1]
	}
	if len(parts) > 2 {
		p.Schema = parts[2]
	}
	return p, nil
}

// Chdir changes the current path: switches or opens the connection and
// changes the database (USE or reconnect) as required.
func (w *Workspace) Chdir(ctx context.Context, arg string) (meta.Path, error) {
	p, err := w.Resolve(arg)
	if err != nil {
		return meta.Path{}, err
	}
	if p.Conn == "" {
		w.mu.Lock()
		w.prev, w.path, w.current = w.path, meta.Path{}, nil
		w.mu.Unlock()
		return meta.Path{}, nil
	}
	s, err := w.Open(ctx, p.Conn)
	if err != nil {
		return meta.Path{}, err
	}
	if p.Schema != "" && !s.Dialect.HasSchemas() {
		return meta.Path{}, fmt.Errorf("%s has no schema level: %s", s.Dialect.Name(), p)
	}
	w.mu.Lock()
	switching := w.path.Conn != p.Conn
	w.mu.Unlock()
	dbChanged := false
	if p.Database == "" {
		// Switching to another connection lands on its current database;
		// "cd .." or "cd /conn" within the same connection stays on the
		// connection level (ls lists the databases there).
		if switching {
			p.Database = s.Database()
		}
	} else if p.Database != s.Database() {
		if s.TxnOpen() && w.BeforeDatabaseChange != nil {
			if err := w.BeforeDatabaseChange(s, p.Database); err != nil {
				return meta.Path{}, err
			}
		}
		if err := s.ChangeDatabase(ctx, p.Database); err != nil {
			return meta.Path{}, err
		}
		dbChanged = true
	}
	if p.Schema != "" {
		if err := s.ChangeSchema(ctx, p.Schema); err != nil {
			return meta.Path{}, err
		}
	} else if s.Dialect.HasSchemas() && p.Database != "" && (switching || dbChanged) {
		// Entering a database (or connection) lands on its current
		// schema; "cd .." from a schema stays on the database level.
		p.Schema = s.Schema()
	}
	w.mu.Lock()
	w.prev, w.path, w.current = w.path, p, s
	w.mu.Unlock()
	return p, nil
}
