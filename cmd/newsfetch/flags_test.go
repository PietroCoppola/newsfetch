package main

import (
	"reflect"
	"testing"
)

func TestTopicsFlag_Set(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantVals []string
	}{
		{"comma list", "rust,ai", []string{"rust", "ai"}},
		{"empty string explicit", "", nil},
		{"whitespace only", "  ", nil},
		{"double comma", "rust,,ai", []string{"rust", "ai"}},
		{"whitespace around tokens", "rust, ai , ", []string{"rust", "ai"}},
		{"single value", "security", []string{"security"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tf topicsFlag
			if err := tf.Set(tc.in); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if !tf.set {
				t.Error("Set should mark the flag as set, even on empty input")
			}
			if !reflect.DeepEqual(tf.vals, tc.wantVals) {
				t.Errorf("vals = %v, want %v", tf.vals, tc.wantVals)
			}
		})
	}
}

func TestTopicsFlag_Unset(t *testing.T) {
	var tf topicsFlag
	if tf.set {
		t.Error("zero-value topicsFlag should not be marked as set")
	}
}
