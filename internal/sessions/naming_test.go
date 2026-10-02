package sessions

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestDefaultNamesTellSessionsApart(t *testing.T) {
	if got := WithTopic(NameBase("myapp", "claude"), "Fix the login page.\nIt 500s on submit"); got != "myapp — Fix the login page" {
		t.Fatalf("project + topic: %q", got)
	}
	if got := WithTopic(NameBase("", "codex"), ""); got != "codex" {
		t.Fatalf("no project, no prompt: %q", got)
	}
	if got := Topic("please make the whole settings page much simpler for new users"); got != "please make the whole settings page" {
		t.Fatalf("topic is a few words: %q", got)
	}
	ended := 1.0
	live := []*store.Session{{ID: 1, Name: "myapp"}, {ID: 2, Name: "myapp #2"}, {ID: 3, Name: "other", EndedAt: &ended}}
	if got := UniqueName("myapp", live, 0); got != "myapp #3" {
		t.Fatalf("duplicates get a number: %q", got)
	}
	if got := UniqueName("other", live, 0); got != "other" {
		t.Fatalf("an ended session's name is free: %q", got)
	}
	if got := UniqueName("myapp", live, 1); got != "myapp" {
		t.Fatalf("self: %q", got)
	}
	if !IsDefaultName("myapp #2", "myapp") || IsDefaultName("my own name", "myapp") || IsDefaultName("myapp — fix it", "myapp") {
		t.Fatal("default-name detection")
	}
}
