package app

import (
	"bytes"
	"strings"
	"testing"

	"dbcc/internal/branding"
)

func TestLicensesFlag(t *testing.T) {
	o, err := ParseArgs([]string{"--licenses"}, &bytes.Buffer{})
	if err != nil || !o.Licenses {
		t.Fatalf("%+v %v", o, err)
	}
	if o, _ := ParseArgs(nil, &bytes.Buffer{}); o.Licenses {
		t.Fatal("licenses set without the flag")
	}
	var out bytes.Buffer
	if code := PrintLicenses(&out); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if out.String() != branding.ThirdPartyLicenses || !strings.Contains(out.String(), "github.com/rivo/tview") {
		t.Fatal("licence text not printed")
	}
}

func TestHeadlessAboutPointsToLicences(t *testing.T) {
	var out bytes.Buffer
	h := &headlessSink{env: &Env{Opts: &Options{Format: "table"}, Stdout: &out}}
	if err := h.About(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{branding.Title, branding.Company, branding.Web, "dbcc --licenses"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("About lacks %q:\n%s", s, out.String())
		}
	}
}
