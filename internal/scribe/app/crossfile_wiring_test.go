package app

import (
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// Once the code map is built the post-edit pass has it, so the cross-file
// check and clone hints run, and the agent calls its pre-edit hook.
func TestPostEditGetsTheCodeMapAndBeforeTool(t *testing.T) {
	be, _ := testBackend(t)
	a := be.(*scribeBackend).current()
	if a.ext.postEdit == nil || a.ext.cmap == nil {
		t.Skip("post-edit or code map is off in this configuration")
	}
	select {
	case <-a.ext.mapOK:
	case <-time.After(30 * time.Second):
		t.Fatal("code map never finished building")
	}
	a.ext.postEdit.mu.Lock()
	deps := a.ext.postEdit.deps
	a.ext.postEdit.mu.Unlock()
	if deps == nil {
		t.Fatal("post-edit pass has no code map after the build")
	}
	if a.agentOptions(agent.Model{}, "default").BeforeTool == nil {
		t.Fatal("agent options carry no BeforeTool hook")
	}
}
