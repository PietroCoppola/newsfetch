package onboard

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadSettingsJSON_Valid(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(`{"topics":["rust"],"style":"boxed","sources":["hackernews","lobsters"]}`))
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	want := Answers{Topics: []string{"rust"}, Style: "boxed", Sources: []string{"hackernews", "lobsters"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadSettingsJSON_AllFieldsRequired(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing topics", `{"style":"boxed","sources":["hackernews"]}`},
		{"missing style", `{"topics":[],"sources":["hackernews"]}`},
		{"missing sources", `{"topics":[],"style":"boxed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestReadSettingsJSON_SourcesEmptyRejected(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":[]}`))
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("error should explain non-empty requirement; got %v", err)
	}
}

func TestReadSettingsJSON_SourcesUnknownRejected(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":["weirdsrc"]}`))
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
	if !strings.Contains(err.Error(), "weirdsrc") {
		t.Errorf("error should name the offending source; got %v", err)
	}
}

func TestReadSettingsJSON_InvalidStyle(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"fancy","sources":["hackernews"]}`))
	if err == nil {
		t.Fatal("expected error for invalid style")
	}
}

func TestReadSettingsJSON_UnknownField(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":["hackernews"],"cache_ttl_minutes":10}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}
