package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestApplyContextManagementOffByDefault(t *testing.T) {
	var r llm.Request
	applyContextManagement(&r, config.ContextManagement{}, "anthropic", 200000)
	if r.ClearThinking || r.ClearToolUses {
		t.Fatalf("unset config turned an edit on: %+v", r)
	}
	if r.ContextWindow != 200000 {
		t.Fatalf("ContextWindow = %d, want the model window passed through", r.ContextWindow)
	}
}

func TestApplyContextManagementSetsAnthropicEdits(t *testing.T) {
	cm := config.ContextManagement{ClearThinking: boolp(true), ClearToolUses: boolp(true)}
	var r llm.Request
	applyContextManagement(&r, cm, "anthropic", 1000000)
	if !r.ClearThinking || !r.ClearToolUses || r.ContextWindow != 1000000 {
		t.Fatalf("request = %+v, want both edits and the window", r)
	}
}

func TestApplyContextManagementSkipsOtherProviders(t *testing.T) {
	cm := config.ContextManagement{ClearThinking: boolp(true), ClearToolUses: boolp(true)}
	var r llm.Request
	applyContextManagement(&r, cm, "openai", 200000)
	if r.ClearThinking || r.ClearToolUses {
		t.Fatalf("non-Anthropic request got context edits: %+v", r)
	}
}

func boolp(b bool) *bool { return &b }
