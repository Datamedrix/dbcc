package export

import (
	"bytes"
	"fmt"
	"testing"
)

func cellText(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprint(v)
}

func TestStreamFormats(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{"table", "id | msg\n---+----\n 1 | a\n 2 | bb\n" + " 3 | c\n" + "10 | longer\n"},
		{"csv", "id,msg\n1,a\n2,bb\n3,c\n10,longer\n"},
		{"tsv", "id\tmsg\n1\ta\n2\tbb\n3\tc\n10\tlonger\n"},
		{"json", `{"id": "1", "msg": "a"}` + "\n" + `{"id": "2", "msg": "bb"}` + "\n" +
			`{"id": "3", "msg": "c"}` + "\n" + `{"id": "10", "msg": "longer"}` + "\n"},
	} {
		var b bytes.Buffer
		st, err := NewStream(tc.format, &b, []bool{true, false}, cellText)
		if err != nil {
			t.Fatal(err)
		}
		st.Header([]string{"id", "msg"})
		st.Row([]any{1, "a"})
		st.Row([]any{2, "bb"})
		if err := st.Flush(); err != nil {
			t.Fatal(err)
		}
		first := b.String()
		st.Row([]any{3, "c"})
		st.Flush()
		st.Row([]any{10, "longer"})
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if b.String() != tc.want {
			t.Errorf("%s:\n%q\nwant\n%q", tc.format, b.String(), tc.want)
		}
		if len(first) == 0 || first != tc.want[:len(first)] {
			t.Errorf("%s: first block not written by Flush: %q", tc.format, first)
		}
	}
}
