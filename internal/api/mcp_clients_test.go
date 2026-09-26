package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// mcpTestServer builds a Server wired only for the connect-tools endpoints:
// a real temp SQLite DB (for the settings-backed last-seen store) and no real
// executor, scheduler or target — those endpoints never touch any of that.
func mcpTestServer(t *testing.T, mode auth.Mode) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{
		DB:          db,
		Auth:        &auth.Resolver{Mode: mode},
		Cfg:         &config.Config{ClaudeBin: "claude", CodexBin: "codex"},
		LecternPath: "/opt/lectern/lectern",
	}
}

// fakeRunnerCall records one invocation a fake runner received.
type fakeRunnerCall struct {
	name string
	args []string
}

func TestMCPClientsListReflectsDetection(t *testing.T) {
	s := mcpTestServer(t, auth.ModeTailscale)
	// exec.LookPath must actually resolve a binary for detection to run at
	// all, and the fake runner never touches a real claude/codex — so point
	// the config at two distinct, always-present stand-ins ("true"/"false",
	// which every Linux box ships) and key the fake runner's canned output off
	// which one was resolved.
	claudeBin := lookPathOrSkip(t, "true")
	codexBin := lookPathOrSkip(t, "false")
	s.Cfg.ClaudeBin = claudeBin
	s.Cfg.CodexBin = codexBin
	var calls []fakeRunnerCall
	s.MCPClientRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, fakeRunnerCall{name, args})
		switch name {
		case claudeBin:
			return []byte("lectern:\n  Status: ✔ Connected\n"), nil
		case codexBin:
			return []byte("Error: No MCP server named 'lectern' found.\n"), errExit
		}
		t.Fatalf("unexpected binary %q", name)
		return nil, nil
	}

	rows := s.mcpClientsList(context.Background())
	byID := map[string]mcpClientInfo{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	claude, ok := byID["claude-code"]
	if !ok {
		t.Fatalf("no claude-code row: %+v", rows)
	}
	if claude.Installed == nil || !*claude.Installed {
		t.Errorf("claude-code should be detected installed: %+v", claude)
	}
	if !strings.Contains(claude.Detail, "Connected") {
		t.Errorf("claude-code detail should surface the CLI's own status line: %q", claude.Detail)
	}
	if claude.Command != "claude mcp add --scope user lectern -- /opt/lectern/lectern mcp" {
		t.Errorf("unexpected claude install command: %q", claude.Command)
	}
	if claude.RemoteCommand != "claude mcp add --scope user lectern -e LECTERN_API=__LECTERN_API__ -- lectern mcp" {
		t.Errorf("unexpected claude remote command: %q", claude.RemoteCommand)
	}
	if !claude.CanInstall {
		t.Errorf("claude-code is on PATH and must be marked installable: %+v", claude)
	}

	codex, ok := byID["codex"]
	if !ok {
		t.Fatalf("no codex row: %+v", rows)
	}
	if codex.Installed == nil || *codex.Installed {
		t.Errorf("codex should be detected NOT installed: %+v", codex)
	}
	if codex.Command != "codex mcp add lectern -- /opt/lectern/lectern mcp" {
		t.Errorf("unexpected codex install command: %q", codex.Command)
	}
	if codex.RemoteCommand != "codex mcp add lectern --env LECTERN_API=__LECTERN_API__ -- lectern mcp" {
		t.Errorf("unexpected codex remote command: %q", codex.RemoteCommand)
	}

	// The other clients need no CLI at all — no detection call was made for
	// them, and they still carry the lectern path the frontend needs to build
	// a config snippet or deep link itself.
	desktop, ok := byID["claude-desktop"]
	if !ok || desktop.LecternPath != "/opt/lectern/lectern" || desktop.CanInstall {
		t.Errorf("unexpected claude-desktop row: %+v", desktop)
	}
	cursor, ok := byID["cursor"]
	if !ok || cursor.CanInstall {
		t.Errorf("unexpected cursor row: %+v", cursor)
	}
	vscode, ok := byID["vscode"]
	if !ok || vscode.CanInstall {
		t.Errorf("unexpected vscode row: %+v", vscode)
	}
	web, ok := byID["web-connectors"]
	if !ok || web.ExternalURL != "https://claude.ai/customize/connectors" {
		t.Errorf("unexpected web-connectors row: %+v", web)
	}
	if len(calls) != 2 {
		t.Errorf("expected exactly one detection call per installable CLI, got %+v", calls)
	}
}

