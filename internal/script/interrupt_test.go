package script

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestInterruptStopsEndlessLoop(t *testing.T) {
	rt, _ := newTestRuntime()
	defer rt.Close()
	if _, err := rt.Load("spin.lua", `dbcc.command{ name = "spin", usage = "spin", help = "x", args = {},
	  run = function(ctx, args) while true do end end }`, false); err != nil {
		t.Fatal(err)
	}
	cmd, _ := rt.Command("spin")
	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background(), cmd, nil) }()
	time.Sleep(100 * time.Millisecond)
	if !rt.Interrupt() {
		t.Fatal("Interrupt found no running command")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("endless loop not interrupted")
	}
	if rt.Interrupt() {
		t.Fatal("Interrupt with nothing running reported true")
	}
}
