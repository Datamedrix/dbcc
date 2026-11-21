package exec

import (
	"testing"
	"time"
)

func TestTimeText(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	mid := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	ts := time.Date(2024, 3, 1, 13, 4, 5, 120000000, loc)
	cases := []struct {
		v    time.Time
		typ  string
		want string
	}{
		{mid, "DATE", "2024-03-01"},
		{mid, "TIMESTAMP", "2024-03-01 00:00:00"},
		{mid, "", "2024-03-01"},
		{ts, "TIMESTAMPTZ", "2024-03-01 13:04:05.12+01:00"},
		{ts, "DATETIMEOFFSET", "2024-03-01 13:04:05.12+01:00"},
		{ts, "DATETIME2", "2024-03-01 13:04:05.12"},
		{ts, "TIME", "13:04:05.12"},
	}
	for _, c := range cases {
		if got := timeText(c.v, c.typ); got != c.want {
			t.Errorf("timeText(%v, %s) = %q, want %q", c.v, c.typ, got, c.want)
		}
	}
}