// errExit stands in for a nonzero-exit CLI failure without depending on a
// real *exec.ExitError, which the fake runner never actually produces.
var errExit = &fakeExitError{}

type fakeExitError struct{}

func (*fakeExitError) Error() string { return "exit status 1" }

func lookPathOrSkip(t *testing.T, name string) string {
	t.Helper()
	// /bin/sh (or an equivalent resolvable name) exists on every CI/dev box
	// this suite runs on; if it somehow doesn't, skip rather than fail on an
	// environment quirk unrelated to the feature under test.
	for _, candidate := range []string{"/bin/" + name, "/usr/bin/" + name} {
		if fileExists(candidate) {
			return candidate
		}
	}
	t.Skipf("no resolvable stand-in binary %q found on this machine", name)
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestMCPClientNotOnPATHOffersCommandInstead(t *testing.T) {
	s := mcpTestServer(t, auth.ModeTailscale)
	s.Cfg.ClaudeBin = "no-such-claude-binary-xyz"
	s.Cfg.CodexBin = "no-such-codex-binary-xyz"
	s.MCPClientRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatal("must not exec a detection command for a binary that isn't on PATH")
		return nil, nil
	}
	rows := s.mcpClientsList(context.Background())
	for _, r := range rows {
		if r.ID != "claude-code" {
			continue
		}
		if r.Installed != nil {
			t.Errorf("installed must be unknown (nil), not %v", *r.Installed)
		}
		if r.CanInstall {
			t.Error("a missing binary must not be offered as one-click installable")
		}
		if !strings.Contains(r.Detail, "not on this server's PATH") {
			t.Errorf("detail should explain the binary is missing: %q", r.Detail)
		}
		if r.Command == "" {
			t.Error("the copyable command must still be offered when the binary is missing locally")
		}
		return
	}
	t.Fatal("no claude-code row returned")
}

func TestInstallMCPClientRequiresHuman(t *testing.T) {
	s := mcpTestServer(t, auth.ModeTailscale)
	var ran atomic.Bool
	s.MCPClientRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran.Store(true)
		return []byte("ok"), nil
	}
	r := httptest.NewRequest("POST", "/api/mcp-clients/claude-code/install", nil)
	r.SetPathValue("id", "claude-code")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindLocal}))
	w := httptest.NewRecorder()
	s.installMCPClient(w, r)
	if w.Code != 403 {
		t.Fatalf("a non-human caller must be refused, got %d: %s", w.Code, w.Body.String())
	}
	if ran.Load() {
		t.Fatal("the install command must never run for a rejected caller")
	}
}

func TestInstallMCPClientRunsForHumanAndReturnsOutput(t *testing.T) {
	s := mcpTestServer(t, auth.ModeTailscale)
	s.Cfg.ClaudeBin = lookPathOrSkip(t, "sh")
	var got fakeRunnerCall
	var sawDeadline bool
	s.MCPClientRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got = fakeRunnerCall{name, args}
		if dl, ok := ctx.Deadline(); ok {
			remaining := time.Until(dl)
			sawDeadline = remaining > 0 && remaining <= 20*time.Second
		}
		return []byte("lectern added\n"), nil
	}
	r := httptest.NewRequest("POST", "/api/mcp-clients/claude-code/install", nil)
	r.SetPathValue("id", "claude-code")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindTailscale, Human: true}))
	w := httptest.NewRecorder()
	s.installMCPClient(w, r)
	if w.Code != 200 {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var out struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Output != "lectern added" {
		t.Errorf("unexpected response: %+v", out)
	}
	wantArgs := []string{"mcp", "add", "--scope", "user", "lectern", "--", "/opt/lectern/lectern", "mcp"}
	if strings.Join(got.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("unexpected install argv: %v", got.args)
	}
	if !sawDeadline {
		t.Error("the install runner must be given a bounded context (the 20s install timeout)")
	}
}

func TestInstallMCPClientReportsFailure(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone) // ModeNone: CanDecide is always true
	s.Cfg.CodexBin = lookPathOrSkip(t, "sh")
	s.MCPClientRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Error: something went wrong"), errExit
	}
	r := httptest.NewRequest("POST", "/api/mcp-clients/codex/install", nil)
	r.SetPathValue("id", "codex")
	w := httptest.NewRecorder()
	s.installMCPClient(w, r)
	if w.Code != 200 {
		t.Fatalf("a failed install is still a successful HTTP call: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.OK {
		t.Error("ok must be false when the client's own command failed")
	}
	if !strings.Contains(out.Output, "something went wrong") {
		t.Errorf("failure output must reach the caller: %q", out.Output)
	}
}

