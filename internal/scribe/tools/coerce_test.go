package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type coerceArgsFixture struct {
	Action string   `json:"action"`
	ID     string   `json:"id"`
	Tags   []string `json:"tags"`
	Limit  int      `json:"limit"`
	Pinned *bool    `json:"pinned"`
}

// The memory tool's first call sent "tags" as a string and "limit" as a
// quoted number. Decode reads both as the field's type, instead of failing
// with a raw Go decode error.
func TestDecodeAcceptsNearMissShapes(t *testing.T) {
	var a coerceArgsFixture
	raw := json.RawMessage(`{"action":"save","id":42,"tags":"auth\nretries, login","limit":"10","pinned":"yes"}`)
	if err := Decode(raw, &a); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if a.Action != "save" || a.ID != "42" || a.Limit != 10 || a.Pinned == nil || !*a.Pinned {
		t.Fatalf("fields = %+v", a)
	}
	if want := []string{"auth", "retries, login"}; !reflect.DeepEqual(a.Tags, want) {
		t.Fatalf("tags = %q, want %q", a.Tags, want)
	}
}

// A single value for a list field becomes a one-item list.
func TestDecodeWrapsScalarForList(t *testing.T) {
	var a coerceArgsFixture
	if err := Decode(json.RawMessage(`{"action":"save","tags":"only"}`), &a); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(a.Tags, []string{"only"}) {
		t.Fatalf("tags = %q", a.Tags)
	}
}

// Shapes that cannot be read still fail, and the message still says to call
// the tool again.
func TestDecodeStillRejectsUnreadableArgs(t *testing.T) {
	var a coerceArgsFixture
	err := Decode(json.RawMessage(`{"action":"save","limit":"ten"}`), &a)
	if err == nil {
		t.Fatal("limit \"ten\" should not decode")
	}
	if !strings.Contains(err.Error(), "call it again") {
		t.Fatalf("error lost the retry wording: %v", err)
	}
	if err := Decode(json.RawMessage(`{"tags":{"x":1}}`), &a); err == nil {
		t.Fatal("an object for a list should not decode")
	}
}

// Well-formed arguments are not touched.
func TestDecodeStrictPathUnchanged(t *testing.T) {
	var a coerceArgsFixture
	if err := Decode(json.RawMessage(`{"action":"x","tags":["a"],"limit":3}`), &a); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if a.Limit != 3 || !reflect.DeepEqual(a.Tags, []string{"a"}) {
		t.Fatalf("fields = %+v", a)
	}
}

func TestDecodeReadsPathFromANearMissKey(t *testing.T) {
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
	}
	if err := Decode(json.RawMessage(`{"file_path":"util.go","old_string":"a"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.Path != "util.go" {
		t.Fatalf("path = %q, want util.go from file_path", in.Path)
	}
	var plain struct {
		Path string `json:"path"`
	}
	if err := Decode(json.RawMessage(`{"path":"a.go","file":"b.go"}`), &plain); err != nil || plain.Path != "a.go" {
		t.Fatalf("an explicit path must win: %q, %v", plain.Path, err)
	}
}
