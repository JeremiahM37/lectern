package api

// refuseIfChanged is the server side of the review's stale check. This test
// runs real git through the local executor without the isolated real-process
// harness, so it always runs in `go test`; the full commit and push routes are
// covered by witness_review_real_test.go under the isolated runner.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/witness"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	return dir
}

func TestRefuseIfChangedUsesThePinsAndRefusesDrift(t *testing.T) {
	dir := gitRepo(t)
	ex := executor.NewLocal()
	reviewed, err := witness.Snapshot(t.Context(), ex, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	// No pin sent: the old behaviour, nothing is checked.
	rec := httptest.NewRecorder()
	if !refuseIfChanged(rec, r, ex, dir, "", "") {
		t.Fatalf("no pin must pass, got %d %s", rec.Code, rec.Body)
	}

	// Matching pins pass.
	rec = httptest.NewRecorder()
	if !refuseIfChanged(rec, r, ex, dir, reviewed.Head, reviewed.DiffHash) {
		t.Fatalf("matching pins must pass, got %d %s", rec.Code, rec.Body)
	}

	// An edit after the review is refused with the stale shape the web app reads.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	if refuseIfChanged(rec, r, ex, dir, reviewed.Head, reviewed.DiffHash) {
		t.Fatal("a changed worktree must be refused")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "stale" || !strings.Contains(body["detail"].(string), "working tree changed") {
		t.Fatalf("body: %v", body)
	}
}
