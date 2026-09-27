package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const apiPluginManifest = `id: acme.kit
name: Kit
version: 1.0.0
capabilities:
  agents: true
  mcp_tools: true
contributes:
  agents:
    - {name: kit-agent, command: kit, display_name: Kit Agent, vendor: Acme, group: Popular, icon: {glyph: K, color: "#123456"}, homepage: "https://example.com", description: d, install_hint: "npm i kit", source: docs, verified_by: docs, verified_at: "2026-09-27"}
  mcp_servers:
    kitdocs: {url: "https://mcp.example.com"}
  skills:
    - {id: review, path: skills/review, description: Reviews}
  quick_commands:
    - {id: go, label: Go, text: "go"}
  themes:
    - {id: forest, name: Forest, accent: "#2f855a", dark: {bg: "#0b1410"}}
  palette_commands:
    - {id: open, title: "Kit: open plugins", href: "#settings/plugins"}
`

func writeAPIPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "review"), 0o755)
	os.WriteFile(filepath.Join(dir, "lectern-plugin.yaml"), []byte(apiPluginManifest), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "review", "SKILL.md"), []byte("---\nname: review\n---\n"), 0o644)
	return dir
}

func strs(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			s, _ := x.(string)
			out = append(out, s)
		}
	}
	return out
}

// A plugin can run commands on the server and every machine, so installing,
// consenting, enabling and adding sources are a person's decisions. In
// tailscale mode a process on this machine — the CLI, the MCP server, an
// agent — reaches the API as a non-human local principal and is refused.
func TestPluginChangesNeedASignedInPerson(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Auth = "tailscale"
		c.TailscaleUsers = "nobody@example.com"
		c.AuthToken = "person-token"
	})
	dir := writeAPIPlugin(t)
	person := map[string]string{"Authorization": "Bearer person-token"}

	for _, c := range []struct{ method, path string }{
		{"POST", "/api/plugins/preview"}, {"POST", "/api/plugins/install"},
		{"PUT", "/api/plugins/lectern.spec-kit"}, {"DELETE", "/api/plugins/acme.kit"},
		{"POST", "/api/plugins/acme.kit/update"}, {"POST", "/api/plugins/acme.kit/trust"},
		{"PUT", "/api/plugins/acme.kit/secrets"}, {"POST", "/api/plugin-sources"},
		{"DELETE", "/api/plugin-sources/x"},
	} {
		if code, body := h.request(c.method, c.path, obj{"kind": "path", "path": dir}, nil); code != 403 {
			t.Errorf("%s %s by a local process: %d %s", c.method, c.path, code, body)
		}
	}
	// Reading stays open to the ordinary API.
	if code := h.status("GET", "/api/plugins", nil); code != 200 {
		t.Fatalf("listing plugins: %d", code)
	}

	code, raw := h.request("POST", "/api/plugins/preview", obj{"kind": "path", "path": dir}, person)
	if code != 200 {
		t.Fatalf("preview by a person: %d %s", code, raw)
	}
	var pv struct {
		Hash   string   `json:"hash"`
		Accept []string `json:"accept"`
	}
	json.Unmarshal(raw, &pv)
	// Even with the preview's hash in hand, a local process cannot consent.
	if code, _ := h.request("POST", "/api/plugins/install", obj{"hash": pv.Hash, "accept": pv.Accept}, nil); code != 403 {
		t.Fatalf("a local process consented: %d", code)
	}
	code, raw = h.request("POST", "/api/plugins/install", obj{"hash": pv.Hash, "accept": pv.Accept}, person)
	if code != 200 {
		t.Fatalf("install by a person: %d %s", code, raw)
	}
	var installed obj
	json.Unmarshal(raw, &installed)
	if installed.str("status") != "active" || installed.str("consented_by") != "token" {
		t.Fatalf("installed %v", installed)
	}
}

func TestPluginContributionsReachTheirPlaces(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth = "none" })
	pid := h.seededProjectID()
	pv := h.post("/api/plugins/preview", obj{"kind": "path", "path": writeAPIPlugin(t)}, 200)
	// Consenting to a different capability list than the preview's is refused.
	h.post("/api/plugins/install", obj{"hash": pv.str("hash"), "accept": []string{"agents"}}, 409)
	h.post("/api/plugins/install", obj{"hash": pv.str("hash"), "accept": pv["accept"]}, 200)

	// The catalog: the bundled presets, unchanged, then the plugin's.
	cat := h.getList("/api/agents/catalog")
	if len(cat) != len(sessions.Catalog())+1 {
		t.Fatalf("catalog has %d entries, want %d", len(cat), len(sessions.Catalog())+1)
	}
	last := cat[len(cat)-1]
	if last.str("name") != "kit-agent" || last.str("plugin") != "acme.kit" || cat[0].str("plugin") != sessions.CatalogPluginID {
		t.Fatalf("catalog ends with %v", last)
	}

	// Project workflows: the bundled workflows, then the plugin's skill.
	wf := h.get(fmt.Sprintf("/api/projects/%d/workflows?agent=claude", pid)).list("workflows")
	var ids []string
	for _, w := range wf {
		ids = append(ids, w.str("id")+"/"+w.str("kind"))
	}
	if strings.Join(ids, ",") != "delegate/workflow,maestro/workflow,spec-kit/workflow,acme.kit:review/skill" {
		t.Fatalf("workflows %v", ids)
	}

	// The launch preview lists the plugin's MCP server beside the project's.
	capv := h.get(fmt.Sprintf("/api/projects/%d/capability", pid))
	if !strings.Contains(strings.Join(strs(capv["mcp_servers"]), ","), "kitdocs") {
		t.Fatalf("capability mcp_servers %v", capv["mcp_servers"])
	}

	ui := h.get("/api/plugins/contributions")
	if len(ui.list("quick_commands")) != 1 || len(ui.list("themes")) != 1 || len(ui.list("palette_commands")) != 1 {
		t.Fatalf("ui contributions %v", ui)
	}

	// Disabling the bundled catalog removes its presets but not the plugin's;
	// disabling the plugin removes the rest.
	h.request2("PUT", "/api/plugins/"+sessions.CatalogPluginID, obj{"enabled": false}, 200)
	if cat := h.getList("/api/agents/catalog"); len(cat) != 1 {
		t.Fatalf("catalog with the bundled plugin off: %d entries", len(cat))
	}
	h.request2("PUT", "/api/plugins/acme.kit", obj{"enabled": false}, 200)
	if ui := h.get("/api/plugins/contributions"); len(ui.list("quick_commands")) != 0 {
		t.Fatal("a disabled plugin still contributes")
	}
	// Scope to a project that does not exist is refused.
	h.request2("PUT", "/api/plugins/acme.kit", obj{"project_ids": []int{99999}}, 422)
	h.request2("DELETE", "/api/plugins/acme.kit", nil, 200)
	h.request2("DELETE", "/api/plugins/lectern.spec-kit", nil, 409)
}

