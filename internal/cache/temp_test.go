package cache

import "testing"

// TestTempPattern pins that Write's temp file is named after its target.
// With one cache file the hardcoded "feed-*.json.tmp" was harmless; with
// a file per pool it is a lie — a following-pool write killed between
// os.CreateTemp and os.Rename would leave debris named as if the news
// cache had crashed, in the same directory as the real feed.json.
func TestTempPattern(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "the news cache keeps the historical pattern",
			path: "/home/user/.cache/newsfetch/feed.json",
			want: "feed-*.json.tmp",
		},
		{
			name: "the following cache is named after itself",
			path: "/home/user/.cache/newsfetch/following.json",
			want: "following-*.json.tmp",
		},
		{
			name: "a bare basename resolves the same way",
			path: "feed.json",
			want: "feed-*.json.tmp",
		},
		{
			name: "a target with no .json suffix keeps its whole basename",
			path: "/tmp/scratch/oddly-named",
			want: "oddly-named-*.json.tmp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tempPattern(tc.path); got != tc.want {
				t.Errorf("tempPattern(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
