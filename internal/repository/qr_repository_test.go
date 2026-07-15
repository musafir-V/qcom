package repository

import "testing"

func TestJoinComma(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a, b"},
		{[]string{"a", "b", "c"}, "a, b, c"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := joinComma(c.in); got != c.want {
			t.Fatalf("joinComma(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
