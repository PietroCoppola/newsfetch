package onboard

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadJSONAnswers_Valid(t *testing.T) {
	got, err := ReadJSONAnswers(strings.NewReader(`{"topics":["rust","ai"],"style":"boxed"}`))
	if err != nil {
		t.Fatalf("ReadJSONAnswers: %v", err)
	}
	want := Answers{Topics: []string{"rust", "ai"}, Style: "boxed"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadJSONAnswers_EmptyTopicsAllowed(t *testing.T) {
	got, err := ReadJSONAnswers(strings.NewReader(`{"topics":[],"style":"minimal"}`))
	if err != nil {
		t.Fatalf("ReadJSONAnswers: %v", err)
	}
	if len(got.Topics) != 0 {
		t.Errorf("Topics = %v, want empty", got.Topics)
	}
	if got.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", got.Style)
	}
}

func TestReadJSONAnswers_MissingTopics(t *testing.T) {
	_, err := ReadJSONAnswers(strings.NewReader(`{"style":"boxed"}`))
	if err == nil {
		t.Fatal("expected error for missing topics")
	}
	if !strings.Contains(err.Error(), "topics") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestReadJSONAnswers_MissingStyle(t *testing.T) {
	_, err := ReadJSONAnswers(strings.NewReader(`{"topics":[]}`))
	if err == nil {
		t.Fatal("expected error for missing style")
	}
	if !strings.Contains(err.Error(), "style") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestReadJSONAnswers_InvalidStyle(t *testing.T) {
	_, err := ReadJSONAnswers(strings.NewReader(`{"topics":[],"style":"fancy"}`))
	if err == nil {
		t.Fatal("expected error for invalid style")
	}
	if !strings.Contains(err.Error(), "fancy") {
		t.Errorf("error should name the offending value; got %v", err)
	}
}

func TestReadJSONAnswers_UnknownField(t *testing.T) {
	_, err := ReadJSONAnswers(strings.NewReader(`{"topics":[],"style":"boxed","sources":["hackernews"]}`))
	if err == nil {
		t.Fatal("expected error for unknown field (sources is not a wizard answer)")
	}
}

func TestReadJSONAnswers_Malformed(t *testing.T) {
	_, err := ReadJSONAnswers(strings.NewReader(`{ not valid json`))
	if err == nil {
		t.Fatal("expected decode error")
	}
}
