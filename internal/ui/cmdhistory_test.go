package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestCmdlineHistoryPersists(t *testing.T) {
	state := t.TempDir()
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, StateDir: state})
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	for _, line := range []string{"pwd", "help ls", "ALTER USER x IDENTIFIED BY 'secret'"} {
		scr.InjectKey(tcell.KeyF9, 0, tcell.ModNone)
		for _, r := range line {
			scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
		}
		scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
		time.Sleep(150 * time.Millisecond)
	}
	stop()
	data, _ := os.ReadFile(filepath.Join(state, cmdHistoryFile))
	if got := string(data); got != "pwd\nhelp ls\n" {
		t.Fatalf("history file %q (secrets must not be written)", got)
	}
	if st, err := os.Stat(filepath.Join(state, cmdHistoryFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode: %v %v", st, err)
	}
	// Restart: Up shows the last saved line.
	a2, scr2, stop2 := startApp(t, emptyConfig(), Options{NoSplash: true, StateDir: state})
	defer stop2()
	waitFor(t, a2, scr2, "6RunAll", 2*time.Second)
	scr2.InjectKey(tcell.KeyF9, 0, tcell.ModNone)
	scr2.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	lines := waitFor(t, a2, scr2, "help ls", 2*time.Second)
	if !strings.Contains(strings.Join(lines, "\n"), "> help ls") {
		t.Fatalf("history not restored")
	}
}