// Configuration made before plugins existed keeps working and is listed as
// a local contribution: a custom agent, a project's MCP servers, and a
// workflow already enabled for a project under the pre-plugin ids.
func TestExistingConfigurationMigratesUntouched(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth = "none" })
	pid := h.seededProjectID()
	db := h.App.DB
	if err := db.SetSetting("agents", `[{"name":"mybot","command":"mybot --tui"}]`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projects SET mcp_json=? WHERE id=?`, `{"mcpServers":{"docs":{"url":"https://d.example"}}}`, pid); err != nil {
		t.Fatal(err)
	}
	p, err := db.Project(pid)
	if err != nil {
		t.Fatal(err)
	}
	// A Spec Kit attachment exactly as the pre-plugin code wrote it.
	att, err := db.InsertProjectSkill(&store.ProjectSkill{ProjectID: pid, TargetID: p.TargetID, Agent: "claude",
		SkillID: "lectern-workflow/spec-kit", SourceID: "lectern-bundled/spec-kit",
		SourcePath: "/home/x/.lectern/workflows/spec-kit/v", EntryName: "lectern-spec-kit", TargetRel: ".claude/skills/lectern-spec-kit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMaterialization(&store.SkillMaterialization{AttachmentID: att.ID, TargetID: p.TargetID,
		WorktreePath: p.RepoPath, TargetPath: filepath.Join(p.RepoPath, ".claude/skills/lectern-spec-kit"),
		SourcePath: "/home/x/.lectern/workflows/spec-kit/v", TargetRel: ".claude/skills/lectern-spec-kit", State: "owned"}); err != nil {
		t.Fatal(err)
	}

	list := h.get("/api/plugins")
	local := list.sub("local")
	if strings.Join(strs(local["agents"]), ",") != "mybot" {
		t.Fatalf("local agents %v", local["agents"])
	}
	mcp := local.list("mcp_servers")
	if len(mcp) != 1 || strings.Join(strs(mcp[0]["servers"]), ",") != "docs" {
		t.Fatalf("local mcp %v", mcp)
	}
	var bundled []string
	for _, p := range list.list("plugins") {
		if p["bundled"] == true && p.str("status") == "active" {
			bundled = append(bundled, p.str("id"))
		}
	}
	if strings.Join(bundled, ",") != "lectern.agent-catalog,lectern.delegate,lectern.maestro,lectern.spec-kit" {
		t.Fatalf("bundled %v", bundled)
	}
	for _, w := range h.get(fmt.Sprintf("/api/projects/%d/workflows?agent=claude", pid)).list("workflows") {
		if w.str("id") == "spec-kit" && w["enabled"] != true {
			t.Fatal("a Spec Kit attachment made before plugins no longer reads as enabled")
		}
	}
	// The custom agent is still an agent.
	found := false
	for _, a := range h.getList("/api/agents") {
		found = found || a.str("name") == "mybot"
	}
	if !found {
		t.Fatal("the custom agent disappeared")
	}
}

// A task's agent gets enabled plugins' MCP servers next to the project's
// own; on a name clash the project's server is the one used.
func TestPluginMCPServersReachTaskLaunches(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth = "none" })
	pid := h.seededProjectID()
	pv := h.post("/api/plugins/preview", obj{"kind": "path", "path": writeAPIPlugin(t)}, 200)
	h.post("/api/plugins/install", obj{"hash": pv.str("hash"), "accept": pv["accept"]}, 200)
	lastSnapshot := func() string {
		var raw string
		if err := h.App.DB.QueryRow(`SELECT mcp_json FROM attempts ORDER BY id DESC LIMIT 1`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	h.run(pid, "with plugin mcp", "go", obj{"agent": "claude"})
	if snap := lastSnapshot(); !strings.Contains(snap, `"kitdocs":{"url":"https://mcp.example.com"}`) {
		t.Fatalf("attempt MCP snapshot %s", snap)
	}
	if !strings.Contains(h.launchCmd(), "--mcp-config") {
		t.Fatalf("launch without --mcp-config: %s", h.launchCmd())
	}
	if _, err := h.App.DB.Exec(`UPDATE projects SET mcp_json=? WHERE id=?`, `{"kitdocs":{"url":"https://mine.example"}}`, pid); err != nil {
		t.Fatal(err)
	}
	h.run(pid, "project wins", "go", obj{"agent": "claude"})
	if snap := lastSnapshot(); !strings.Contains(snap, "mine.example") || strings.Contains(snap, "mcp.example.com") {
		t.Fatalf("project server did not win: %s", snap)
	}
}
