package agent

import (
	"strings"
	"testing"
)

func TestPromptCarriesCommitGuidanceAndGitContext(t *testing.T) {
	in := PromptInput{
		Workdir:    "/p",
		ModelRef:   "anthropic/claude-haiku-5-5",
		Commands:   "test: go test ./...",
		GitContext: "Branch: main\nWorking tree: clean",
	}
	s := BuildSystem(in, "default")
	for _, want := range []string{
		"- Project commands: test: go test ./...",
		"# Git context",
		"Branch: main",
		"# Commit messages",
		"Do not add Co-Authored-By",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}

	in.Subagent = "explore"
	if s := BuildSystem(in, "default"); strings.Contains(s, "# Commit messages") {
		t.Error("explore subagents do not commit, so they get no commit guidance")
	}
}

func TestPromptChangesWhenGitContextChanges(t *testing.T) {
	a := PromptInput{Workdir: "/p", GitContext: "Working tree: clean"}
	b := a
	b.GitContext = "Working tree: 1 staged, 0 unstaged, 0 untracked"
	if BuildSystem(a, "default") == BuildSystem(b, "default") {
		t.Fatal("the memoised prompt must not ignore a changed git context")
	}
}
