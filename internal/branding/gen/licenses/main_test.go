package main

import (
	"strings"
	"testing"

	"dbcc/internal/branding"
)

func TestDetect(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"The MIT License\n\nPermission is hereby granted, free of charge, to any person", "MIT"},
		{"Apache License\n  Version 2.0, January 2004", "Apache-2.0"},
		{"Mozilla Public License Version 2.0\n=====", "MPL-2.0"},
		{"Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted", "ISC"},
		{"Redistribution and use in source and binary forms, with or without\nmodification ... Neither the name of Google Inc.", "BSD-3-Clause"},
		{"Redistribution and use in source and binary forms, with or without modification, are permitted", "BSD-2-Clause"},
		{"All rights reserved. Do not copy.", ""},
	} {
		if got := Detect(c.text); got != c.want {
			t.Errorf("Detect(%.40q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestEscapePathAndRepoURL(t *testing.T) {
	if got := EscapePath("github.com/BurntSushi/toml"); got != "github.com/!burnt!sushi/toml" {
		t.Error(got)
	}
	for in, want := range map[string]string{
		"github.com/go-sql-driver/mysql": "https://github.com/go-sql-driver/mysql",
		"github.com/jackc/pgx/v5":        "https://github.com/jackc/pgx",
		"golang.org/x/text":              "https://go.googlesource.com/text",
		"filippo.io/edwards25519":        "https://pkg.go.dev/filippo.io/edwards25519",
	} {
		if got := RepoURL(in); got != want {
			t.Errorf("RepoURL(%s) = %s", in, got)
		}
	}
}

func TestRenderParses(t *testing.T) {
	mods := []Module{
		{Path: StdModule, Version: StdVersion, Licence: "BSD-3-Clause", File: "GOROOT/LICENSE", Text: "go text"},
		{Path: "example.com/a", Version: "v1.2.3", Licence: "MIT", File: "x", Text: "mit text"},
		{Path: "github.com/o/mpl", Version: "v0.1.0", Licence: "MPL-2.0", File: "y", Text: "mpl text"},
	}
	out := Render(mods)
	got := branding.ParseLicenses(out)
	if len(got) != 3 || got[1] != (branding.License{Module: "example.com/a", Version: "v1.2.3", Licence: "MIT"}) {
		t.Fatalf("%+v\n%s", got, out)
	}
	for _, s := range []string{"mit text", "mpl text", "Source code: https://github.com/o/mpl", Separator + "\nexample.com/a v1.2.3\n"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q", s)
		}
	}
}
