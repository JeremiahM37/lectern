// mcp_clients.go backs the Settings "Connect your AI tools" card: one place
// that tells a new user which MCP clients can be wired to this Lectern with a
// single click, runs that click for the ones that genuinely support it, and
// remembers which clients have actually spoken to `lectern mcp` at least
// once.
//
// Claude Code and Codex ship their own `mcp add`/`mcp get` subcommands, so for
// those two lectern can run the client's own registration command on the
// machine Lectern itself runs on (as the Lectern service user) — a true one
// click. Every other client (Claude Desktop, Cursor, VS Code, claude.ai,
// ChatGPT) gets the most automatic path it genuinely supports: a prefilled
// config snippet, a real deep link, or — where the client needs a public
// HTTPS endpoint Lectern does not expose — an honest explanation instead of a
// fake button.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// mcpAPIPlaceholder appears literally inside a "remote" command string. The
// browser is the only thing that reliably knows its own origin (this could be
// tailscale serve's https name, the direct :8443 TLS listener, or a plain LAN
// URL), so the server hands back a template and the frontend substitutes
// window.location.origin into it rather than the server guessing.
const mcpAPIPlaceholder = "__LECTERN_API__"

// mcpClientRunnerFunc runs one external command and returns its combined
// output. A field on Server (not a package var) so each test gets its own
// fake without cross-test global state, and so production wiring (internal/
// app) never has to remember to set anything — nil means "really exec it".
type mcpClientRunnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func runMCPClientCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (s *Server) mcpRunner() mcpClientRunnerFunc {
	if s.MCPClientRunner != nil {
		return s.MCPClientRunner
	}
	return runMCPClientCommand
}

// mcpClientSeenKey is the single settings row every recorded "last spoke to
// lectern mcp" timestamp lives in — one JSON blob rather than one row per
// client, so recording a new client never needs a schema change.
const mcpClientSeenKey = "mcp_clients_seen"

type mcpSeenRecord struct {
	Name    string `json:"name"`    // the clientInfo.name exactly as reported
	Version string `json:"version"` // clientInfo.version, if any
	At      int64  `json:"at"`      // unix seconds
}

// mcpClientInfo is one row of GET /api/mcp-clients. Not every field applies to
// every client: a CLI carries Command/RemoteCommand/CanInstall, a web surface
// carries only ExternalURL, and Claude Desktop/Cursor/VS Code carry neither —
// the frontend builds their config JSON / deep link itself from LecternPath,
// since only it knows this browser's own origin for the "remote" variant.
type mcpClientInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Installed  *bool  `json:"installed"` // nil: unknown or not applicable
	Detail     string `json:"detail"`
	CanInstall bool   `json:"can_install"`

	// LecternPath is this host's resolved lectern binary, repeated on every
	// row so the frontend can build a config/deep-link JSON body itself
	// without a second round trip.
	LecternPath string `json:"lectern_path,omitempty"`

	Command       string `json:"command,omitempty"`
	RemoteCommand string `json:"remote_command,omitempty"`

	ExternalURL string `json:"external_url,omitempty"`

	LastSeen *mcpSeenRecord `json:"last_seen,omitempty"`
}

// mcpInstallableClient describes one CLI lectern can register itself with.
type mcpInstallableClient struct {
	id, name string
	// cliName is the literal command a person would type — "claude"/"codex" —
	// used in the copyable command text. bin resolves the actual binary this
	// server execs, which is the same value unless LECTERN_CLAUDE_BIN/
	// LECTERN_CODEX_BIN points at something else (e.g. an absolute path
	// outside a systemd unit's PATH); the copyable command still says the
	// plain name because that is what a person's own shell resolves.
	cliName    string
	bin        func(cfg *config.Config) string
	detectArg  []string // argv after the binary, e.g. ["mcp", "get", "lectern"]
	installArg func(lecternPath string) []string
}

func mcpInstallableClients() []mcpInstallableClient {
	return []mcpInstallableClient{
		{
			id:        "claude-code",
			name:      "Claude Code (CLI)",
			cliName:   "claude",
			bin:       func(cfg *config.Config) string { return cfg.ClaudeBin },
			detectArg: []string{"mcp", "get", "lectern"},
			installArg: func(lecternPath string) []string {
				return []string{"mcp", "add", "--scope", "user", "lectern", "--", lecternPath, "mcp"}
			},
		},
		{
			id:        "codex",
			name:      "Codex (CLI)",
			cliName:   "codex",
			bin:       func(cfg *config.Config) string { return cfg.CodexBin },
			detectArg: []string{"mcp", "get", "lectern"},
			installArg: func(lecternPath string) []string {
				return []string{"mcp", "add", "lectern", "--", lecternPath, "mcp"}
			},
		},
	}
}

