package api_test

import (
	"os"
	"path/filepath"
	"testing"
)

// The real file, changes, review and worktree flows once more with the
// local target running `lectern helper …` instead of the Python scripts.
// python3 is shadowed by a stub that fails, so a fallback cannot pass.
func TestRealTargetFlowsThroughTheGoHelpers(t *testing.T) {
	t.Setenv(goHelpersEnv, "1")
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "python3"), []byte("#!/bin/sh\necho 'python3 is disabled in this test' >&2\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{"WorkspaceEditSavesWithConflictDetection", TestWorkspaceEditSavesWithConflictDetection},
		{"WorkspaceWritesStayInsideTheWorkspace", TestWorkspaceWritesStayInsideTheWorkspace},
		{"WorkspaceFileOperations", TestWorkspaceFileOperations},
		{"WorkspaceWriteSizeCap", TestWorkspaceWriteSizeCap},
		{"WorkspaceIndexStatusAndSearch", TestWorkspaceIndexStatusAndSearch},
		{"WorkspaceIndexAndSearchWithoutGit", TestWorkspaceIndexAndSearchWithoutGit},
		{"WorkspaceWatchReportsChanges", TestWorkspaceWatchReportsChanges},
		{"LiveReviewReadsGitIndexAndWorktreeWithoutChangingEither", TestLiveReviewReadsGitIndexAndWorktreeWithoutChangingEither},
		{"ReviewUnbornRepositoryAndWorkspaceBoundary", TestReviewUnbornRepositoryAndWorkspaceBoundary},
		{"RealGitStageAndUnstageSingleHunks", TestRealGitStageAndUnstageSingleHunks},
		{"RealGitDiscardFileHunkAndRefusesEscapes", TestRealGitDiscardFileHunkAndRefusesEscapes},
		{"RealGitConflictResolution", TestRealGitConflictResolution},
		{"RealGitAbortMerge", TestRealGitAbortMerge},
		{"RealGitImageBlob", TestRealGitImageBlob},
		{"RealAttributionFromHooksAndHistory", TestRealAttributionFromHooksAndHistory},
		{"RealGitStagesChosenLinesAndNewFileHunks", TestRealGitStagesChosenLinesAndNewFileHunks},
		{"RealAttributionPrunesAgentMarks", TestRealAttributionPrunesAgentMarks},
	} {
		t.Run(c.name, c.fn)
	}
}
