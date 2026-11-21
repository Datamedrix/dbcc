package ui

// Hang diagnostics: a watchdog notices when the UI goroutine stops
// answering and writes the stacks of all goroutines to the state
// directory; SIGQUIT writes the same dump on demand without exiting.

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

const (
	watchdogInterval = 2 * time.Second
	watchdogTimeout  = 5 * time.Second
)

// startWatchdog runs until a.quit is closed.
func (a *App) startWatchdog() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGQUIT)
	go func() {
		defer signal.Stop(sig)
		ticker := time.NewTicker(watchdogInterval)
		defer ticker.Stop()
		dumped := false
		for {
			select {
			case <-a.quit:
				return
			case <-sig:
				a.writeDump("signal")
			case <-ticker.C:
				pong := make(chan struct{})
				a.tv.QueueUpdate(func() { close(pong) })
				select {
				case <-pong:
					dumped = false
				case <-a.quit:
					return
				case <-time.After(watchdogTimeout):
					if !dumped { // one dump per hang
						dumped = true
						a.writeDump("ui-unresponsive")
					}
				}
			}
		}
	}()
}

// writeDump writes all goroutine stacks to <state>/hang-<time>-<reason>.txt.
func (a *App) writeDump(reason string) string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	dir := a.opts.StateDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	name := filepath.Join(dir, fmt.Sprintf("hang-%s-%s.txt", time.Now().Format("20060102-150405"), reason))
	busy := a.busy.Load()
	header := fmt.Sprintf("dbcc goroutine dump (%s) at %s\nworker busy: %d, queued jobs: %d\n\n",
		reason, time.Now().Format(time.RFC3339), busy, len(a.jobs))
	if err := os.WriteFile(name, append([]byte(header), buf...), 0o600); err != nil {
		return ""
	}
	return name
}
