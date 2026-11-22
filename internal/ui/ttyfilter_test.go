package ui

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/gdamore/tcell/v2/terminfo"
)

// fakeTty feeds byte chunks to tcell as a terminal would: one Read per
// chunk (one key press is one read).
type fakeTty struct {
	in chan []byte
}

func (f *fakeTty) Start() error        { return nil }
func (f *fakeTty) Stop() error         { return nil }
func (f *fakeTty) Drain() error        { return nil }
func (f *fakeTty) NotifyResize(func()) {}
func (f *fakeTty) WindowSize() (tcell.WindowSize, error) {
	return tcell.WindowSize{Width: 80, Height: 24}, nil
}
func (f *fakeTty) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeTty) Close() error                { return nil }
func (f *fakeTty) Read(p []byte) (int, error) {
	b, ok := <-f.in
	if !ok {
		return 0, io.EOF
	}
	return copy(p, b), nil
}

// keysThroughTcell sends the chunks through a real tcell terminfo screen
// and returns the key events it reports.
func keysThroughTcell(t *testing.T, wrap bool, chunks ...string) []*tcell.EventKey {
	t.Helper()
	ti, err := terminfo.LookupTerminfo("xterm-256color")
	if err != nil {
		t.Skip("no terminfo for xterm-256color")
	}
	ft := &fakeTty{in: make(chan []byte, len(chunks))}
	var tty tcell.Tty = ft
	if wrap {
		tty = newFilterTty(ft)
	}
	scr, err := tcell.NewTerminfoScreenFromTtyTerminfo(tty, ti)
	if err != nil {
		t.Fatal(err)
	}
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	// Fini waits for tcell's input loop: end it with EOF first.
	defer func() { close(ft.in); scr.Fini() }()
	for _, c := range chunks {
		ft.in <- []byte(c)
		time.Sleep(80 * time.Millisecond) // separate key presses (> tcell's ESC timeout)
	}
	var keys []*tcell.EventKey
	deadline := time.After(500 * time.Millisecond)
	evs := make(chan tcell.Event, 16)
	go func() {
		for {
			ev := scr.PollEvent()
			if ev == nil {
				close(evs)
				return
			}
			evs <- ev
		}
	}()
	for {
		select {
		case ev, ok := <-evs:
			if !ok {
				return keys
			}
			if k, ok := ev.(*tcell.EventKey); ok {
				keys = append(keys, k)
			}
		case <-deadline:
			return keys
		}
	}
}

// TestAltShiftXDoesNotSwallowInput: Alt+Shift+X sends ESC X, which tcell
// takes as the start of an SOS string and then swallows every following
// key until ESC \ — the UI looked frozen. The same holds for ESC ^ (PM),
// ESC _ (APC) and ESC ] (OSC); ESC N / ESC O swallow the next key.
func TestAltShiftXDoesNotSwallowInput(t *testing.T) {
	for _, seq := range []string{"\x1bX", "\x1b^", "\x1b_", "\x1b]", "\x1bN", "\x1bO"} {
		keys := keysThroughTcell(t, true, seq, "q", "\x11") // then q and Ctrl+Q
		var got []string
		for _, k := range keys {
			got = append(got, k.Name())
		}
		if len(keys) != 3 || keys[0].Rune() != rune(seq[1]) || keys[0].Modifiers()&tcell.ModAlt == 0 ||
			keys[1].Rune() != 'q' || keys[2].Key() != tcell.KeyCtrlQ {
			t.Errorf("%q: keys %v", seq, got)
		}
	}
	// Without the filter tcell swallows the following keys (documents the cause).
	if keys := keysThroughTcell(t, false, "\x1bX", "q", "\x11"); len(keys) != 0 {
		t.Logf("tcell no longer swallows after ESC X: %d keys", len(keys))
	}
}

func TestFixEscapesKeepsSequences(t *testing.T) {
	for in, want := range map[string]string{
		"\x1bX":              "\x1b[88;3u",
		"\x1b]\x1b[A":        "\x1b[93;3u\x1b[A",   // Alt+] then Up in one read
		"\x1bOA":             "\x1bOA",             // SS3 cursor key: unchanged
		"\x1b]52;c;eA==\x07": "\x1b]52;c;eA==\x07", // OSC response: unchanged
		"\x1b_Gi=1;OK\x1b\\": "\x1b_Gi=1;OK\x1b\\", // APC response: unchanged
		"\x1b\x1bX":          "\x1b\x1b[88;3u",
		"abc\x1bXd":          "abc\x1b[88;3ud", // no terminator follows: a key press
		"\x1b[A":             "\x1b[A",
	} {
		if got := fixEscapes([]byte(in)); !bytes.Equal(got, []byte(want)) {
			t.Errorf("fixEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}
