package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dbcc/internal/command"
)

func TestExportCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	var stderr bytes.Buffer
	if err := ExportCommands(dir, &stderr); err != nil {
		t.Fatal(err)
	}
	names, _ := command.DefaultScripts()
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s not exported: %v", n, err)
		}
	}
	// A second export must not overwrite anything.
	os.WriteFile(filepath.Join(dir, "ll.lua"), []byte("-- mine"), 0o600)
	err := ExportCommands(dir, &stderr)
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want refusal, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ll.lua")); string(b) != "-- mine" {
		t.Errorf("existing file overwritten")
	}
}