func TestInstallMCPClientOutputIsTruncated(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	s.Cfg.ClaudeBin = lookPathOrSkip(t, "sh")
	s.MCPClientRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(strings.Repeat("x", 5000)), nil
	}
	r := httptest.NewRequest("POST", "/api/mcp-clients/claude-code/install", nil)
	r.SetPathValue("id", "claude-code")
	w := httptest.NewRecorder()
	s.installMCPClient(w, r)
	var out struct {
		Output string `json:"output"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Output) > 2048+len("…") {
		t.Errorf("output must be capped near 2KB, got %d bytes", len(out.Output))
	}
}

func TestInstallMCPClientUnknownID(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	r := httptest.NewRequest("POST", "/api/mcp-clients/cursor/install", nil)
	r.SetPathValue("id", "cursor")
	w := httptest.NewRecorder()
	s.installMCPClient(w, r)
	if w.Code != 404 {
		t.Fatalf("cursor cannot be installed automatically; want 404, got %d", w.Code)
	}
}

func TestInstallMCPClientBinaryMissing(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	s.Cfg.ClaudeBin = "no-such-claude-binary-xyz"
	r := httptest.NewRequest("POST", "/api/mcp-clients/claude-code/install", nil)
	r.SetPathValue("id", "claude-code")
	w := httptest.NewRecorder()
	s.installMCPClient(w, r)
	if w.Code != 409 {
		t.Fatalf("a missing binary must 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMCPClientSeenRecordsAndSurfacesLastSeen(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	r := httptest.NewRequest("POST", "/api/mcp-clients/seen",
		strings.NewReader(`{"client_name":"claude-code","client_version":"1.2.3"}`))
	w := httptest.NewRecorder()
	s.mcpClientSeen(w, r)
	if w.Code != 200 {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	rows := s.mcpClientsList(context.Background())
	for _, row := range rows {
		if row.ID != "claude-code" {
			continue
		}
		if row.LastSeen == nil || row.LastSeen.Version != "1.2.3" {
			t.Fatalf("last_seen not recorded on claude-code: %+v", row)
		}
		if row.LastSeen.At <= 0 {
			t.Errorf("last_seen.at must be a real timestamp: %+v", row.LastSeen)
		}
		return
	}
	t.Fatal("no claude-code row")
}

func TestMCPClientSeenMapsKnownAliases(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	for _, alias := range []string{"codex-mcp-client", "codex-cli"} {
		r := httptest.NewRequest("POST", "/api/mcp-clients/seen",
			strings.NewReader(`{"client_name":"`+alias+`"}`))
		w := httptest.NewRecorder()
		s.mcpClientSeen(w, r)
		if w.Code != 200 {
			t.Fatalf("alias %q: status %d", alias, w.Code)
		}
	}
	rows := s.mcpClientsList(context.Background())
	for _, row := range rows {
		if row.ID == "codex" {
			if row.LastSeen == nil {
				t.Fatalf("codex aliases must be folded into the codex row: %+v", rows)
			}
			return
		}
	}
	t.Fatal("no codex row")
}

func TestMCPClientSeenListsUnknownClientsAsIs(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	r := httptest.NewRequest("POST", "/api/mcp-clients/seen",
		strings.NewReader(`{"client_name":"some-future-client","client_version":"0.1"}`))
	w := httptest.NewRecorder()
	s.mcpClientSeen(w, r)
	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}
	rows := s.mcpClientsList(context.Background())
	for _, row := range rows {
		if row.ID == "some-future-client" {
			if row.Name != "some-future-client" || row.LastSeen == nil {
				t.Errorf("unknown client must be listed as-is: %+v", row)
			}
			return
		}
	}
	t.Fatalf("unknown client never appeared in the list: %+v", rows)
}

func TestMCPClientSeenRejectsEmptyName(t *testing.T) {
	s := mcpTestServer(t, auth.ModeNone)
	r := httptest.NewRequest("POST", "/api/mcp-clients/seen", strings.NewReader(`{"client_name":"  "}`))
	w := httptest.NewRecorder()
	s.mcpClientSeen(w, r)
	if w.Code != 400 {
		t.Fatalf("blank client_name must 400, got %d", w.Code)
	}
}

// A client registered with a versioned release path breaks at the next
// upgrade; when the stable name on PATH leads to this binary, use that.
func TestLecternPathPrefersStableNameOnPath(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "lectern")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got := (&Server{}).lecternPath(); got != link {
		t.Fatalf("lecternPath() = %q, want the stable PATH entry %q", got, link)
	}
}