func mcpInstallableByID(id string) (mcpInstallableClient, bool) {
	for _, c := range mcpInstallableClients() {
		if c.id == id {
			return c, true
		}
	}
	return mcpInstallableClient{}, false
}

// mcpKnownSeenNames maps a clientInfo.name an MCP initialize handshake
// reports onto the installable client id it belongs to, so "Claude Code
// connected · used 2 min ago" lands on the right card. Anything not listed
// here is a client this card doesn't otherwise show a row for (Claude
// Desktop, Cursor, VS Code, or something not yet seen) — recordMCPClientSeen
// keeps it under its own raw name instead of dropping it.
var mcpKnownSeenNames = map[string]string{
	"claude-code":      "claude-code",
	"codex":            "codex",
	"codex-mcp-client": "codex",
	"codex-cli":        "codex",
}

// mcpSeenGroup resolves a reported clientInfo.name to the id its last-seen
// record should be filed under.
func mcpSeenGroup(name string) string {
	if id, ok := mcpKnownSeenNames[strings.ToLower(strings.TrimSpace(name))]; ok {
		return id
	}
	return strings.TrimSpace(name)
}

// lecternPath resolves the absolute path of the currently running lectern
// binary — what a local Claude Code / Codex / Claude Desktop / Cursor / VS
// Code, sharing this machine with Lectern, should launch. Overridable via
// Server.LecternPath so tests get a deterministic value instead of a test
// binary's own path.
func (s *Server) lecternPath() string {
	if s.LecternPath != "" {
		return s.LecternPath
	}
	p, err := os.Executable()
	if err != nil {
		return "lectern"
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// firstNonEmptyLine picks the first line of a CLI's own status output worth
// showing next to a client's card. A bare "<name>:" header — both `claude mcp
// get` and `codex mcp get` print one before their actual status line — is
// skipped as content-free.
func firstNonEmptyLine(s, fallback string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		return line
	}
	return fallback
}

func truncateOutput(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// mcpClientsList builds the whole GET /api/mcp-clients response: the CLI
// clients (detected live against the real binary), the clients that only need
// a config snippet or deep link, the two web surfaces that need neither, and
// any client that has actually connected but isn't one of the above.
func (s *Server) mcpClientsList(ctx context.Context) []mcpClientInfo {
	lecternPath := s.lecternPath()
	seen := s.loadMCPClientsSeen()
	out := make([]mcpClientInfo, 0, 8)

	for _, def := range mcpInstallableClients() {
		info := mcpClientInfo{
			ID:            def.id,
			Name:          def.name,
			CanInstall:    true,
			LecternPath:   lecternPath,
			Command:       mcpLocalCommand(def, lecternPath),
			RemoteCommand: mcpRemoteCommand(def),
			LastSeen:      seen[def.id],
		}
		bin := def.bin(s.Cfg)
		resolved, err := exec.LookPath(bin)
		if err != nil {
			info.CanInstall = false
			info.Detail = bin + " is not on this server's PATH — run the command below on the machine where you sign in to it, or set LECTERN_" + strings.ToUpper(def.cliName) + "_BIN if it's installed somewhere else."
			out = append(out, info)
			continue
		}
		detectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out2, runErr := s.mcpRunner()(detectCtx, resolved, def.detectArg...)
		cancel()
		installed := runErr == nil
		info.Installed = &installed
		if installed {
			info.Detail = firstNonEmptyLine(string(out2), "Already connected")
		} else {
			info.Detail = firstNonEmptyLine(string(out2), "Not connected yet")
		}
		out = append(out, info)
	}

	out = append(out,
		mcpClientInfo{
			ID:          "claude-desktop",
			Name:        "Claude Desktop",
			LecternPath: lecternPath,
			Detail:      "Settings → Developer → Edit Config, then paste the snippet below and restart Claude Desktop.",
			LastSeen:    seen["claude-desktop"],
		},
		mcpClientInfo{
			ID:          "cursor",
			Name:        "Cursor",
			LecternPath: lecternPath,
			Detail:      "One-click install via Cursor's MCP deep link.",
			LastSeen:    seen["cursor"],
		},
		mcpClientInfo{
			ID:          "vscode",
			Name:        "VS Code",
			LecternPath: lecternPath,
			Detail:      "One-click install via VS Code's MCP deep link.",
			LastSeen:    seen["vscode"],
		},
		mcpClientInfo{
			ID:          "web-connectors",
			Name:        "claude.ai / ChatGPT (web)",
			Detail:      "These need a public HTTPS MCP endpoint, which Lectern does not expose by default. Use a CLI or desktop client above instead, or expose one yourself and add it as a connector.",
			ExternalURL: "https://claude.ai/customize/connectors",
			LastSeen:    seen["web-connectors"],
		},
	)

	known := map[string]bool{}
	for _, row := range out {
		known[row.ID] = true
	}
	var extra []string
	for id := range seen {
		if !known[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		rec := seen[id]
		out = append(out, mcpClientInfo{
			ID:       id,
			Name:     rec.Name,
			LastSeen: rec,
		})
	}
	return out
}

func mcpLocalCommand(def mcpInstallableClient, lecternPath string) string {
	return def.cliName + " " + strings.Join(quoteArgs(def.installArg(lecternPath)), " ")
}

// mcpRemoteCommand builds the flavor of the install command for a client
// running on a machine other than the one hosting Lectern: no absolute path
// (the remote machine has its own `lectern` on PATH) and an explicit
// LECTERN_API pointed at this browser's own origin, filled in client-side.
func mcpRemoteCommand(def mcpInstallableClient) string {
	switch def.id {
	case "claude-code":
		return "claude mcp add --scope user lectern -e LECTERN_API=" + mcpAPIPlaceholder + " -- lectern mcp"
	case "codex":
		return "codex mcp add lectern --env LECTERN_API=" + mcpAPIPlaceholder + " -- lectern mcp"
	default:
		return ""
	}
}

// quoteArgs is a display-only, not shell-executed, best-effort quoting: the
// backend never passes these strings to a shell (installMCPClient always
// execs with an explicit argv), so this only has to look right when a person
// pastes it into their own terminal.
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			out[i] = "\"" + strings.ReplaceAll(a, "\"", "\\\"") + "\""
		} else {
			out[i] = a
		}
	}
	return out
}

