package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// fakeOpenAI is an OpenAI-compatible SSE server with a script keyed on the
// last user message: WRITE asks for write_file, SHELL asks for a destructive
// shell command. After a tool result it answers with plain text.
func fakeOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(chunk map[string]any) {
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		text := func(s string) {
			send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": s}, "finish_reason": nil}}})
			send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		}
		call := func(name string, args map[string]any) {
			a, _ := json.Marshal(args)
			id := fmt.Sprintf("call_%d", calls.Add(1))
			send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": string(a)}}},
			}, "finish_reason": nil}}})
			send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" {
			text("All done.")
		} else {
			switch prompt := contentText(last.Content); {
			case strings.Contains(prompt, "WRITE"):
				call("write_file", map[string]any{"path": "hello.txt", "content": "hi there\n"})
			case strings.Contains(prompt, "SHELL"):
				call("shell", map[string]any{"command": "rm -rf build"})
			default:
				text("ok")
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// testBackend builds a git repo, a SCRIBE_HOME that points at the fake server,
// and a Backend around an App opened on the repo.
func testBackend(t *testing.T) (tui.Backend, string) {
	t.Helper()
	srv := fakeOpenAI(t)
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	cfg := map[string]any{
		"model": "fake/test-model",
		"providers": []any{map[string]any{
			"id": "fake", "name": "Fake", "base_url": srv.URL + "/v1", "models": []string{"test-model"},
		}},
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "scribe@example.test"},
		{"config", "user.name", "Scribe Test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	host := NewTUIHost()
	a, err := Open(context.Background(), Options{Workdir: repo, Host: host, Version: "test"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	be := NewBackend(a, host)
	t.Cleanup(func() { _ = be.Close() })
	return be, repo
}

// runTurn submits prompt and reads events and prompts until the turn ends.
// answer handles approval prompts; nil means no prompt is expected.
func runTurn(t *testing.T, be tui.Backend, prompt string, answer func(tui.Prompt)) []agent.Event {
	t.Helper()
	be.Submit(prompt, nil)
	var evs []agent.Event
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-be.Events():
			evs = append(evs, e)
			if e.Kind == agent.EvTurnDone {
				return evs
			}
		case p := <-be.Prompts():
			if answer == nil {
				t.Fatalf("unexpected prompt: %+v", p)
			}
			answer(p)
		case <-deadline:
			t.Fatalf("turn did not finish; last events: %v", kinds(evs))
		}
	}
}

func kinds(evs []agent.Event) []agent.EventKind {
	var out []agent.EventKind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func toolResult(evs []agent.Event, tool string) *tools.Result {
	for _, e := range evs {
		if e.Kind == agent.EvToolResult && e.Tool == tool && e.Result != nil {
			return e.Result
		}
	}
	return nil
}

func TestBackendWriteUndoRedoSessionsExport(t *testing.T) {
	be, repo := testBackend(t)
	hello := filepath.Join(repo, "hello.txt")

	evs := runTurn(t, be, "WRITE a greeting file", nil)
	if r := toolResult(evs, "write_file"); r == nil || r.IsError {
		t.Fatalf("write_file did not succeed: %+v", r)
	}
	if got, err := os.ReadFile(hello); err != nil || string(got) != "hi there\n" {
		t.Fatalf("hello.txt = %q, %v", got, err)
	}
	if be.Model() != "fake/test-model" {
		t.Fatalf("model = %q", be.Model())
	}

	cps := be.Checkpoints()
	if len(cps) != 1 || len(cps[0].Files) != 1 || cps[0].Files[0] != "hello.txt" {
		t.Fatalf("checkpoints = %+v", cps)
	}
	oldID := be.SessionID()
	if oldID == "" {
		t.Fatal("session has no id")
	}

	if _, err := be.Undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Stat(hello); !os.IsNotExist(err) {
		t.Fatalf("undo should remove the created file, stat err = %v", err)
	}
	if _, err := be.Redo(); err != nil {
		t.Fatalf("redo: %v", err)
	}
	if _, err := os.Stat(hello); err != nil {
		t.Fatalf("redo should restore the file: %v", err)
	}
	if _, err := be.Undo(); err != nil {
		t.Fatalf("second undo: %v", err)
	}

	sessions := be.Sessions()
	var found bool
	for _, s := range sessions {
		if s.ID == oldID {
			found = true
			if !strings.Contains(s.Preview, "WRITE") {
				t.Errorf("preview = %q", s.Preview)
			}
		}
	}
	if !found {
		t.Fatalf("session %s not listed: %+v", oldID, sessions)
	}

	path, err := be.Export("markdown")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(repo, ".scribe", "exports")) {
		t.Errorf("export path = %s", path)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "WRITE a greeting file") {
		t.Fatalf("export content: %v\n%s", err, data)
	}

	if err := be.NewSession(); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if be.SessionID() == oldID {
		t.Fatal("new session kept the old id")
	}
	if err := be.Resume(oldID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if be.SessionID() != oldID {
		t.Fatalf("resumed to %s, want %s", be.SessionID(), oldID)
	}
	var replayed bool
	for _, e := range be.Transcript() {
		if e.Kind == agent.EvTurnStart && e.Prompt == "WRITE a greeting file" {
			replayed = true
		}
	}
	if !replayed {
		t.Fatalf("transcript did not replay the turn: %v", kinds(be.Transcript()))
	}
	if err := be.Rename("Greeting work"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if be.SessionTitle() != "Greeting work" {
		t.Fatalf("title = %q", be.SessionTitle())
	}
}

func TestBackendDestructiveShellDenied(t *testing.T) {
	be, repo := testBackend(t)
	keep := filepath.Join(repo, "build", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var seen []tools.Approval
	evs := runTurn(t, be, "SHELL clean the build", func(p tui.Prompt) {
		if p.Approval == nil {
			t.Errorf("expected an approval, got %+v", p)
			p.Reply <- tui.PromptReply{Decision: tools.Deny}
			return
		}
		seen = append(seen, *p.Approval)
		p.Reply <- tui.PromptReply{Decision: tools.Deny}
	})
	if len(seen) != 1 || seen[0].Kind != "destructive" || seen[0].Tool != "shell" {
		t.Fatalf("approvals = %+v", seen)
	}
	r := toolResult(evs, "shell")
	if r == nil || !r.IsError || !strings.Contains(r.Output, "Denied") {
		t.Fatalf("shell result = %+v, want a denial", r)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("denied command must not run: %v", err)
	}
}

func TestTUIHostAllowSessionIsRemembered(t *testing.T) {
	h := NewTUIHost()
	var prompts atomic.Int64
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case p := <-h.prompts:
				prompts.Add(1)
				p.Reply <- tui.PromptReply{Decision: tools.AllowSession}
			case <-stop:
				return
			}
		}
	}()
	ap := tools.Approval{Tool: "shell", Kind: "destructive", Title: "rm", Detail: "rm -rf x"}
	if d := h.Approve(context.Background(), ap); d != tools.AllowSession {
		t.Fatalf("first decision = %v", d)
	}
	if d := h.Approve(context.Background(), ap); d != tools.AllowSession {
		t.Fatalf("second decision = %v", d)
	}
	if n := prompts.Load(); n != 1 {
		t.Fatalf("prompted %d times, want 1", n)
	}
	h.resetSession()
	_ = h.Approve(context.Background(), ap)
	if n := prompts.Load(); n != 2 {
		t.Fatalf("after reset prompted %d times, want 2", n)
	}
}

func TestPumpNeverBlocksAndKeepsOrder(t *testing.T) {
	p := newPump()
	const n = eventBuffer + 3000
	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			p.push(agent.Event{Kind: agent.EvToolProgress, Text: fmt.Sprint(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("push blocked with no reader")
	}
	for i := 0; i < n; i++ {
		select {
		case e := <-p.out:
			if e.Text != fmt.Sprint(i) {
				t.Fatalf("event %d out of order: got %q", i, e.Text)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d never arrived", i)
		}
	}
}

func TestPumpMergesDeltasBehindABacklog(t *testing.T) {
	p := newPump()
	for i := 0; i < eventBuffer; i++ {
		p.push(agent.Event{Kind: agent.EvWarning, Text: "fill"})
	}
	// The first queued event is in flight to out, so it must not absorb deltas.
	p.push(agent.Event{Kind: agent.EvWarning, Text: "queued"})
	p.push(agent.Event{Kind: agent.EvText, Text: "he"})
	p.push(agent.Event{Kind: agent.EvText, Text: "llo"})
	p.push(agent.Event{Kind: agent.EvTurnDone})
	var tail []agent.Event
	for len(tail) < 3 {
		e := <-p.out
		if e.Text == "fill" {
			continue
		}
		tail = append(tail, e)
	}
	if tail[0].Text != "queued" {
		t.Fatalf("order lost at the head: %+v", tail[0])
	}
	if tail[1].Kind != agent.EvText || tail[1].Text != "hello" {
		t.Fatalf("deltas not merged: %+v", tail[1])
	}
	if tail[2].Kind != agent.EvTurnDone {
		t.Fatalf("order lost: %+v", tail[2])
	}
}
