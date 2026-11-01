package model

import (
	"encoding/json"
	"testing"
)

// decodedAttributes returns attributes exactly as the storage layer produces
// them: JSON numbers arrive as float64.
func decodedAttributes(t *testing.T, raw string) Attributes {
	t.Helper()
	var attributes Attributes
	if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
		t.Fatalf("unmarshal attributes: %v", err)
	}
	return attributes
}

func TestAttributesSetSkipsNilAndEmpty(t *testing.T) {
	attributes := Attributes{}
	attributes.Set("nothing", nil)
	attributes.Set("empty", "")
	attributes.Set("text", "keep")
	attributes.Set("zero", 0)
	attributes.Set("off", false)

	if attributes.Has("nothing") || attributes.Has("empty") {
		t.Fatalf("nil and empty values must not be stored: %#v", attributes)
	}
	if got := attributes.GetString("text", ""); got != "keep" {
		t.Errorf("text = %q, want keep", got)
	}
	if got := attributes.GetInteger("zero", -1); got != 0 {
		t.Errorf("zero = %d, want 0", got)
	}
	if !attributes.Has("off") {
		t.Errorf("false is a real value and must be stored: %#v", attributes)
	}
}

func TestAttributesGetString(t *testing.T) {
	attributes := decodedAttributes(t, `{"name":"Bob","count":7,"ratio":1.5,"flag":true}`)

	if got := attributes.GetString("name", "default"); got != "Bob" {
		t.Errorf("name = %q, want Bob", got)
	}
	if got := attributes.GetString("missing", "default"); got != "default" {
		t.Errorf("missing = %q, want default", got)
	}
	if got := attributes.GetString("count", ""); got != "7" {
		t.Errorf("count = %q, want 7", got)
	}
	if got := attributes.GetString("flag", ""); got != "true" {
		t.Errorf("flag = %q, want true", got)
	}
}

func TestAttributesGetDouble(t *testing.T) {
	attributes := decodedAttributes(t, `{"ratio":1.5,"count":7,"text":"2.25","bad":"x","flag":true}`)

	cases := []struct {
		key      string
		expected float64
	}{
		{"ratio", 1.5},
		{"count", 7},
		{"text", 2.25},
		{"bad", 0},
		{"flag", 0},
		{"missing", 0},
	}
	for _, testCase := range cases {
		if got := attributes.GetDouble(testCase.key, 0); got != testCase.expected {
			t.Errorf("GetDouble(%q) = %v, want %v", testCase.key, got, testCase.expected)
		}
	}
	if got := attributes.GetDouble("missing", 4.5); got != 4.5 {
		t.Errorf("default = %v, want 4.5", got)
	}
	if got := (Attributes{"n": int64(3)}).GetDouble("n", 0); got != 3 {
		t.Errorf("int64 attribute = %v, want 3", got)
	}
	if got := (Attributes{"n": uint16(9)}).GetDouble("n", 0); got != 9 {
		t.Errorf("uint16 attribute = %v, want 9", got)
	}
}

func TestAttributesGetInteger(t *testing.T) {
	attributes := decodedAttributes(t, `{"count":7.9,"text":"12","bad":"x","flag":true}`)

	cases := []struct {
		key      string
		expected int
	}{
		{"count", 7},
		{"text", 12},
		{"bad", -1},
		{"flag", -1},
		{"missing", -1},
	}
	for _, testCase := range cases {
		if got := attributes.GetInteger(testCase.key, -1); got != testCase.expected {
			t.Errorf("GetInteger(%q) = %d, want %d", testCase.key, got, testCase.expected)
		}
	}
}

func TestAttributesGetLong(t *testing.T) {
	attributes := decodedAttributes(t, `{"id":9007199254740993,"text":"42","bad":"x"}`)

	if got := attributes.GetLong("text", 0); got != 42 {
		t.Errorf("text = %d, want 42", got)
	}
	if got := attributes.GetLong("bad", -1); got != -1 {
		t.Errorf("bad = %d, want -1", got)
	}
	if got := attributes.GetLong("missing", -1); got != -1 {
		t.Errorf("missing = %d, want -1", got)
	}
	if got := (Attributes{"id": int64(9007199254740993)}).GetLong("id", 0); got != 9007199254740993 {
		t.Errorf("int64 id = %d, want 9007199254740993", got)
	}
}

func TestAttributesGetBoolean(t *testing.T) {
	attributes := decodedAttributes(t, `{"on":true,"off":false,"text":"TRUE","noise":"1","number":1}`)

	cases := []struct {
		key      string
		expected bool
	}{
		{"on", true},
		{"off", false},
		{"text", true},
		{"noise", false},
		{"number", false},
		{"missing", false},
	}
	for _, testCase := range cases {
		if got := attributes.GetBoolean(testCase.key); got != testCase.expected {
			t.Errorf("GetBoolean(%q) = %v, want %v", testCase.key, got, testCase.expected)
		}
	}
}

func TestAttributesRemove(t *testing.T) {
	attributes := Attributes{"floor": 100.0}

	if value := attributes.Remove("floor"); value != 100.0 {
		t.Errorf("removed = %v, want 100", value)
	}
	if attributes.Has("floor") {
		t.Errorf("floor is still present: %#v", attributes)
	}
	if value := attributes.Remove("floor"); value != nil {
		t.Errorf("removing a missing key = %v, want nil", value)
	}
}

func TestAttributesNumbersSurviveJSONRoundTrip(t *testing.T) {
	attributes := Attributes{"floor": 100.0, "mode": "strict", "on": true}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Attributes
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.GetDouble("floor", 0) != 100 || decoded.GetString("mode", "") != "strict" || !decoded.GetBoolean("on") {
		t.Errorf("round trip lost a value: %#v", decoded)
	}
}
