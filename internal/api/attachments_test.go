package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func upload(t *testing.T, url, filename string, data []byte, token string) (int, obj) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	f, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	w.Close()
	req, _ := http.NewRequest("POST", url, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out obj
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response %d: %s", res.StatusCode, raw)
	}
	return res.StatusCode, out
}

func TestRealAttachmentUploadAndTmuxDelivery(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	dir := t.TempDir()
	mustRun(t, "", "git", "init", "-q", dir)
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "local-upload", Kind: "local", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The controlled shell represents an adopted tmux session. Upload must not
	// type anything into it; the explicit send later proves the path is readable.
	mustRun(t, dir, "tmux", "new-session", "-d", "-s", "attachment-test", "bash --norc")
	sess, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "upload", Agent: "claude", Workdir: dir, TmuxSession: "attachment-test", Status: "idle", Origin: "adopted"})
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("%s/api/sessions/%d/attachments", h.URL, sess.ID)
	data := bytes.Repeat([]byte("%PDF-1.4\n\x00\xff\n"), 30000)
	code, attached := upload(t, url, `../../Résumé's $(touch owned).pdf`, data, "")
	if code != 201 {
		t.Fatalf("upload: %d %+v", code, attached)
	}
	file := attached.str("path")
	if !strings.HasPrefix(file, filepath.Join(dir, ".lectern", "context")+"/") {
		t.Fatalf("escaped destination: %s", file)
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file mismatch: %v", err)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0600 {
		t.Fatal("file is not private")
	}
	mustRun(t, dir, "git", "check-ignore", file)
	output := filepath.Join(dir, "received")
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("upload submitted to terminal")
	}
	h.post(fmt.Sprintf("/api/sessions/%d/send", sess.ID), obj{"text": "cat " + shellq.Quote(file) + " > " + shellq.Quote(output)}, 200)
	h.waitUntil("tmux reads original attachment", func() bool { got, _ := os.ReadFile(output); return bytes.Equal(got, data) })
	code, second := upload(t, url, attached.str("name"), []byte("different"), "")
	if code != 201 || second.str("path") == file {
		t.Fatal("duplicate filename overwrote original")
	}
	got, _ = os.ReadFile(file)
	if !bytes.Equal(got, data) {
		t.Fatal("original overwritten")
	}
	// Stage-name collision and zero-byte files are valid too.
	code, _ = upload(t, url, ".upload", nil, "")
	if code != 201 {
		t.Fatalf("empty file: %d", code)
	}
	// End the actual process and let the normal poll observe it. Writing only
	// the DB status races a poll that correctly still sees a live tmux shell.
	mustRun(t, dir, "tmux", "kill-session", "-t", "=attachment-test")
	h.waitUntil("session is observed dead", func() bool {
		row, err := h.App.DB.Session(sess.ID)
		return err == nil && row.Status == "dead"
	})
	code, _ = upload(t, url, "ended.pdf", data, "")
	if code != 409 {
		t.Fatalf("ended: %d", code)
	}
}

func TestAttachmentLimitsAuthAndTaskTarget(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AuthToken = "upload-token"; c.Auth = "token" })
	// Seed directly: authenticated endpoint is tested with actual multipart data.
	projects, _ := h.App.DB.Projects()
	p := projects[0]
	task, err := h.App.DB.InsertTask(&store.Task{ProjectID: p.ID, Title: "context", Prompt: "read", Status: "backlog", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("%s/api/tasks/%d/attachments", h.URL, task.ID)
	code, _ := upload(t, url, "test.pdf", []byte("pdf"), "")
	if code != 401 {
		t.Fatalf("unauthenticated: %d", code)
	}
	code, out := upload(t, url, "test.pdf", []byte("pdf"), "upload-token")
	if code != 201 || !strings.HasPrefix(out.str("path"), p.RepoPath+"/") {
		t.Fatalf("task upload %d %+v", code, out)
	}
	code, _ = upload(t, url, "big.pdf", make([]byte, (25<<20)+1), "upload-token")
	if code != 413 {
		t.Fatalf("oversize: %d", code)
	}
	code, _ = upload(t, url, "invalid.pdf", make([]byte, 26<<20), "upload-token")
	if code != 413 {
		t.Fatalf("body limit: %d", code)
	}
	h.App.DB.Update("targets", p.TargetID, map[string]any{"kind": "sandbox"})
	code, _ = upload(t, url, "test.pdf", []byte("pdf"), "upload-token")
	if code != 409 {
		t.Fatalf("sandbox upload: %d", code)
	}
}
