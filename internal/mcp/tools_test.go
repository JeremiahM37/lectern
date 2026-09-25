package mcp

// End-to-end tests for the claim_work/release_work/list_claims MCP tools
// (docs/claims.md point 2) against a REAL lectern server (internal/app.App,
// mock target) — these tools are thin JSON-over-HTTP wrappers around
// POST/GET/DELETE /api/claims, so the thing worth proving is the whole
// chain: LECTERN_SESSION_ID resolution, the HTTP round trip, and the JSON
// shape a caller actually sees. internal/api/claims_test.go covers the
// underlying endpoints' own edge cases in depth; this file does not repeat
// them.

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/app"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func newTestApp(t *testing.T) (*app.App, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DBPath: filepath.Join(dir, "test.db"), Mock: true,
		TickInterval: 40 * time.Millisecond, MockAgentDelay: 40 * time.Millisecond,
		ApprovalPoll: 400 * time.Millisecond, ApprovalExpire: 900 * time.Second,
		JanitorDays: 7, ClaudeBin: "claude", CodexBin: "codex", GeminiBin: "gemini",
		VAPIDEmail:       "admin@example.com",
		HostClaudeConfig: filepath.Join(dir, "no-such-claude.json"),
		ClaudeCredsPath:  filepath.Join(dir, "no-such-creds.json"),
		CodexCredsPath:   filepath.Join(dir, "no-such-codex.json"),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.BaseURL = "http://" + ln.Addr().String()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: a.Handler()}}
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		a.Close()
	})
	return a, cfg.BaseURL
}

// mustSeededProject returns the demo project mock mode seeds.
func mustSeededProject(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rows, err := s.list("/projects")
	if err != nil || len(rows) == 0 {
		t.Fatalf("expected a seeded demo project, got %v (err=%v)", rows, err)
	}
	return rows[0]
}

func mustSession(t *testing.T, s *Server, projectID float64, name string) int64 {
	t.Helper()
	raw, err := s.api("POST", "/sessions", map[string]any{
		"project_id": projectID, "name": name, "agent": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := raw.(map[string]any)
	id, _ := sess["id"].(float64)
	if id == 0 {
		t.Fatalf("expected a session id, got %v", raw)
	}
	return int64(id)
}

func TestClaimWorkReleaseWorkListClaims(t *testing.T) {
	a, baseURL := newTestApp(t)
	s := New(baseURL, "")
	proj := mustSeededProject(t, s)
	sid := mustSession(t, s, proj["id"].(float64), "mcp-caller")

	// The MCP tools identify the calling session via LECTERN_SESSION_ID
	// (mediapost.SessionID()), exactly like the active_work tool.
	t.Setenv("LECTERN_SESSION_ID", fmt.Sprint(sid))

	// Awareness's repo-key resolution is async and shells a real git
	// command this test's mock target cannot satisfy — poke it directly,
	// same convention internal/api/claims_test.go uses.
	if err := a.DB.Update("sessions", sid, map[string]any{
		"repo_key": "1:/repo/.git", "repo_toplevel": "/repo"}); err != nil {
		t.Fatal(err)
	}

	// claim_work
	raw, err := s.call("claim_work", map[string]any{
		"scope_kind": "paths", "paths": []any{"frontend/src/sessions/**"}, "intent": "rename button",
	})
	if err != nil {
		t.Fatalf("claim_work: %v", err)
	}
	claim, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("expected an object result, got %#v", raw)
	}
	if claim["repo_key"] != "1:/repo/.git" {
		t.Fatalf("expected repo_key resolved from the session, got %v", claim)
	}
	claimID, _ := claim["id"].(float64)
	if claimID == 0 {
		t.Fatalf("expected a claim id, got %v", claim)
	}

	// list_claims (no args — resolves the caller's own session's repo).
	rawList, err := s.call("list_claims", map[string]any{})
	if err != nil {
		t.Fatalf("list_claims: %v", err)
	}
	list, ok := rawList.([]map[string]any)
	if !ok || len(list) != 1 {
		t.Fatalf("expected exactly one claim listed, got %#v", rawList)
	}

	// release_work by id.
	if _, err := s.call("release_work", map[string]any{"claim_id": claimID}); err != nil {
		t.Fatalf("release_work: %v", err)
	}
	rawList, err = s.call("list_claims", map[string]any{})
	if err != nil {
		t.Fatalf("list_claims after release: %v", err)
	}
	if list, _ := rawList.([]map[string]any); len(list) != 0 {
		t.Fatalf("expected no active claims after release_work, got %v", list)
	}
}

func TestReleaseWorkAll(t *testing.T) {
	a, baseURL := newTestApp(t)
	s := New(baseURL, "")
	proj := mustSeededProject(t, s)
	sid := mustSession(t, s, proj["id"].(float64), "mcp-caller-2")
	t.Setenv("LECTERN_SESSION_ID", fmt.Sprint(sid))
	if err := a.DB.Update("sessions", sid, map[string]any{
		"repo_key": "1:/repo2.git", "repo_toplevel": "/repo2"}); err != nil {
		t.Fatal(err)
	}

	// Two claims via claim_work.
	for _, topic := range []string{"first thing", "second thing"} {
		if _, err := s.call("claim_work", map[string]any{
			"scope_kind": "topic", "scope": topic}); err != nil {
			t.Fatalf("claim_work(%q): %v", topic, err)
		}
	}
	rawList, err := s.call("list_claims", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := rawList.([]map[string]any); len(list) != 2 {
		t.Fatalf("expected two active claims before release, got %v", list)
	}

	rawRelease, err := s.call("release_work", map[string]any{"all": true})
	if err != nil {
		t.Fatalf("release_work all: %v", err)
	}
	released, _ := rawRelease.(map[string]any)
	if n, _ := released["released"].(int); n != 2 {
		t.Fatalf("expected released:2, got %v", rawRelease)
	}

	rawList, err = s.call("list_claims", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := rawList.([]map[string]any); len(list) != 0 {
		t.Fatalf("expected no active claims after release_work all, got %v", list)
	}
}

func TestClaimWorkRequiresSessionID(t *testing.T) {
	_, baseURL := newTestApp(t)
	s := New(baseURL, "")
	// LECTERN_SESSION_ID deliberately unset.
	if _, err := s.call("claim_work", map[string]any{"scope_kind": "topic", "scope": "x"}); err == nil {
		t.Fatalf("expected an error with no LECTERN_SESSION_ID")
	}
}
