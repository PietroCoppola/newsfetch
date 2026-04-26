package onboard

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadInitJSON_Valid(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(`{"topics":["rust","ai"],"style":"boxed"}`))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	want := Answers{Topics: []string{"rust", "ai"}, Style: "boxed"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.Sources != nil {
		t.Errorf("Sources should be nil when omitted; got %v", got.Sources)
	}
}

func TestReadInitJSON_EmptyTopicsAllowed(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"minimal"}`))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	if len(got.Topics) != 0 {
		t.Errorf("Topics = %v, want empty", got.Topics)
	}
	if got.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", got.Style)
	}
}

func TestReadInitJSON_SourcesOptional_PowerUser(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":["hackernews","lobsters"]}`))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	if !reflect.DeepEqual(got.Sources, []string{"hackernews", "lobsters"}) {
		t.Errorf("Sources = %v, want [hackernews lobsters]", got.Sources)
	}
}

func TestReadInitJSON_SourcesEmptyRejected(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":[]}`))
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("error should explain non-empty requirement; got %v", err)
	}
}

func TestReadInitJSON_SourcesUnknownRejected(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":["weirdsrc"]}`))
	if err == nil {
		t.Fatal("expected error for unknown source name")
	}
	if !strings.Contains(err.Error(), "weirdsrc") {
		t.Errorf("error should name the offending source; got %v", err)
	}
}

func TestReadInitJSON_MissingTopics(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"style":"boxed"}`))
	if err == nil {
		t.Fatal("expected error for missing topics")
	}
	if !strings.Contains(err.Error(), "topics") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestReadInitJSON_MissingStyle(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[]}`))
	if err == nil {
		t.Fatal("expected error for missing style")
	}
	if !strings.Contains(err.Error(), "style") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestReadInitJSON_InvalidStyle(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"fancy"}`))
	if err == nil {
		t.Fatal("expected error for invalid style")
	}
	if !strings.Contains(err.Error(), "fancy") {
		t.Errorf("error should name the offending value; got %v", err)
	}
}

func TestReadInitJSON_UnknownField(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","cache_ttl_minutes":10}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestReadInitJSON_Malformed(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{ not valid json`))
	if err == nil {
		t.Fatal("expected decode error")
	}
}
