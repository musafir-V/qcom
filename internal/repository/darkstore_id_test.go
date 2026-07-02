package repository

import "testing"

func TestFormatDarkstoreID(t *testing.T) {
	cases := map[int64]string{
		1:    "001",
		42:   "042",
		221:  "221",
		999:  "999",
		1000: "1000",
	}
	for n, want := range cases {
		if got := formatDarkstoreID(n); got != want {
			t.Errorf("formatDarkstoreID(%d) = %q, want %q", n, got, want)
		}
	}
}
