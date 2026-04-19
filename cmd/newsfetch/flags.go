package main

import "strings"

// topicsFlag is a custom flag.Value for --topics that distinguishes "not
// passed at all" from "passed empty" (explicit empty override of config).
// This supports the design spec's explicitEmptyOverride semantic: users
// can defeat a configured topic list for a single invocation via
// "newsfetch --topics=".
type topicsFlag struct {
	set  bool
	vals []string
}

func (t *topicsFlag) String() string {
	if t == nil {
		return ""
	}
	return strings.Join(t.vals, ",")
}

// Set records the flag as provided (regardless of value). Empty or
// whitespace-only input means "no topics"; the caller will merge this
// over any configured topics.
func (t *topicsFlag) Set(s string) error {
	t.set = true
	if strings.TrimSpace(s) == "" {
		t.vals = nil
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	t.vals = out
	return nil
}
