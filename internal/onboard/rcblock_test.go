package onboard

import (
	"strings"
	"testing"
)

func TestInsert(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantHas []string // substrings the result must contain
		wantEnd string   // result must end with this
	}{
		{
			name:    "empty file",
			in:      "",
			wantHas: []string{BeginMarker, "newsfetch", EndMarker},
			wantEnd: "\n",
		},
		{
			name:    "existing content without trailing newline",
			in:      "export FOO=bar",
			wantHas: []string{"export FOO=bar", BeginMarker, EndMarker},
			wantEnd: "\n",
		},
		{
			name:    "existing content with trailing newline",
			in:      "export FOO=bar\n",
			wantHas: []string{"export FOO=bar", BeginMarker, EndMarker},
			wantEnd: "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Insert(tc.in)
			if !changed {
				t.Fatalf("Insert reported no change; want change")
			}
			for _, s := range tc.wantHas {
				if !strings.Contains(got, s) {
					t.Errorf("result missing %q; got:\n%s", s, got)
				}
			}
			if !strings.HasSuffix(got, tc.wantEnd) {
				t.Errorf("result should end with %q; got tail %q", tc.wantEnd, got[max(0, len(got)-5):])
			}
			// Preserves original content
			if tc.in != "" && !strings.Contains(got, strings.TrimRight(tc.in, "\n")) {
				t.Errorf("result lost original content; got:\n%s", got)
			}
		})
	}
}

func TestInsertIsIdempotent(t *testing.T) {
	initial := "# my rc file\nalias ll='ls -l'\n"
	once, _ := Insert(initial)
	twice, changed := Insert(once)
	if changed {
		t.Errorf("second Insert should report no change")
	}
	if once != twice {
		t.Errorf("second Insert changed content:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

func TestInsertReplacesExistingBlock(t *testing.T) {
	// Simulate an older version of the block with stale content between markers.
	stale := "alias ll='ls -l'\n\n" + BeginMarker + "\noldcommand --with-args\n" + EndMarker + "\n"
	got, changed := Insert(stale)
	if !changed {
		t.Errorf("Insert over stale block should report change")
	}
	if strings.Contains(got, "oldcommand --with-args") {
		t.Errorf("stale content survived: %s", got)
	}
	if !strings.Contains(got, "alias ll='ls -l'") {
		t.Errorf("surrounding content lost: %s", got)
	}
	if strings.Count(got, BeginMarker) != 1 || strings.Count(got, EndMarker) != 1 {
		t.Errorf("expected exactly one marker pair; got:\n%s", got)
	}
}

func TestRemove(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantChanged bool
		wantNotHas  []string
		wantHas     []string
	}{
		{
			name:        "block present at end",
			in:          "alias ll='ls -l'\n\n" + BeginMarker + "\nnewsfetch\n" + EndMarker + "\n",
			wantChanged: true,
			wantNotHas:  []string{BeginMarker, EndMarker, "newsfetch"},
			wantHas:     []string{"alias ll='ls -l'"},
		},
		{
			name:        "block present in middle",
			in:          "# top\n" + BeginMarker + "\nnewsfetch\n" + EndMarker + "\n# bottom\n",
			wantChanged: true,
			wantNotHas:  []string{BeginMarker, EndMarker},
			wantHas:     []string{"# top", "# bottom"},
		},
		{
			name:        "block absent",
			in:          "alias ll='ls -l'\n",
			wantChanged: false,
			wantHas:     []string{"alias ll='ls -l'"},
		},
		{
			name:        "empty file",
			in:          "",
			wantChanged: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Remove(tc.in)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			for _, s := range tc.wantNotHas {
				if strings.Contains(got, s) {
					t.Errorf("result unexpectedly contains %q; got:\n%s", s, got)
				}
			}
			for _, s := range tc.wantHas {
				if !strings.Contains(got, s) {
					t.Errorf("result missing %q; got:\n%s", s, got)
				}
			}
			if !tc.wantChanged && got != tc.in {
				t.Errorf("no-change case modified content:\nin:  %q\ngot: %q", tc.in, got)
			}
		})
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	in := "alias ll='ls -l'\n\n" + BeginMarker + "\nnewsfetch\n" + EndMarker + "\n"
	once, _ := Remove(in)
	twice, changed := Remove(once)
	if changed {
		t.Errorf("second Remove should report no change")
	}
	if once != twice {
		t.Errorf("second Remove changed content")
	}
}

func TestRoundTripInsertRemove(t *testing.T) {
	orig := "# rc file\nexport PATH=/usr/local/bin:$PATH\nalias ll='ls -l'\n"
	inserted, _ := Insert(orig)
	removed, _ := Remove(inserted)
	if removed != orig {
		t.Errorf("round trip did not restore original:\norig:    %q\nremoved: %q", orig, removed)
	}
}
