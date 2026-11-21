package main

import (
	"os"
	"os/exec"
	"testing"

	"dbcc/internal/branding"
)

// TestMain lets the test binary act as dbcc when DBCC_TEST_MAIN is set.
func TestMain(m *testing.M) {
	if os.Getenv("DBCC_TEST_MAIN") == "1" {
		os.Args = append([]string{"dbcc"}, os.Args[1:]...)
		os.Exit(run())
	}
	os.Exit(m.Run())
}

func TestLicensesFlagPrintsAndExits0(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--licenses")
	cmd.Env = append(os.Environ(), "DBCC_TEST_MAIN=1", "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dbcc --licenses: %v", err)
	}
	if string(out) != branding.ThirdPartyLicenses {
		t.Fatalf("output differs from the embedded licences (%d bytes)", len(out))
	}
}
