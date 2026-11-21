// Command licenses writes internal/branding/THIRD_PARTY_LICENSES.txt: the
// licence notices of every third-party module compiled into dbcc (spec 7.1,
// A16).
//
// Run it from the module root with `make licenses`, or
//
//	go run ./internal/branding/gen/licenses [-o file]
//
// The module list comes from `go list -deps ./cmd/dbcc` for the release
// target (GOOS=linux GOARCH=amd64 CGO_ENABLED=0, see the Makefile); the
// texts come from the LICENSE/LICENCE/COPYING files in the module cache.
// A module without such a file inherits the file of its nearest parent
// module in the cache (golang.org/x/... style splits). The Go standard
// library and runtime, which are linked in as well, get their notice from
// $(go env GOROOT)/LICENSE.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Separator frames each licence header. The layout is parsed by
// internal/branding (licenses.go): the summary table starts at the line
// whose fields are "Module Version Licence" and ends at the first blank
// line. Keep both in sync.
const Separator = "================================================================================"

// StdModule is the summary entry for the Go standard library and runtime.
const (
	StdModule  = "std"
	StdVersion = "(toolchain)"
)

// Module is one compiled-in module and its licence.
type Module struct {
	Path, Version, Dir string
	Licence            string // SPDX-like identifier
	File               string // licence file, relative to the module cache or GOROOT
	Text               string
}

func main() {
	out := flag.String("o", "internal/branding/THIRD_PARTY_LICENSES.txt", `output file ("-" for stdout)`)
	pkg := flag.String("pkg", "./cmd/dbcc", "main package whose dependencies are listed")
	flag.Parse()
	mods, err := Collect(*pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "licenses:", err)
		os.Exit(1)
	}
	text := Render(mods)
	if *out == "-" {
		os.Stdout.WriteString(text)
		return
	}
	if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "licenses:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "licenses: %d modules written to %s\n", len(mods)-1, *out)
}

// ReleaseEnv is the build environment of the release binary (Makefile).
var ReleaseEnv = []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=-mod=readonly"}

func goCmd(args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), ReleaseEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out), nil
}

// ListFormat prints "path version dir" for every non-main module of a
// package in the dependency graph.
const ListFormat = `{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}`

