package agent

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestLoopHintIgnoresRereadsAfterAnEdit(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	a := New(baseOptions(newFake(t), reg, &eventLog{}, t.TempDir()))
	read := `{"path":"util.go"}`
	// read, edit (a tool that changes state), read, read: two reads since
	// the edit, which is checking the work, not a loop.
	a.msgs = append(a.msgs, callMsg("1", "read", read), callMsg("2", "edit_file", `{"path":"util.go"}`),
		callMsg("3", "read", read), callMsg("4", "read", read))
	if h := a.loopHint(); h != "" {
		t.Fatalf("re-reads after an edit were called a loop: %q", h)
	}
	// A third identical read with nothing changed in between is a loop.
	a.msgs = append(a.msgs, callMsg("5", "read", read))
	if h := a.loopHint(); !strings.Contains(h, "three times") {
		t.Fatalf("three identical reads with no change should be a loop, got %q", h)
	}
}
