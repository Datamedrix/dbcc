package script

import (
	"strings"
	"testing"
)

func TestDescribeParams(t *testing.T) {
	if describeParams(nil) != "" {
		t.Fatal("no parameters must render nothing")
	}
	got := describeParams([]any{"2026-01-01", nil, int64(5), "it's"})
	for _, want := range []string{"with parameters:", "1: '2026-01-01'", "2: NULL", "3: 5", "4: 'it''s'"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}
