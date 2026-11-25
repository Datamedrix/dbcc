package script

import (
	"strings"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/session"
)

// testHost records output for assertions.
type testHost struct {
	results  []*exec.Result
	texts    []string
	messages []exec.Message
	yes      bool
}

func (h *testHost) Result(r *exec.Result)          { h.results = append(h.results, r) }
func (h *testHost) Text(title, text, lexer string) { h.texts = append(h.texts, text) }
func (h *testHost) Message(m exec.Message)         { h.messages = append(h.messages, m) }
func (h *testHost) Confirm(string) (bool, error)   { return h.yes, nil }
func (h *testHost) Pick(string, []string) (int, error) {
	return -1, ErrNoUI
}
func (h *testHost) Form(string, []OptionSpec, map[string]any) (map[string]any, bool, error) {
	return nil, false, ErrNoUI
}
func (h *testHost) OpenFile(SaveFileOptions) (string, bool, error) { return "", false, ErrNoUI }
func (h *testHost) SaveFile(SaveFileOptions) (string, bool, error) { return "", false, ErrNoUI }
func (h *testHost) EditorSet(string) error                         { return ErrNoUI }
func (h *testHost) EditorOpen(string, string, *ObjectRef) error    { return nil }
func (h *testHost) About() error                                   { return nil }
func (h *testHost) ActiveResult() (*exec.Result, exec.View)        { return nil, nil }

func (h *testHost) infos() string {
	var b []string
	for _, m := range h.messages {
		b = append(b, m.Info)
	}
	return strings.Join(b, "\n")
}

func newTestRuntime() (*Runtime, *testHost) {
	h := &testHost{}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h})
	return rt, h
}
