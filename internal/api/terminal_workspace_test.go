package api_test

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestWorkspaceFilesReadActualTargetAndStayWithinRoot(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	mustRun(t, root, "tmux", "new-session", "-d", "-s", "file-session", "bash --norc")
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "files", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "files", Workdir: root, TmuxSession: "file-session", Status: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/term/session/%d", session.ID)
	filename := "Résumé's $(touch owned).txt"
	payload := []byte("<script>not code</script>\x00\xff")
	if err := os.WriteFile(filepath.Join(root, filename), payload, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape.txt"))
	mustRun(t, root, "mkfifo", filepath.Join(root, "pipe"))
	code, data := h.request("GET", base+"/file?path="+url.QueryEscape(filename), nil, nil)
	if code != 200 || !bytes.Equal(data, payload) {
		t.Fatalf("file bytes: %d %q", code, data)
	}
	var listing obj
	h.decode("GET", base+"/files", nil, 200, &listing)
	encoded := fmt.Sprint(listing)
	if strings.Contains(encoded, "escape.txt") || strings.Contains(encoded, "pipe") {
		t.Fatalf("unsafe entries: %s", encoded)
	}
	for _, name := range []string{"escape.txt", "pipe", outside, "../private.txt"} {
		code, _ := h.request("GET", base+"/file?path="+url.QueryEscape(name), nil, nil)
		if code != 400 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	huge, err := os.Create(filepath.Join(root, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	huge.Truncate((25 << 20) + 1)
	huge.Close()
	code, _ = h.request("GET", base+"/file?path=huge", nil, nil)
	if code != 400 {
		t.Fatalf("oversize: %d", code)
	}
	// Attempt files and uploads belong to that attempt's worktree, not its repo.
	repo := t.TempDir()
	project, err := h.App.DB.InsertProject(&store.Project{Name: "file-task", TargetID: target.ID, RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	task := h.task(project.ID, "worktree files", "x", nil)
	result, err := h.App.DB.Exec(`INSERT INTO attempts(task_id,n,status,token,prompt,tmux_session,worktree_path) VALUES(?,1,'running','file-token','','file-attempt',?)`, task.id(), root)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := result.LastInsertId()
	attemptBase := fmt.Sprintf("/api/term/attempt/%d", attemptID)
	code, data = h.request("GET", attemptBase+"/file?path="+url.QueryEscape(filename), nil, nil)
	if code != 200 || !bytes.Equal(data, payload) {
		t.Fatalf("wrong worktree: %d", code)
	}
	code, attachment := upload(t, h.URL+attemptBase+"/attachments", "context.txt", []byte("context"), "")
	if code != 201 || !strings.HasPrefix(attachment.str("path"), root+"/") {
		t.Fatalf("wrong upload destination: %d %+v", code, attachment)
	}
	var shell obj
	h.decode("GET", fmt.Sprintf("/api/term/attempt-shell/%d/info", attemptID), nil, 200, &shell)
	if shell.str("workdir") != root {
		t.Fatalf("wrong companion shell: %+v", shell)
	}
}

func TestTerminalAuthCoversWebsocketAndFiles(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AuthToken = "terminal-secret" })
	for _, p := range []string{"/term/session/1/token", "/term/session/1/ws", "/api/term/session/1/info", "/api/term/session/1/files", "/api/term/session/1/file?path=x", "/api/term/session/1/history"} {
		code, _ := h.request("GET", p, nil, nil)
		if code != 401 {
			t.Fatalf("ungated %s: %d", p, code)
		}
	}
}
