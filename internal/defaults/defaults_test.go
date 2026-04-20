package defaults

import "testing"

func TestClampWidth(t *testing.T) {
	const fallback = 80
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero -> fallback", 0, fallback},
		{"negative -> fallback", -1, fallback},
		{"just below min -> fallback", 39, fallback},
		{"at min -> passes through", 40, 40},
		{"just above min -> passes through", 41, 41},
		{"mid-range -> passes through", 80, 80},
		{"just below max -> passes through", 99, 99},
		{"at max -> passes through", 100, 100},
		{"just above max -> clamps to 100", 101, 100},
		{"huge -> clamps to 100", 10_000, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampWidth(tc.in, fallback)
			if got != tc.want {
				t.Errorf("clampWidth(%d, %d) = %d, want %d", tc.in, fallback, got, tc.want)
			}
		})
	}
}

// TestTermWidth_NonTTYFallsBack exercises the path where x/term.GetSize
// returns an error (because go test redirects stdout to a pipe). In that
// case TermWidth must return the caller's fallback value verbatim.
func TestTermWidth_NonTTYFallsBack(t *testing.T) {
	if got := TermWidth(BoxWidth); got != BoxWidth {
		t.Errorf("TermWidth under non-TTY = %d, want %d (fallback)", got, BoxWidth)
	}
	if got := TermWidth(73); got != 73 {
		t.Errorf("TermWidth under non-TTY should echo fallback; got %d want 73", got)
	}
}
