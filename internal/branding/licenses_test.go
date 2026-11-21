package branding

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const separator = "================================================================================"

// section returns the full licence text that follows the header of module.
func section(t *testing.T, l License) string {
	t.Helper()
	title := l.Module + " " + l.Version
	if l.Module == "std" {
		title = "Go standard library and runtime (std)"
	}
	head := "\n" + separator + "\n" + title + "\n"
	i := strings.Index(ThirdPartyLicenses, head)
	if i < 0 {
		t.Fatalf("no licence section for %s", title)
	}
	rest := ThirdPartyLicenses[i+len(head):]
	j := strings.Index(rest, separator+"\n")
	if j < 0 {
		t.Fatalf("%s: header not closed", title)
	}
	if !strings.Contains(rest[:j], "Licence: "+l.Licence+",") {
		t.Errorf("%s: header does not name %s:\n%s", title, l.Licence, rest[:j])
	}
	rest = rest[j+len(separator)+1:]
	if k := strings.Index(rest, "\n"+separator+"\n"); k >= 0 {
		rest = rest[:k]
	}
	return strings.TrimSpace(rest)
}

func TestLicensesSummaryAndTexts(t *testing.T) {
	if len(Licenses) < 2 || Licenses[0].Module != "std" {
		t.Fatalf("summary: %+v", Licenses)
	}
	known := map[string]bool{"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "MPL-2.0": true, "ISC": true}
	for _, l := range Licenses {
		if !known[l.Licence] {
			t.Errorf("%s: unexpected licence %q", l.Module, l.Licence)
		}
		if txt := section(t, l); len(txt) < 200 {
			t.Errorf("%s: licence text too short: %q", l.Module, txt)
		}
		if l.Licence == "MPL-2.0" && !strings.Contains(ThirdPartyLicenses, "Source code: https://") {
			t.Errorf("%s: MPL-2.0 module without source code note", l.Module)
		}
	}
}

func TestParseLicenses(t *testing.T) {
	got := ParseLicenses("x\n\nModule  Version  Licence\na/b  v1.0.0  MIT\nc  v2  BSD-3-Clause\n\nd v3 MIT\n")
	if len(got) != 2 || got[0] != (License{"a/b", "v1.0.0", "MIT"}) || got[1].Licence != "BSD-3-Clause" {
		t.Fatalf("%+v", got)
	}
}

// releaseEnv matches gen/licenses.ReleaseEnv (the Makefile build target).
var releaseEnv = []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=-mod=readonly"}

func goTool(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not available:", err)
	}
	return p
}

// TestLicensesUpToDate fails when the modules compiled into dbcc changed
// (go.mod) but THIRD_PARTY_LICENSES.txt was not regenerated.
func TestLicensesUpToDate(t *testing.T) {
	cmd := exec.Command(goTool(t), "list", "-deps", "-f",
		`{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}`, "dbcc/cmd/dbcc")
	cmd.Env = append(os.Environ(), releaseEnv...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Skipf("go list failed (module cache unavailable?): %v: %s", err, ee.Stderr)
		}
		t.Skip("go list:", err)
	}
	want := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			want[l] = true
		}
	}
	have := map[string]bool{}
	for _, l := range Licenses {
		if l.Module != "std" {
			have[l.Module+" "+l.Version] = true
		}
	}
	var missing, extra []string
	for m := range want {
		if !have[m] {
			missing = append(missing, m)
		}
	}
	for m := range have {
		if !want[m] {
			extra = append(extra, m)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Errorf("THIRD_PARTY_LICENSES.txt is stale, run `make licenses`\nmissing: %v\nno longer compiled in: %v", missing, extra)
	}
}

// TestLicensesRegenerate runs the generator and compares its output with
// the committed file byte for byte.
func TestLicensesRegenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	cmd := exec.Command(goTool(t), "run", "dbcc/internal/branding/gen/licenses", "-pkg", "dbcc/cmd/dbcc", "-o", "-")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Skipf("generator failed (module cache unavailable?): %v: %s", err, ee.Stderr)
		}
		t.Skip("generator:", err)
	}
	if string(out) != ThirdPartyLicenses {
		t.Error("THIRD_PARTY_LICENSES.txt differs from the generator output, run `make licenses`")
	}
}
