package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

// The delivered items are what the operator actually reads, and they come from
// a store whose shape lectern does not control — so every one of these is a
// tolerance the delivery log depends on (docs/memory-visibility.md).

func TestMemoryDeliveryItemsKeepTheStoresOwnFields(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"a1","path":"memory/kestrel.md","title":"Kestrel","snippet":"deploys","trust":"trusted","score":0.4},
		{"key":"a2","source":"Agent Memory/project_x.md","text":"  second fact  "},
		{"id":"","path":"","title":"","snippet":""}
	]`)
	items := normalizeItems(raw, nil)
	if len(items) != 2 {
		t.Fatalf("empty items must be dropped, got %+v", items)
	}
	if items[0].ID != "a1" || items[0].Source != "memory/kestrel.md" ||
		items[0].Title != "Kestrel" || items[0].Snippet != "deploys" {
		t.Errorf("first item mangled: %+v", items[0])
	}
	if items[1].ID != "a2" || items[1].Source != "Agent Memory/project_x.md" ||
		items[1].Snippet != "second fact" {
		t.Errorf("alternate keys not accepted: %+v", items[1])
	}
}

func TestMemoryDeliveryItemsFallBackToKeysAndBoundTheSnippet(t *testing.T) {
	items := normalizeItems(nil, []string{"memory/kestrel.md#3", "memory/notes.md#1"})
	if len(items) != 2 || items[0].ID != "memory/kestrel.md#3" {
		t.Fatalf("keys are all a store may send: %+v", items)
	}
	if items[0].Source != "memory/kestrel.md" {
		t.Errorf("the path half of a fingerprint is worth keeping: %+v", items[0])
	}
	// A snippet is an excerpt, not the record: the log answers "was this the
	// right memory", and the source is a click away.
	long := strings.Repeat("é", 400)
	items = normalizeItems(json.RawMessage(`[{"id":"x","snippet":`+quote(long)+`}]`), nil)
	if len(items) != 1 || len([]rune(items[0].Snippet)) > snippetLimit+1 {
		t.Fatalf("snippet not bounded: %d runes", len([]rune(items[0].Snippet)))
	}
	if !json.Valid([]byte(`"` + items[0].Snippet + `"`)) {
		t.Errorf("snippet clipped mid-character: %q", items[0].Snippet)
	}
}

func TestMemoryDeliveryItemsAcceptAnIDKeyedMap(t *testing.T) {
	items := normalizeItems(json.RawMessage(`{"m2":{"path":"b.md"},"m1":{"path":"a.md"}}`), nil)
	if len(items) != 2 || items[0].ID != "m1" || items[1].ID != "m2" {
		t.Fatalf("a map of items must read as a list: %+v", items)
	}
	if items[0].Source != "a.md" {
		t.Errorf("map key lost: %+v", items[0])
	}
}

func TestMemoryContextResultRecordsSizeAndItemsJSON(t *testing.T) {
	result := ContextResult{Context: "twelve bytes", Items: []Item{{ID: "a", Source: "s", Title: "t", Snippet: "n"}}}
	if result.Size() != len("twelve bytes") {
		t.Errorf("size must be the injected bytes: %d", result.Size())
	}
	var back []Item
	if err := json.Unmarshal([]byte(result.ItemsJSON()), &back); err != nil || len(back) != 1 || back[0].ID != "a" {
		t.Fatalf("items_json round trip: %s %v", result.ItemsJSON(), err)
	}
	// An item-less delivery is still valid JSON, and a corrupt column is not
	// allowed to take the Memory section down with it.
	if (ContextResult{}).ItemsJSON() != "[]" {
		t.Errorf("empty items must marshal to []")
	}
	if got := ParseItems("{not json"); len(got) != 0 {
		t.Errorf("malformed items_json must degrade to empty: %v", got)
	}
	if got := ParseItems(`[{"id":"a"}]`); len(got) != 1 {
		t.Errorf("items did not read back: %v", got)
	}
}

// quote renders a Go string as the JSON literal an item would carry it in.
func quote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