func (s *Server) listMCPClients(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, 200, s.mcpClientsList(ctx))
}

// installMCPClient runs a client's own registration command on this machine.
// This writes the operator's own agent config (a user-scope MCP server
// entry), which is exactly the kind of consequential, unsupervised action the
// rest of this codebase already reserves for a signed-in human — see
// autonomyHuman in autonomy.go and decideApproval in approvals.go, which this
// mirrors. In LECTERN_AUTH=none (single-machine, no-login-ever) mode
// Auth.CanDecide is unconditionally true, matching every other such gate.
func (s *Server) installMCPClient(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(principal) {
		httpError(w, 403, "connecting an AI tool writes your local agent configuration; this requires your signed-in identity, not an automated caller")
		return
	}
	id := r.PathValue("id")
	def, ok := mcpInstallableByID(id)
	if !ok {
		httpError(w, 404, "%q cannot be installed automatically from here", id)
		return
	}
	bin := def.bin(s.Cfg)
	resolved, err := exec.LookPath(bin)
	if err != nil {
		httpError(w, 409, "%s is not on this server's PATH", bin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, runErr := s.mcpRunner()(ctx, resolved, def.installArg(s.lecternPath())...)
	writeJSON(w, 200, map[string]any{
		"ok":     runErr == nil,
		"output": truncateOutput(strings.TrimSpace(string(out)), 2048),
	})
}

// mcpClientSeen is best-effort telemetry POSTed by `lectern mcp` itself (see
// internal/mcp/mcp.go's initialize handler) — not a human action, so it needs
// no CanDecide gate, only the ordinary API auth every /api route already
// requires. A local MCP server process authenticates as KindLocal, which
// every auth mode already lets use the ordinary API.
func (s *Server) mcpClientSeen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientName    string `json:"client_name"`
		ClientVersion string `json:"client_version"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		httpError(w, 400, "client_name required")
		return
	}
	s.recordMCPClientSeen(name, strings.TrimSpace(in.ClientVersion))
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) loadMCPClientsSeen() map[string]*mcpSeenRecord {
	s.mcpClientsMu.Lock()
	defer s.mcpClientsMu.Unlock()
	out := map[string]*mcpSeenRecord{}
	raw := s.DB.Setting(mcpClientSeenKey)
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (s *Server) recordMCPClientSeen(name, version string) {
	s.mcpClientsMu.Lock()
	defer s.mcpClientsMu.Unlock()
	current := map[string]*mcpSeenRecord{}
	if raw := s.DB.Setting(mcpClientSeenKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &current)
	}
	id := mcpSeenGroup(name)
	current[id] = &mcpSeenRecord{Name: name, Version: version, At: time.Now().Unix()}
	raw, _ := json.Marshal(current)
	_ = s.DB.SetSetting(mcpClientSeenKey, string(raw))
}
