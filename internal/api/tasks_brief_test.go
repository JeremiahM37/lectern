package api_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The web app's lists ask for ?brief=true: a long prompt arrives clipped and
// the result's small values (what the board card shows) survive, while the
// task itself still carries everything.
func TestTaskListBriefClipsPrompt(t *testing.T) {
	h := newHarness(t)
	prompt := "do it " + strings.Repeat("é", 5000)
	task := h.run(h.seededProjectID(), "big", prompt, nil)
	var brief obj
	for _, x := range h.getList("/api/tasks?brief=true") {
		if x.id() == task.id() {
			brief = x
		}
	}
	if brief == nil {
		t.Fatal("the task is missing from the brief list")
	}
	got, _ := brief["prompt"].(string)
	if n := utf8.RuneCountInString(got); n != 400 || !strings.HasPrefix(got, "do it ") {
		t.Fatalf("brief prompt is %d runes, want 400 with the same start", n)
	}
	full := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	if full["prompt"] != prompt {
		t.Fatal("the task's own view lost its full prompt")
	}
	fullResult, _ := full["attempt"].(map[string]any)["result"].(map[string]any)
	briefResult, _ := brief["attempt"].(map[string]any)["result"].(map[string]any)
	if len(fullResult) == 0 {
		t.Fatal("the mock attempt recorded no result to compare")
	}
	for key, value := range fullResult {
		if _, isText := value.(string); isText && len(value.(string)) > 512 {
			if _, kept := briefResult[key]; kept {
				t.Errorf("brief kept the large %q", key)
			}
			continue
		}
		if fmt.Sprint(briefResult[key]) != fmt.Sprint(value) {
			t.Errorf("brief changed small %q: %v, want %v", key, briefResult[key], value)
		}
	}
	if len(h.getList("/api/tasks")) != len(h.getList("/api/tasks?brief=true")) {
		t.Fatal("brief and full lists disagree on which tasks exist")
	}
	plain := h.getList("/api/tasks")
	for _, x := range plain {
		if x.id() == task.id() && x["prompt"] != prompt {
			t.Fatal("the plain list should still carry the full prompt")
		}
	}
}