// ListModules returns the third-party modules compiled into pkg, sorted by
// path, as "path version" plus the source directory.
func ListModules(pkg string) ([]Module, error) {
	out, err := goCmd("list", "-deps", "-f", ListFormat, pkg)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var mods []Module
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		m := Module{Path: f[0], Version: f[1]}
		if len(f) > 2 {
			m.Dir = strings.Join(f[2:], " ")
		}
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// Collect lists the modules and reads their licence texts. The first entry
// is the Go standard library.
func Collect(pkg string) ([]Module, error) {
	mods, err := ListModules(pkg)
	if err != nil {
		return nil, err
	}
	env, err := goCmd("env", "GOMODCACHE", "GOROOT")
	if err != nil {
		return nil, err
	}
	ev := strings.Split(strings.TrimSpace(env), "\n")
	if len(ev) != 2 {
		return nil, fmt.Errorf("go env: unexpected output %q", env)
	}
	cache, goroot := ev[0], ev[1]

	std := Module{Path: StdModule, Version: StdVersion, Dir: goroot}
	if err := readLicence(&std, goroot); err != nil {
		return nil, fmt.Errorf("Go standard library: %w", err)
	}
	std.File = "GOROOT/" + std.File
	var errs []error
	for i := range mods {
		m := &mods[i]
		if err := findLicence(m, cache); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", m.Path, m.Version, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return append([]Module{std}, mods...), nil
}

// licenceNames are tried in order in a module directory.
var licenceNames = []string{
	"LICENSE", "LICENSE.txt", "LICENSE.md", "LICENCE", "LICENCE.txt", "LICENCE.md",
	"COPYING", "COPYING.txt", "COPYING.md", "LICENSE-MIT", "LICENSE.MIT",
}

func readLicence(m *Module, dir string) error {
	for _, n := range licenceNames {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		m.Text = normalise(string(b))
		m.File = n
		m.Licence = Detect(m.Text)
		if m.Licence == "" {
			return fmt.Errorf("%s: licence type not recognised", filepath.Join(dir, n))
		}
		return nil
	}
	return errors.New("no licence file")
}

// findLicence looks in the module directory, then in the cached versions of
// each parent module path (nearest first, newest version first).
func findLicence(m *Module, cache string) error {
	if m.Dir == "" {
		m.Dir = filepath.Join(cache, EscapePath(m.Path)+"@"+m.Version)
	}
	if err := readLicence(m, m.Dir); err == nil {
		m.File = path.Join(m.Path+"@"+m.Version, m.File)
		return nil
	} else if !strings.Contains(err.Error(), "no licence file") {
		return err
	}
	for p := path.Dir(m.Path); p != "." && p != "/" && strings.Contains(p, "/"); p = path.Dir(p) {
		dirs, _ := filepath.Glob(filepath.Join(cache, EscapePath(p)+"@*"))
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		for _, d := range dirs {
			if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
				continue
			}
			if err := readLicence(m, d); err == nil {
				m.File = path.Join(filepath.Base(filepath.Dir(d)), filepath.Base(d), m.File) + " (parent module)"
				return nil
			}
		}
	}
	return fmt.Errorf("no licence file in %s or a parent module", m.Dir)
}

// EscapePath applies the module cache's case encoding: an upper-case
// letter becomes '!' plus the lower-case letter.
func EscapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// normalise uses LF line ends, strips trailing blanks on lines and at the
// end, and expands tabs (the About viewer draws cells, not tabs).
func normalise(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.ReplaceAll(l, "\t", "    "), " ")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// Detect classifies a licence text. It returns "" when the text matches no
// known licence.
func Detect(text string) string {
	t := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	has := func(s ...string) bool {
		for _, x := range s {
			if !strings.Contains(t, x) {
				return false
			}
		}
		return true
	}
	switch {
	case has("mozilla public license") && (has("version 2.0") || has("v. 2.0")):
		return "MPL-2.0"
	case has("apache license") && has("version 2.0"):
		return "Apache-2.0"
	case has("gnu lesser general public license"):
		return "LGPL"
	case has("gnu general public license"):
		return "GPL"
	case has("permission is hereby granted, free of charge"):
		return "MIT"
	case has("permission to use, copy, modify, and/or distribute this software for any purpose"),
		has("permission to use, copy, modify, and distribute this software for any purpose with or without fee"):
		return "ISC"
	case has("redistribution and use in source and binary forms"):
		if has("neither the name") || has("names of its contributors") || has("name of the copyright holder") {
			return "BSD-3-Clause"
		}
		return "BSD-2-Clause"
	case has("this is free and unencumbered software released into the public domain"):
		return "Unlicense"
	}
	return ""
}

// RepoURL is where the source of a module is published.
func RepoURL(modPath string) string {
	parts := strings.Split(modPath, "/")
	switch parts[0] {
	case "github.com", "gitlab.com", "bitbucket.org":
		if len(parts) >= 3 {
			return "https://" + strings.Join(parts[:3], "/")
		}
	case "golang.org":
		if len(parts) >= 3 && parts[1] == "x" {
			return "https://go.googlesource.com/" + parts[2]
		}
	}
	return "https://pkg.go.dev/" + modPath
}

// Render produces the file. mods[0] is the standard library.
func Render(mods []Module) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("THIRD-PARTY LICENCES\n")
	w("====================\n\n")
	w("dbcc is built with the Go toolchain and the following third-party Go\n")
	w("modules. Their licence notices follow the summary.\n\n")
	w("Generated by `make licenses` (internal/branding/gen/licenses) from the\n")
	w("module cache; do not edit by hand.\n\n")
	pw, vw := len("Module"), len("Version")
	for _, m := range mods {
		pw, vw = max(pw, len(m.Path)), max(vw, len(m.Version))
	}
	w("%-*s  %-*s  %s\n", pw, "Module", vw, "Version", "Licence")
	for _, m := range mods {
		w("%-*s  %-*s  %s\n", pw, m.Path, vw, m.Version, m.Licence)
	}
	w("\n")
	var mpl []Module
	for _, m := range mods {
		if m.Licence == "MPL-2.0" {
			mpl = append(mpl, m)
		}
	}
	if len(mpl) > 0 {
		w("Source code of MPL-2.0 licensed modules (MPL-2.0 section 3.2): dbcc uses\n")
		w("them unmodified; their Source Code Form is available at\n")
		for _, m := range mpl {
			w("  %s %s: %s\n", m.Path, m.Version, RepoURL(m.Path))
		}
		w("\n")
	}
	for _, m := range mods {
		w("%s\n", Separator)
		if m.Path == StdModule {
			w("Go standard library and runtime (%s)\n", m.Path)
		} else {
			w("%s %s\n", m.Path, m.Version)
		}
		w("Licence: %s, from %s\n", m.Licence, m.File)
		if m.Licence == "MPL-2.0" {
			w("Source code: %s (unmodified)\n", RepoURL(m.Path))
		}
		w("%s\n\n", Separator)
		w("%s\n\n", m.Text)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
