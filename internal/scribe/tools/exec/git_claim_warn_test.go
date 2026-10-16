package exec

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

type fakeClaims map[string]string

func (f fakeClaims) OtherClaims() map[string]string { return f }

func TestGitWarnsAboutOtherTabsClaims(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	env := newTestEnv(dir, &fakeHost{})
	env.Claims = fakeClaims{filepath.Join(dir, "main.go"): "tab two"}
	tool := &gitTool{}

	raw, _ := json.Marshal(map[string]any{"action": "commit", "message": "x", "files": []string{"main.go"}})
	warn := claimWarning(env, raw)
	if !strings.Contains(warn, "main.go (claimed by tab two)") {
		t.Fatalf("expected a warning naming main.go, got %q", warn)
	}
	// The call still runs: the warning is advisory, not a refusal.
	res := tool.Run(context.Background(), env, raw)
	if !strings.HasPrefix(res.Output, "WARNING:") {
		t.Fatalf("warning should lead the result, got %q", res.Output)
	}

	// A file no other tab holds produces no warning.
	env.Claims = fakeClaims{filepath.Join(dir, "elsewhere.go"): "tab two"}
	if w := claimWarning(env, raw); w != "" {
		t.Fatalf("no overlap should mean no warning, got %q", w)
	}
	// Read-only calls never warn.
	env.Claims = fakeClaims{filepath.Join(dir, "main.go"): "tab two"}
	ro, _ := json.Marshal(map[string]any{"action": "status"})
	if res := tool.Run(context.Background(), env, ro); strings.Contains(res.Output, "WARNING") {
		t.Fatalf("status should not warn: %q", res.Output)
	}
}

func TestClaimWarningNeedsNoRegistry(t *testing.T) {
	env := &tools.Env{Workdir: t.TempDir()}
	if w := claimWarning(env, json.RawMessage(`{"action":"commit"}`)); w != "" {
		t.Fatalf("nil claim source should not warn, got %q", w)
	}
}
