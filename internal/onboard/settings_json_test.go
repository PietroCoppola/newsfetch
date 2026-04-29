package onboard

import (
	"reflect"
	"strings"
	"testing"
)

// curr is the baseline current Answers used for tests where the caller
// would have loaded the existing config. Holds non-default ticker values
// so tests can assert preservation through omission.
var curr = Answers{
	Topics:       nil,
	Style:        "boxed",
	Sources:      []string{"hackernews"},
	Count:        1,
	TickerMarker: "branch",
	TickerBoxed:  true,
}

func TestReadSettingsJSON_Valid(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":["rust"],"style":"boxed","sources":["hackernews","lobsters"],"count":3,"ticker_marker":"arrow","ticker_boxed":false}`,
	), curr)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	want := Answers{
		Topics:       []string{"rust"},
		Style:        "boxed",
		Sources:      []string{"hackernews", "lobsters"},
		Count:        3,
		TickerMarker: "arrow",
		TickerBoxed:  false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadSettingsJSON_OmittedTickerFieldsPreserveCurrent(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"minimal","sources":["hackernews"],"count":1}`,
	), curr)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	if got.TickerMarker != curr.TickerMarker {
		t.Errorf("TickerMarker = %q, want %q (preserved from current)", got.TickerMarker, curr.TickerMarker)
	}
	if got.TickerBoxed != curr.TickerBoxed {
		t.Errorf("TickerBoxed = %v, want %v (preserved from current)", got.TickerBoxed, curr.TickerBoxed)
	}
}

func TestReadSettingsJSON_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing topics", `{"style":"boxed","sources":["hackernews"],"count":1}`},
		{"missing style", `{"topics":[],"sources":["hackernews"],"count":1}`},
		{"missing sources", `{"topics":[],"style":"boxed","count":1}`},
		{"missing count", `{"topics":[],"style":"boxed","sources":["hackernews"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), curr)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestReadSettingsJSON_SourcesEmptyRejected(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":[],"count":1}`), curr)
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("error should explain non-empty requirement; got %v", err)
	}
}

func TestReadSettingsJSON_SourcesUnknownRejected(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":["weirdsrc"],"count":1}`), curr)
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
	if !strings.Contains(err.Error(), "weirdsrc") {
		t.Errorf("error should name the offending source; got %v", err)
	}
}

func TestReadSettingsJSON_InvalidStyle(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(`{"topics":[],"style":"fancy","sources":["hackernews"],"count":1}`), curr)
	if err == nil {
		t.Fatal("expected error for invalid style")
	}
}

func TestReadSettingsJSON_CountOutOfRange(t *testing.T) {
	cases := []string{
		`{"topics":[],"style":"boxed","sources":["hackernews"],"count":0}`,
		`{"topics":[],"style":"boxed","sources":["hackernews"],"count":99}`,
	}
	for _, body := range cases {
		_, err := ReadSettingsJSON(strings.NewReader(body), curr)
		if err == nil {
			t.Errorf("expected error for body %q", body)
		}
	}
}

func TestReadSettingsJSON_UnknownTickerMarker(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","sources":["hackernews"],"count":2,"ticker_marker":"spiral"}`,
	), curr)
	if err == nil {
		t.Fatal("expected error for unknown ticker_marker")
	}
	if !strings.Contains(err.Error(), "spiral") {
		t.Errorf("error should name the offending marker; got %v", err)
	}
}

func TestReadSettingsJSON_UnknownField(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","sources":["hackernews"],"count":1,"cache_ttl_minutes":10}`,
	), curr)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}
