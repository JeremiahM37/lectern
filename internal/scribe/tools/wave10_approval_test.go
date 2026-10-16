package tools

import (
	"context"
	"strings"
	"testing"
)

// A note from an approval is kept on the call's slot, and an empty reason or
// a context without a slot is a no-op.
func TestNoteApprovalKeepsTheNoteOnTheCall(t *testing.T) {
	ctx, notes := WithCallNotes(context.Background())
	NoteApproval(ctx, "  use the scratch dir  ")
	NoteApproval(ctx, "")
	got := notes()
	if got != "The user approved this with a note: use the scratch dir" {
		t.Fatalf("notes = %q", got)
	}
	NoteApproval(context.Background(), "dropped") // no slot: nothing happens
}

func TestNoteApprovalCapsLongText(t *testing.T) {
	ctx, notes := WithCallNotes(context.Background())
	NoteApproval(ctx, strings.Repeat("x", 900))
	if n := len(notes()); n > 560 {
		t.Fatalf("note is %d bytes, want it capped", n)
	}
}
